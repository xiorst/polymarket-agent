package scalper

import (
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Polymarket CTF Exchange EIP-712 constants
const (
	eip712ProtocolName    = "Polymarket CTF Exchange"
	eip712ProtocolVersion = "1"
	// Polygon mainnet chain ID
	polygonChainID = 137
)

// CTF Exchange contract address (Polygon mainnet)
const ctfExchangeAddress = "0x4bFb41d5B3570DeFd03C39a9A4D8dE6Bd8B8982E"

// EIP-712 type hashes
var (
	// keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)")
	eip712DomainTypeHash = crypto.Keccak256Hash([]byte(
		"EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)",
	))

	// keccak256("Order(uint256 salt,address maker,address signer,address taker,uint256 tokenId,uint256 makerAmount,uint256 takerAmount,uint256 expiration,uint256 nonce,uint256 feeRateBps,uint8 side,uint8 signatureType)")
	orderTypeHash = crypto.Keccak256Hash([]byte(
		"Order(uint256 salt,address maker,address signer,address taker,uint256 tokenId,uint256 makerAmount,uint256 takerAmount,uint256 expiration,uint256 nonce,uint256 feeRateBps,uint8 side,uint8 signatureType)",
	))
)

// OrderStruct holds the fields for EIP-712 signing.
type OrderStruct struct {
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
	Side          uint8 // 0=BUY, 1=SELL
	SignatureType uint8 // 0=EOA
}

// domainSeparator computes the EIP-712 domain separator.
func domainSeparator() common.Hash {
	nameHash := crypto.Keccak256Hash([]byte(eip712ProtocolName))
	versionHash := crypto.Keccak256Hash([]byte(eip712ProtocolVersion))
	chainID := big.NewInt(polygonChainID)
	contract := common.HexToAddress(ctfExchangeAddress)

	encoded := make([]byte, 0, 5*32)
	encoded = append(encoded, eip712DomainTypeHash.Bytes()...)
	encoded = append(encoded, nameHash.Bytes()...)
	encoded = append(encoded, versionHash.Bytes()...)
	encoded = append(encoded, common.LeftPadBytes(chainID.Bytes(), 32)...)
	encoded = append(encoded, common.LeftPadBytes(contract.Bytes(), 32)...)

	return crypto.Keccak256Hash(encoded)
}

// hashOrder computes the EIP-712 struct hash for an order.
func hashOrder(o *OrderStruct) common.Hash {
	encoded := make([]byte, 0, 13*32)
	encoded = append(encoded, orderTypeHash.Bytes()...)
	encoded = append(encoded, common.LeftPadBytes(o.Salt.Bytes(), 32)...)
	encoded = append(encoded, common.LeftPadBytes(o.Maker.Bytes(), 32)...)
	encoded = append(encoded, common.LeftPadBytes(o.Signer.Bytes(), 32)...)
	encoded = append(encoded, common.LeftPadBytes(o.Taker.Bytes(), 32)...)
	encoded = append(encoded, common.LeftPadBytes(o.TokenID.Bytes(), 32)...)
	encoded = append(encoded, common.LeftPadBytes(o.MakerAmount.Bytes(), 32)...)
	encoded = append(encoded, common.LeftPadBytes(o.TakerAmount.Bytes(), 32)...)
	encoded = append(encoded, common.LeftPadBytes(o.Expiration.Bytes(), 32)...)
	encoded = append(encoded, common.LeftPadBytes(o.Nonce.Bytes(), 32)...)
	encoded = append(encoded, common.LeftPadBytes(o.FeeRateBps.Bytes(), 32)...)
	encoded = append(encoded, common.LeftPadBytes([]byte{o.Side}, 32)...)
	encoded = append(encoded, common.LeftPadBytes([]byte{o.SignatureType}, 32)...)

	return crypto.Keccak256Hash(encoded)
}

// signOrder signs the order with EIP-712 and returns hex signature.
func signOrder(o *OrderStruct, privateKeyHex string) (string, error) {
	// Load private key
	pkHex := strings.TrimPrefix(privateKeyHex, "0x")
	pkBytes, err := hex.DecodeString(pkHex)
	if err != nil {
		return "", fmt.Errorf("decode private key: %w", err)
	}
	pk, err := crypto.ToECDSA(pkBytes)
	if err != nil {
		return "", fmt.Errorf("parse private key: %w", err)
	}

	// Compute EIP-712 digest: "\x19\x01" + domainSeparator + structHash
	domain := domainSeparator()
	structHash := hashOrder(o)

	digest := make([]byte, 0, 2+32+32)
	digest = append(digest, 0x19, 0x01)
	digest = append(digest, domain.Bytes()...)
	digest = append(digest, structHash.Bytes()...)

	hash := crypto.Keccak256Hash(digest)

	// Sign
	sig, err := crypto.Sign(hash.Bytes(), pk)
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}

	// Adjust V: go-ethereum returns v=0/1, EIP-712 expects v=27/28
	sig[64] += 27

	return "0x" + hex.EncodeToString(sig), nil
}

// signerAddress derives the signer address from private key hex.
func signerAddressFromKey(privateKeyHex string) (common.Address, *ecdsa.PrivateKey, error) {
	pkHex := strings.TrimPrefix(privateKeyHex, "0x")
	pkBytes, err := hex.DecodeString(pkHex)
	if err != nil {
		return common.Address{}, nil, err
	}
	pk, err := crypto.ToECDSA(pkBytes)
	if err != nil {
		return common.Address{}, nil, err
	}
	addr := crypto.PubkeyToAddress(pk.PublicKey)
	return addr, pk, nil
}
