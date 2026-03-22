package polymarket

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log/slog"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/x10rst/ai-agent-autonom/internal/models"
)

// Polymarket CTF Exchange contract addresses on Polygon mainnet.
const (
	DefaultCTFExchangeAddress = "0x4bFb41d5B3570DeFd03C39a9A4D8dE6Bd8B8982E" // Polygon mainnet
	USDCPolygon               = "0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174" // USDC (PoS) Polygon
)

// ctfExchangeABI is the relevant subset of the Polymarket CTF Exchange ABI.
// Source: https://polygonscan.com/address/0x4bFb41d5B3570DeFd03C39a9A4D8dE6Bd8B8982E#code
//
// Only fillOrder and cancelOrder are needed for trading. The full ABI can be used
// if more functions are required in the future.
const ctfExchangeABIJSON = `[
  {
    "name": "fillOrder",
    "type": "function",
    "inputs": [
      {
        "name": "order",
        "type": "tuple",
        "components": [
          {"name": "salt",          "type": "uint256"},
          {"name": "maker",         "type": "address"},
          {"name": "signer",        "type": "address"},
          {"name": "taker",         "type": "address"},
          {"name": "tokenId",       "type": "uint256"},
          {"name": "makerAmount",   "type": "uint256"},
          {"name": "takerAmount",   "type": "uint256"},
          {"name": "expiration",    "type": "uint256"},
          {"name": "nonce",         "type": "uint256"},
          {"name": "feeRateBps",    "type": "uint256"},
          {"name": "side",          "type": "uint8"},
          {"name": "signatureType", "type": "uint8"}
        ]
      },
      {"name": "fillAmount", "type": "uint256"},
      {"name": "signature",  "type": "bytes"}
    ],
    "outputs": [
      {"name": "makerAssetFilled", "type": "uint256"},
      {"name": "takerAssetFilled", "type": "uint256"}
    ],
    "stateMutability": "nonpayable"
  },
  {
    "name": "cancelOrder",
    "type": "function",
    "inputs": [
      {
        "name": "order",
        "type": "tuple",
        "components": [
          {"name": "salt",          "type": "uint256"},
          {"name": "maker",         "type": "address"},
          {"name": "signer",        "type": "address"},
          {"name": "taker",         "type": "address"},
          {"name": "tokenId",       "type": "uint256"},
          {"name": "makerAmount",   "type": "uint256"},
          {"name": "takerAmount",   "type": "uint256"},
          {"name": "expiration",    "type": "uint256"},
          {"name": "nonce",         "type": "uint256"},
          {"name": "feeRateBps",    "type": "uint256"},
          {"name": "side",          "type": "uint8"},
          {"name": "signatureType", "type": "uint8"}
        ]
      }
    ],
    "outputs": [],
    "stateMutability": "nonpayable"
  }
]`

// ContractProvider implements trading.MarketContractProvider for Polymarket's CTF Exchange.
type ContractProvider struct {
	exchangeAddress common.Address
	parsedABI       abi.ABI
	signer          *OrderSigner
}

// NewContractProvider creates a ContractProvider for read-only use (paper mode / market lookup).
// For live order execution, use NewContractProviderWithSigner.
func NewContractProvider(exchangeAddr string) *ContractProvider {
	cp, _ := newContractProvider(exchangeAddr, nil)
	return cp
}

// NewContractProviderWithSigner creates a ContractProvider capable of signing orders for live mode.
func NewContractProviderWithSigner(exchangeAddr string, privateKey *ecdsa.PrivateKey) (*ContractProvider, error) {
	if privateKey == nil {
		return nil, fmt.Errorf("private key required for live order signing")
	}
	return newContractProvider(exchangeAddr, privateKey)
}

func newContractProvider(exchangeAddr string, privateKey *ecdsa.PrivateKey) (*ContractProvider, error) {
	addr := exchangeAddr
	if addr == "" {
		addr = DefaultCTFExchangeAddress
	}

	parsedABI, err := abi.JSON(strings.NewReader(ctfExchangeABIJSON))
	if err != nil {
		return nil, fmt.Errorf("parse CTF exchange ABI: %w", err)
	}

	cp := &ContractProvider{
		exchangeAddress: common.HexToAddress(addr),
		parsedABI:       parsedABI,
	}

	if privateKey != nil {
		cp.signer = NewOrderSigner(privateKey)
	}

	return cp, nil
}

// GetMarketContract returns the CTF Exchange address (all Polymarket markets share one contract).
func (cp *ContractProvider) GetMarketContract(_ context.Context, _ uuid.UUID) (common.Address, error) {
	if cp.exchangeAddress == (common.Address{}) {
		return common.Address{}, fmt.Errorf("CTF exchange address not configured")
	}
	return cp.exchangeAddress, nil
}

// BuildOrderCalldata encodes a fillOrder call using the real ABI + EIP-712 signature.
//
// For live mode: signer must be set (use NewContractProviderWithSigner).
// For paper mode: signer may be nil; calldata is built without signature (never sent on-chain).
func (cp *ContractProvider) BuildOrderCalldata(order *models.Order) ([]byte, error) {
	// Resolve token ID from order.TokenID (set by trading engine from Polymarket API response).
	// TokenID is the CLOB token ID for the specific outcome (YES/NO).
	// If not set, order cannot be executed on-chain — return error in live mode.
	var tokenID *big.Int
	if order.TokenID != "" {
		tokenID = new(big.Int)
		if _, ok := tokenID.SetString(order.TokenID, 10); !ok {
			return nil, fmt.Errorf("invalid token_id %q: must be a decimal integer", order.TokenID)
		}
	} else {
		if cp.signer != nil {
			// Live mode: token ID required
			return nil, fmt.Errorf("order.TokenID is empty — fetch token_id from Polymarket CLOB API before placing live order")
		}
		// Paper mode: use zero (not sent on-chain)
		tokenID = big.NewInt(0)
	}

	if cp.signer == nil {
		// Paper mode: build unsigned calldata for logging/simulation purposes only
		return cp.buildUnsignedCalldata(order, tokenID)
	}

	// Live mode: build full signed order
	takerAddr := common.Address{} // zero address = open order (anyone can fill)
	ctfOrder, sig, err := cp.signer.BuildSignedOrder(order, tokenID, takerAddr)
	if err != nil {
		return nil, fmt.Errorf("build signed order: %w", err)
	}

	slog.Debug("built signed CTF order",
		"maker", ctfOrder.Maker.Hex(),
		"side", ctfOrder.Side,
		"maker_amount", ctfOrder.MakerAmount,
		"taker_amount", ctfOrder.TakerAmount,
		"expiration", ctfOrder.Expiration,
		"sig_len", len(sig),
	)

	return cp.encodefillOrder(ctfOrder, sig)
}

// buildUnsignedCalldata builds calldata without a signature (paper mode only).
func (cp *ContractProvider) buildUnsignedCalldata(order *models.Order, tokenID *big.Int) ([]byte, error) {
	var side uint8
	if order.Side == models.OrderSideSell {
		side = 1
	}

	usdcMul := big.NewInt(1_000_000)
	priceF, _ := order.Price.Float64()
	qtyF, _ := order.Quantity.Float64()

	makerAmount := new(big.Int).SetInt64(int64(priceF * float64(qtyF) * 1_000_000))
	takerAmount := new(big.Int).SetInt64(int64(qtyF * 1_000_000))
	_ = usdcMul

	ctfOrder := &CTFOrder{
		Salt:          big.NewInt(1),
		Maker:         common.Address{},
		Signer:        common.Address{},
		Taker:         common.Address{},
		TokenID:       tokenID,
		MakerAmount:   makerAmount,
		TakerAmount:   takerAmount,
		Expiration:    big.NewInt(0),
		Nonce:         big.NewInt(0),
		FeeRateBps:    big.NewInt(0),
		Side:          side,
		SignatureType: 0,
	}

	return cp.encodefillOrder(ctfOrder, []byte{})
}

// encodefillOrder packs the fillOrder ABI calldata using the parsed ABI.
func (cp *ContractProvider) encodefillOrder(ctfOrder *CTFOrder, sig []byte) ([]byte, error) {
	return cp.encodeCalldata("fillOrder", ctfOrder, sig)
}

func (cp *ContractProvider) encodeCalldata(method string, ctfOrder *CTFOrder, sig []byte) ([]byte, error) {
	// Pack as Go struct matching the ABI tuple layout
	type abiOrder struct {
		Salt          *big.Int       `abi:"salt"`
		Maker         common.Address `abi:"maker"`
		Signer        common.Address `abi:"signer"`
		Taker         common.Address `abi:"taker"`
		TokenID       *big.Int       `abi:"tokenId"`
		MakerAmount   *big.Int       `abi:"makerAmount"`
		TakerAmount   *big.Int       `abi:"takerAmount"`
		Expiration    *big.Int       `abi:"expiration"`
		Nonce         *big.Int       `abi:"nonce"`
		FeeRateBps    *big.Int       `abi:"feeRateBps"`
		Side          uint8          `abi:"side"`
		SignatureType uint8          `abi:"signatureType"`
	}

	ao := abiOrder{
		Salt:          ctfOrder.Salt,
		Maker:         ctfOrder.Maker,
		Signer:        ctfOrder.Signer,
		Taker:         ctfOrder.Taker,
		TokenID:       ctfOrder.TokenID,
		MakerAmount:   ctfOrder.MakerAmount,
		TakerAmount:   ctfOrder.TakerAmount,
		Expiration:    ctfOrder.Expiration,
		Nonce:         ctfOrder.Nonce,
		FeeRateBps:    ctfOrder.FeeRateBps,
		Side:          ctfOrder.Side,
		SignatureType: ctfOrder.SignatureType,
	}

	var data []byte
	var err error

	if method == "fillOrder" {
		fillAmount := ctfOrder.TakerAmount
		data, err = cp.parsedABI.Pack("fillOrder", ao, fillAmount, sig)
	} else {
		data, err = cp.parsedABI.Pack("cancelOrder", ao)
	}

	if err != nil {
		return nil, fmt.Errorf("ABI pack %s: %w", method, err)
	}

	return data, nil
}

// BuildCancelCalldata encodes a cancelOrder call for a previously submitted order.
// The full CTFOrder struct is needed because the contract cancels by order hash.
func (cp *ContractProvider) BuildCancelCalldata(_ uuid.UUID) ([]byte, error) {
	// To cancel we need the original CTFOrder struct.
	// In production, this would be retrieved from the database where the signed order was stored.
	// For now, return an error to signal that the order must be fetched first.
	return nil, fmt.Errorf("cancel requires the original signed CTFOrder — fetch from DB first")
}

// BuildCancelCalldataFromOrder encodes cancelOrder calldata from a stored CTFOrder.
func (cp *ContractProvider) BuildCancelCalldataFromOrder(ctfOrder *CTFOrder) ([]byte, error) {
	return cp.encodeCalldata("cancelOrder", ctfOrder, nil)
}
