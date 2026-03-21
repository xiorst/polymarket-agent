package polymarket

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"math/rand"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/shopspring/decimal"
	"github.com/x10rst/ai-agent-autonom/internal/models"
)

// Polymarket CTF Exchange EIP-712 domain on Polygon mainnet.
// Source: https://polygonscan.com/address/0x4bFb41d5B3570DeFd03C39a9A4D8dE6Bd8B8982E
const (
	eip712DomainName    = "Polymarket CTF Exchange"
	eip712DomainVersion = "1"
	eip712ChainID       = 137 // Polygon mainnet
)

// CTFOrder mirrors the on-chain Order struct used by the CTF Exchange.
// Field order MUST match the Solidity struct exactly for correct ABI encoding.
//
//	struct Order {
//	    uint256 salt;
//	    address maker;
//	    address signer;
//	    address taker;
//	    uint256 tokenId;
//	    uint256 makerAmount;
//	    uint256 takerAmount;
//	    uint256 expiration;
//	    uint256 nonce;
//	    uint256 feeRateBps;
//	    uint8   side;          // 0 = BUY, 1 = SELL
//	    uint8   signatureType; // 0 = EOA (ECDSA)
//	}
type CTFOrder struct {
	Salt          *big.Int
	Maker         common.Address
	Signer        common.Address
	Taker         common.Address
	TokenID       *big.Int
	MakerAmount   *big.Int
	TakerAmount   *big.Int
	Expiration    *big.Int
	Nonce         *big.Int
	FeeRateBps    *big.Int
	Side          uint8
	SignatureType uint8
}

// OrderSigner signs Polymarket orders using EIP-712 typed data.
type OrderSigner struct {
	privateKey      *ecdsa.PrivateKey
	signerAddress   common.Address
	domainSeparator [32]byte
}

// NewOrderSigner creates a signer using the provided ECDSA private key.
func NewOrderSigner(privateKey *ecdsa.PrivateKey) *OrderSigner {
	address := crypto.PubkeyToAddress(privateKey.PublicKey)
	ds := computeDomainSeparator()
	return &OrderSigner{
		privateKey:      privateKey,
		signerAddress:   address,
		domainSeparator: ds,
	}
}

// BuildSignedOrder constructs a CTFOrder from an agent models.Order, signs it,
// and returns the order struct + EIP-712 signature bytes.
func (os *OrderSigner) BuildSignedOrder(
	order *models.Order,
	tokenID *big.Int,
	takerAddress common.Address,
) (*CTFOrder, []byte, error) {
	// Convert price & quantity to USDC base units (6 decimals)
	usdcMultiplier := decimal.NewFromInt(1_000_000)

	var makerAmount, takerAmount *big.Int
	if order.Side == models.OrderSideBuy {
		// Buying: maker sends USDC (makerAmount), receives outcome tokens (takerAmount)
		makerAmount = order.Price.Mul(order.Quantity).Mul(usdcMultiplier).BigInt()
		takerAmount = order.Quantity.Mul(usdcMultiplier).BigInt()
	} else {
		// Selling: maker sends outcome tokens (makerAmount), receives USDC (takerAmount)
		makerAmount = order.Quantity.Mul(usdcMultiplier).BigInt()
		takerAmount = order.Price.Mul(order.Quantity).Mul(usdcMultiplier).BigInt()
	}

	var side uint8
	if order.Side == models.OrderSideBuy {
		side = 0
	} else {
		side = 1
	}

	// Order expires in 1 hour
	expiration := big.NewInt(time.Now().Add(1 * time.Hour).Unix())

	// Random salt prevents order hash collisions
	salt := big.NewInt(rand.Int63()) //nolint:gosec — non-cryptographic salt

	ctfOrder := &CTFOrder{
		Salt:          salt,
		Maker:         os.signerAddress,
		Signer:        os.signerAddress,
		Taker:         takerAddress, // zero address = anyone can fill
		TokenID:       tokenID,
		MakerAmount:   makerAmount,
		TakerAmount:   takerAmount,
		Expiration:    expiration,
		Nonce:         big.NewInt(0),
		FeeRateBps:    big.NewInt(0),
		Side:          side,
		SignatureType: 0, // 0 = EOA ECDSA
	}

	sig, err := os.signOrder(ctfOrder)
	if err != nil {
		return nil, nil, fmt.Errorf("sign order: %w", err)
	}

	return ctfOrder, sig, nil
}

// signOrder computes the EIP-712 hash of the order and signs it.
func (os *OrderSigner) signOrder(order *CTFOrder) ([]byte, error) {
	orderHash := hashOrder(order)

	// EIP-712 final hash: keccak256("\x19\x01" || domainSeparator || orderHash)
	rawData := make([]byte, 0, 2+32+32)
	rawData = append(rawData, 0x19, 0x01)
	rawData = append(rawData, os.domainSeparator[:]...)
	rawData = append(rawData, orderHash[:]...)
	digest := crypto.Keccak256Hash(rawData)

	sig, err := crypto.Sign(digest.Bytes(), os.privateKey)
	if err != nil {
		return nil, fmt.Errorf("ecdsa sign: %w", err)
	}

	// Ethereum signature: v is 27 or 28 (not 0 or 1)
	sig[64] += 27

	return sig, nil
}

// hashOrder computes keccak256 of the EIP-712 encoded Order struct.
func hashOrder(order *CTFOrder) common.Hash {
	// ORDER_TYPEHASH = keccak256("Order(uint256 salt,address maker,address signer,address taker,
	//   uint256 tokenId,uint256 makerAmount,uint256 takerAmount,uint256 expiration,
	//   uint256 nonce,uint256 feeRateBps,uint8 side,uint8 signatureType)")
	orderTypeHash := crypto.Keccak256Hash([]byte(
		"Order(uint256 salt,address maker,address signer,address taker," +
			"uint256 tokenId,uint256 makerAmount,uint256 takerAmount,uint256 expiration," +
			"uint256 nonce,uint256 feeRateBps,uint8 side,uint8 signatureType)",
	))

	// ABI-encode the order fields (each padded to 32 bytes)
	encoded := make([]byte, 0, 13*32)
	encoded = append(encoded, orderTypeHash.Bytes()...)
	encoded = append(encoded, math.PaddedBigBytes(order.Salt, 32)...)
	encoded = append(encoded, common.LeftPadBytes(order.Maker.Bytes(), 32)...)
	encoded = append(encoded, common.LeftPadBytes(order.Signer.Bytes(), 32)...)
	encoded = append(encoded, common.LeftPadBytes(order.Taker.Bytes(), 32)...)
	encoded = append(encoded, math.PaddedBigBytes(order.TokenID, 32)...)
	encoded = append(encoded, math.PaddedBigBytes(order.MakerAmount, 32)...)
	encoded = append(encoded, math.PaddedBigBytes(order.TakerAmount, 32)...)
	encoded = append(encoded, math.PaddedBigBytes(order.Expiration, 32)...)
	encoded = append(encoded, math.PaddedBigBytes(order.Nonce, 32)...)
	encoded = append(encoded, math.PaddedBigBytes(order.FeeRateBps, 32)...)
	encoded = append(encoded, common.LeftPadBytes([]byte{order.Side}, 32)...)
	encoded = append(encoded, common.LeftPadBytes([]byte{order.SignatureType}, 32)...)

	return crypto.Keccak256Hash(encoded)
}

// computeDomainSeparator builds the EIP-712 domain separator for Polymarket CTF Exchange.
func computeDomainSeparator() [32]byte {
	// DOMAIN_TYPEHASH = keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)")
	domainTypeHash := crypto.Keccak256Hash([]byte(
		"EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)",
	))

	nameHash := crypto.Keccak256Hash([]byte(eip712DomainName))
	versionHash := crypto.Keccak256Hash([]byte(eip712DomainVersion))
	chainID := big.NewInt(eip712ChainID)
	contractAddr := common.HexToAddress(DefaultCTFExchangeAddress)

	encoded := make([]byte, 0, 5*32)
	encoded = append(encoded, domainTypeHash.Bytes()...)
	encoded = append(encoded, nameHash.Bytes()...)
	encoded = append(encoded, versionHash.Bytes()...)
	encoded = append(encoded, math.PaddedBigBytes(chainID, 32)...)
	encoded = append(encoded, common.LeftPadBytes(contractAddr.Bytes(), 32)...)

	return crypto.Keccak256Hash(encoded)
}
