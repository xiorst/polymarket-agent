package risk

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/x10rst/ai-agent-autonom/internal/config"
	"github.com/x10rst/ai-agent-autonom/internal/models"
	"github.com/x10rst/ai-agent-autonom/internal/notification"
)

func newTestManager() *Manager {
	cfg := config.RiskConfig{
		MaxSlippageTolerance:    0.05,
		StopLoss:                0.15,
		MaxPositionPerMarket:    0.10,
		MaxTotalExposure:        0.70,
		DailyLossLimit:          0.05,
		MaxPriceImpactThreshold: 0.03,
		PriceImpactAutoSplit:    true,
		OrderSplitThreshold:     100.00,
		OrderSplitMaxChunks:     5,
		OrderSplitDelayMs:       100,
	}
	notifier := notification.New(config.NotificationConfig{})
	return NewManager(cfg, nil, notifier) // nil db — tests that don't query DB
}

// --- Stop-Loss Tests ---

func TestCheckStopLoss_Triggered(t *testing.T) {
	mgr := newTestManager()
	ctx := context.Background()

	positions := []models.Position{
		{
			ID:         uuid.New(),
			MarketID:   uuid.New(),
			Outcome:    "Yes",
			EntryPrice: decimal.NewFromFloat(0.60),
			Quantity:   decimal.NewFromFloat(100),
			Status:     models.PositionStatusOpen,
		},
	}

	// Price dropped 20% from 0.60 to 0.48 → exceeds 15% stop-loss
	currentPrices := map[string]decimal.Decimal{
		"Yes": decimal.NewFromFloat(0.48),
	}

	triggered := mgr.CheckStopLoss(ctx, positions, currentPrices)
	if len(triggered) != 1 {
		t.Fatalf("expected 1 triggered stop-loss, got %d", len(triggered))
	}
	if triggered[0].Outcome != "Yes" {
		t.Errorf("expected outcome 'Yes', got %q", triggered[0].Outcome)
	}
}

func TestCheckStopLoss_NotTriggered(t *testing.T) {
	mgr := newTestManager()
	ctx := context.Background()

	positions := []models.Position{
		{
			ID:         uuid.New(),
			MarketID:   uuid.New(),
			Outcome:    "Yes",
			EntryPrice: decimal.NewFromFloat(0.60),
			Quantity:   decimal.NewFromFloat(100),
			Status:     models.PositionStatusOpen,
		},
	}

	// Price dropped 5% from 0.60 to 0.57 → within 15% threshold
	currentPrices := map[string]decimal.Decimal{
		"Yes": decimal.NewFromFloat(0.57),
	}

	triggered := mgr.CheckStopLoss(ctx, positions, currentPrices)
	if len(triggered) != 0 {
		t.Fatalf("expected 0 triggered stop-losses, got %d", len(triggered))
	}
}

func TestCheckStopLoss_SkipsClosedPositions(t *testing.T) {
	mgr := newTestManager()
	ctx := context.Background()

	positions := []models.Position{
		{
			ID:         uuid.New(),
			Outcome:    "Yes",
			EntryPrice: decimal.NewFromFloat(0.60),
			Quantity:   decimal.NewFromFloat(100),
			Status:     models.PositionStatusClosed, // already closed
		},
	}

	currentPrices := map[string]decimal.Decimal{
		"Yes": decimal.NewFromFloat(0.10), // huge drop but already closed
	}

	triggered := mgr.CheckStopLoss(ctx, positions, currentPrices)
	if len(triggered) != 0 {
		t.Fatalf("expected 0 triggered (position closed), got %d", len(triggered))
	}
}

func TestCheckStopLoss_ProfitablePosition(t *testing.T) {
	mgr := newTestManager()
	ctx := context.Background()

	positions := []models.Position{
		{
			ID:         uuid.New(),
			Outcome:    "Yes",
			EntryPrice: decimal.NewFromFloat(0.40),
			Quantity:   decimal.NewFromFloat(100),
			Status:     models.PositionStatusOpen,
		},
	}

	// Price went UP from 0.40 to 0.65 → profitable, no stop-loss
	currentPrices := map[string]decimal.Decimal{
		"Yes": decimal.NewFromFloat(0.65),
	}

	triggered := mgr.CheckStopLoss(ctx, positions, currentPrices)
	if len(triggered) != 0 {
		t.Fatalf("expected 0 triggered (profitable), got %d", len(triggered))
	}
}

// --- Order Splitting Tests ---

func TestShouldSplit_AboveThreshold(t *testing.T) {
	mgr := newTestManager()
	orderValue := decimal.NewFromFloat(150.00) // > 100 threshold
	if !mgr.ShouldSplit(orderValue) {
		t.Error("expected ShouldSplit to return true for 150 USDC")
	}
}

func TestShouldSplit_BelowThreshold(t *testing.T) {
	mgr := newTestManager()
	orderValue := decimal.NewFromFloat(50.00) // < 100 threshold
	if mgr.ShouldSplit(orderValue) {
		t.Error("expected ShouldSplit to return false for 50 USDC")
	}
}

func TestSplitOrder_DividesEvenly(t *testing.T) {
	mgr := newTestManager()

	order := &models.Order{
		ID:       uuid.New(),
		Price:    decimal.NewFromFloat(0.50),
		Quantity: decimal.NewFromFloat(500),
	}

	chunks := mgr.SplitOrder(order)
	if len(chunks) != 5 {
		t.Fatalf("expected 5 chunks, got %d", len(chunks))
	}

	// Verify total quantity
	totalQty := decimal.Zero
	for _, c := range chunks {
		totalQty = totalQty.Add(c.Quantity)
	}

	if !totalQty.Equal(order.Quantity) {
		t.Errorf("total chunk quantity %s != original %s", totalQty, order.Quantity)
	}
}

func TestSplitOrder_SingleChunk(t *testing.T) {
	mgr := newTestManager()

	order := &models.Order{
		ID:       uuid.New(),
		Price:    decimal.NewFromFloat(0.50),
		Quantity: decimal.NewFromFloat(10), // small, but force split
	}

	chunks := mgr.SplitOrder(order)
	totalQty := decimal.Zero
	for _, c := range chunks {
		totalQty = totalQty.Add(c.Quantity)
	}

	if !totalQty.Equal(order.Quantity) {
		t.Errorf("total chunk quantity %s != original %s", totalQty, order.Quantity)
	}
}

// --- Daily Loss Limit Tests ---

func TestDailyLossLimit_RecordAndCheck(t *testing.T) {
	mgr := newTestManager()

	// Record a small loss
	mgr.RecordDailyPnL(decimal.NewFromFloat(-2.0))
	if mgr.IsDailyLimitHit() {
		t.Error("daily limit should not be hit after small loss")
	}

	// Record more losses to exceed 5% of 100 portfolio = -5
	mgr.RecordDailyPnL(decimal.NewFromFloat(-4.0))
	// Total: -6, but we can't call checkDailyLossLimit without DB
	// Verify the PnL is tracked
	if mgr.dailyPnL.GreaterThan(decimal.NewFromFloat(-5.0)) {
		t.Errorf("expected dailyPnL <= -5, got %s", mgr.dailyPnL)
	}
}

func TestDailyLossLimit_ResetsOnNewDay(t *testing.T) {
	mgr := newTestManager()

	mgr.RecordDailyPnL(decimal.NewFromFloat(-10.0))
	mgr.dailyLimitHit = true

	// Simulate new day
	mgr.dailyPnLDate = time.Now().Add(-25 * time.Hour).Truncate(24 * time.Hour)

	// Record a new trade — should reset
	mgr.RecordDailyPnL(decimal.NewFromFloat(1.0))

	if mgr.dailyLimitHit {
		t.Error("daily limit should have reset on new day")
	}
	if !mgr.dailyPnL.Equal(decimal.NewFromFloat(1.0)) {
		t.Errorf("expected dailyPnL = 1.0 after reset, got %s", mgr.dailyPnL)
	}
}

// --- Price Impact Calculation Tests ---

func TestCalculatePriceImpact_BuyOrder(t *testing.T) {
	bids := []struct{ Price, Qty float64 }{
		{0.50, 100},
		{0.49, 200},
	}
	asks := []struct{ Price, Qty float64 }{
		{0.52, 50},
		{0.55, 100},
		{0.60, 200},
	}

	impact, avgPrice := CalculatePriceImpact(100, models.OrderSideBuy, bids, asks)

	// Mid price = (0.50 + 0.52) / 2 = 0.51
	// Buying 100: 50 @ 0.52, 50 @ 0.55 → avg = 0.535
	// Impact = (0.535 - 0.51) / 0.51 ≈ 0.049
	if impact < 0.04 || impact > 0.06 {
		t.Errorf("expected impact around 0.049, got %f", impact)
	}
	if avgPrice < 0.53 || avgPrice > 0.54 {
		t.Errorf("expected avg price around 0.535, got %f", avgPrice)
	}
}

func TestCalculatePriceImpact_EmptyBook(t *testing.T) {
	bids := []struct{ Price, Qty float64 }{}
	asks := []struct{ Price, Qty float64 }{}

	impact, _ := CalculatePriceImpact(100, models.OrderSideBuy, bids, asks)
	if impact != 0 {
		t.Errorf("expected 0 impact for empty book, got %f", impact)
	}
}

func TestCalculatePriceImpact_ZeroQuantity(t *testing.T) {
	bids := []struct{ Price, Qty float64 }{{0.50, 100}}
	asks := []struct{ Price, Qty float64 }{{0.52, 100}}

	impact, _ := CalculatePriceImpact(0, models.OrderSideBuy, bids, asks)
	if impact != 0 {
		t.Errorf("expected 0 impact for zero qty, got %f", impact)
	}
}

// --- Slippage Check Tests ---

func TestCheckSlippage_LimitOrderPassesAlways(t *testing.T) {
	mgr := newTestManager()

	order := &models.Order{
		OrderType: models.OrderTypeLimit,
	}

	err := mgr.checkSlippage(order)
	if err != nil {
		t.Errorf("limit order slippage check should always pass, got: %s", err)
	}
}
