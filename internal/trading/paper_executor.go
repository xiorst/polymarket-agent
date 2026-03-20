package trading

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/x10rst/ai-agent-autonom/internal/models"
)

// PaperConfig controls how realistic the paper simulation is.
type PaperConfig struct {
	// MaxSlippagePct is the maximum random slippage applied to fill price (e.g. 0.02 = 2%).
	MaxSlippagePct float64

	// GasCostUSD is the fixed fee deducted per order to simulate on-chain gas (e.g. 0.02).
	GasCostUSD float64

	// PartialFillProbability is the probability (0–1) that an order is only partially filled.
	PartialFillProbability float64

	// PartialFillMinRatio is the minimum fill ratio when partial fill occurs (e.g. 0.50 = 50%).
	PartialFillMinRatio float64
}

// DefaultPaperConfig returns a config that closely mirrors real Polygon/Polymarket conditions.
func DefaultPaperConfig() PaperConfig {
	return PaperConfig{
		MaxSlippagePct:         0.02,  // up to 2% slippage
		GasCostUSD:             0.02,  // ~$0.02 gas per order on Polygon
		PartialFillProbability: 0.20,  // 20% chance of partial fill
		PartialFillMinRatio:    0.50,  // if partial, fill at least 50%
	}
}

// PaperExecutor simulates order execution with realistic market conditions:
//   - Random slippage on fill price
//   - Fixed gas cost deduction per order
//   - Probabilistic partial fills
//
// The executor mutates order.FillPrice, order.FilledQuantity, order.FeeCost,
// and order.Status before returning so the engine can persist accurate data.
type PaperExecutor struct {
	cfg PaperConfig
	rng *rand.Rand
}

func NewPaperExecutor() *PaperExecutor {
	return &PaperExecutor{
		cfg: DefaultPaperConfig(),
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// NewPaperExecutorWithConfig creates a paper executor with custom simulation parameters.
// Useful for testing with deterministic seeds or tighter/looser slippage.
func NewPaperExecutorWithConfig(cfg PaperConfig, seed int64) *PaperExecutor {
	return &PaperExecutor{
		cfg: cfg,
		rng: rand.New(rand.NewSource(seed)),
	}
}

func (pe *PaperExecutor) PlaceOrder(_ context.Context, order *models.Order) (string, error) {
	// 1. Simulate slippage: fill price deviates from requested price
	fillPrice := pe.applySlippage(order)

	// 2. Simulate partial fill
	filledQty, isPartial := pe.applyPartialFill(order)

	// 3. Apply gas cost
	feeCost := decimal.NewFromFloat(pe.cfg.GasCostUSD)

	// 4. Mutate order so the engine persists accurate fill data
	order.FillPrice = fillPrice
	order.FilledQuantity = filledQty
	order.FeeCost = feeCost
	if isPartial {
		order.Status = models.OrderStatusPartiallyFilled
	}

	fakeTxHash := fmt.Sprintf("0xpaper_%s", order.ID.String()[:8])

	slog.Info("[PAPER] order simulated",
		"id", order.ID,
		"side", order.Side,
		"outcome", order.Outcome,
		"requested_price", order.Price,
		"fill_price", fillPrice,
		"requested_qty", order.Quantity,
		"filled_qty", filledQty,
		"fee_cost_usd", feeCost,
		"partial", isPartial,
	)

	return fakeTxHash, nil
}

func (pe *PaperExecutor) CancelOrder(_ context.Context, orderID uuid.UUID) error {
	slog.Info("[PAPER] order cancelled", "id", orderID)
	return nil
}

// applySlippage returns a fill price with random slippage applied.
// Buy orders slip upward (pay more), sell orders slip downward (receive less).
func (pe *PaperExecutor) applySlippage(order *models.Order) decimal.Decimal {
	if pe.cfg.MaxSlippagePct == 0 {
		return order.Price
	}

	// Random slippage between 0 and MaxSlippagePct
	slippagePct := pe.rng.Float64() * pe.cfg.MaxSlippagePct
	slippageFactor := decimal.NewFromFloat(slippagePct)

	if order.Side == models.OrderSideBuy {
		// Buying: pay more than requested
		return order.Price.Add(order.Price.Mul(slippageFactor))
	}
	// Selling: receive less than requested
	return order.Price.Sub(order.Price.Mul(slippageFactor))
}

// applyPartialFill returns the filled quantity and whether it was a partial fill.
func (pe *PaperExecutor) applyPartialFill(order *models.Order) (filledQty decimal.Decimal, isPartial bool) {
	if pe.cfg.PartialFillProbability == 0 || pe.rng.Float64() >= pe.cfg.PartialFillProbability {
		return order.Quantity, false
	}

	// Fill between MinRatio and 99% of requested quantity
	minRatio := pe.cfg.PartialFillMinRatio
	if minRatio <= 0 {
		minRatio = 0.50
	}
	fillRatio := minRatio + pe.rng.Float64()*(0.99-minRatio)
	filledQty = order.Quantity.Mul(decimal.NewFromFloat(fillRatio)).Round(8)
	return filledQty, true
}
