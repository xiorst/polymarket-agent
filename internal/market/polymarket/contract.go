package polymarket

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/x10rst/ai-agent-autonom/internal/models"
)

// Polymarket CTF Exchange contract addresses on Polygon mainnet.
const (
	DefaultCTFExchangeAddress = "0x4bFb41d5B3570DeFd03C39a9A4D8dE6Bd8B8982E" // Polygon mainnet
	USDCPolygon               = "0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174" // USDC (PoS) Polygon
)

// ContractProvider implements trading.MarketContractProvider for Polymarket's CTF Exchange.
type ContractProvider struct {
	exchangeAddress common.Address
}

func NewContractProvider(exchangeAddr string) *ContractProvider {
	addr := exchangeAddr
	if addr == "" {
		addr = DefaultCTFExchangeAddress
	}
	return &ContractProvider{
		exchangeAddress: common.HexToAddress(addr),
	}
}

// GetMarketContract returns the CTF Exchange address (all markets go through the same contract).
func (cp *ContractProvider) GetMarketContract(_ context.Context, _ uuid.UUID) (common.Address, error) {
	if cp.exchangeAddress == (common.Address{}) {
		return common.Address{}, fmt.Errorf("CTF exchange address not configured")
	}
	return cp.exchangeAddress, nil
}

// BuildOrderCalldata encodes an order for the CTF Exchange.
//
// The Polymarket CTF Exchange uses a signed order model (EIP-712).
// This method builds the calldata for fillOrder(Order, Signature).
//
// Simplified ABI encoding — in production, use abigen-generated bindings.
func (cp *ContractProvider) BuildOrderCalldata(order *models.Order) ([]byte, error) {
	// fillOrder selector: placeholder — actual selector depends on the deployed contract ABI
	// For Polymarket's CTF Exchange: fillOrder(Order order, Signature sig)
	//
	// Order struct:
	//   uint256 salt
	//   address maker
	//   address signer
	//   address taker
	//   uint256 tokenId
	//   uint256 makerAmount
	//   uint256 takerAmount
	//   uint256 expiration
	//   uint256 nonce
	//   uint256 feeRateBps
	//   uint8   side         (0=BUY, 1=SELL)
	//   uint8   signatureType

	// Convert price & quantity to USDC amounts (6 decimals)
	makerAmount := order.Price.Mul(order.Quantity).Mul(decimal.NewFromInt(1_000_000))
	takerAmount := order.Quantity.Mul(decimal.NewFromInt(1_000_000))

	var side uint8
	if order.Side == models.OrderSideBuy {
		side = 0
	} else {
		side = 1
	}

	slog.Debug("building order calldata",
		"side", side,
		"maker_amount", makerAmount,
		"taker_amount", takerAmount,
		"outcome", order.Outcome,
	)

	// Build ABI-encoded calldata
	// In production, use github.com/ethereum/go-ethereum/accounts/abi for proper encoding
	//
	// For now, we encode a simplified representation:
	// bytes4 selector + encoded params

	// Placeholder selector for fillOrder
	selector := common.Hex2Bytes("d798eff6")

	// Encode side (uint8, padded to 32 bytes)
	sidePadded := common.LeftPadBytes([]byte{side}, 32)

	// Encode makerAmount (uint256)
	makerBig := makerAmount.BigInt()
	makerPadded := common.LeftPadBytes(makerBig.Bytes(), 32)

	// Encode takerAmount (uint256)
	takerBig := takerAmount.BigInt()
	takerPadded := common.LeftPadBytes(takerBig.Bytes(), 32)

	// Encode expiration (1 hour from now)
	expiration := big.NewInt(0) // 0 = no expiration
	expirationPadded := common.LeftPadBytes(expiration.Bytes(), 32)

	data := make([]byte, 0, 4+32*4)
	data = append(data, selector...)
	data = append(data, sidePadded...)
	data = append(data, makerPadded...)
	data = append(data, takerPadded...)
	data = append(data, expirationPadded...)

	return data, nil
}

// BuildCancelCalldata encodes a cancel order request.
func (cp *ContractProvider) BuildCancelCalldata(orderID uuid.UUID) ([]byte, error) {
	// cancelOrder selector: placeholder
	selector := common.Hex2Bytes("514fcac7")

	// Encode orderID as bytes32
	orderBytes := common.LeftPadBytes(orderID[:], 32)

	data := make([]byte, 0, 4+32)
	data = append(data, selector...)
	data = append(data, orderBytes...)

	return data, nil
}
