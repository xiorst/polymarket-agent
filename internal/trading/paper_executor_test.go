package trading

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/x10rst/ai-agent-autonom/internal/models"
)

func newTestOrder(side models.OrderSide, price, qty float64) *models.Order {
	p := decimal.NewFromFloat(price)
	q := decimal.NewFromFloat(qty)
	return &models.Order{
		ID:       uuid.New(),
		MarketID: uuid.New(),
		Side:     side,
		Outcome:  "YES",
		Price:    p,
		Quantity: q,
		Status:   models.OrderStatusPending,
	}
}

// TestPaperExecutor_NoSimulation verifies zero-config executor always fills at requested price.
func TestPaperExecutor_NoSimulation(t *testing.T) {
	pe := NewPaperExecutorWithConfig(PaperConfig{}, 42)
	order := newTestOrder(models.OrderSideBuy, 0.60, 10.0)

	_, err := pe.PlaceOrder(context.Background(), order)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !order.FillPrice.Equal(order.Price) {
		t.Errorf("expected fill price %s, got %s", order.Price, order.FillPrice)
	}
	if !order.FilledQuantity.Equal(order.Quantity) {
		t.Errorf("expected filled qty %s, got %s", order.Quantity, order.FilledQuantity)
	}
	if order.Status == models.OrderStatusPartiallyFilled {
		t.Error("expected full fill with zero partial-fill probability")
	}
}

// TestPaperExecutor_BuySlipsUp verifies buy orders always pay >= requested price.
func TestPaperExecutor_BuySlipsUp(t *testing.T) {
	cfg := PaperConfig{MaxSlippagePct: 0.02}
	pe := NewPaperExecutorWithConfig(cfg, 1)

	for i := 0; i < 50; i++ {
		order := newTestOrder(models.OrderSideBuy, 0.60, 5.0)
		if _, err := pe.PlaceOrder(context.Background(), order); err != nil {
			t.Fatal(err)
		}
		if order.FillPrice.LessThan(order.Price) {
			t.Errorf("buy fill price %s should be >= requested %s", order.FillPrice, order.Price)
		}
		maxAllowed := order.Price.Mul(decimal.NewFromFloat(1.02))
		if order.FillPrice.GreaterThan(maxAllowed) {
			t.Errorf("buy fill price %s exceeds max allowed %s", order.FillPrice, maxAllowed)
		}
	}
}

// TestPaperExecutor_SellSlipsDown verifies sell orders always receive <= requested price.
func TestPaperExecutor_SellSlipsDown(t *testing.T) {
	cfg := PaperConfig{MaxSlippagePct: 0.02}
	pe := NewPaperExecutorWithConfig(cfg, 2)

	for i := 0; i < 50; i++ {
		order := newTestOrder(models.OrderSideSell, 0.60, 5.0)
		if _, err := pe.PlaceOrder(context.Background(), order); err != nil {
			t.Fatal(err)
		}
		if order.FillPrice.GreaterThan(order.Price) {
			t.Errorf("sell fill price %s should be <= requested %s", order.FillPrice, order.Price)
		}
		minAllowed := order.Price.Mul(decimal.NewFromFloat(0.98))
		if order.FillPrice.LessThan(minAllowed) {
			t.Errorf("sell fill price %s below min allowed %s", order.FillPrice, minAllowed)
		}
	}
}

// TestPaperExecutor_GasCost verifies fee cost is always applied.
func TestPaperExecutor_GasCost(t *testing.T) {
	cfg := PaperConfig{GasCostUSD: 0.02}
	pe := NewPaperExecutorWithConfig(cfg, 3)

	order := newTestOrder(models.OrderSideBuy, 0.50, 10.0)
	if _, err := pe.PlaceOrder(context.Background(), order); err != nil {
		t.Fatal(err)
	}

	expected := decimal.NewFromFloat(0.02)
	if !order.FeeCost.Equal(expected) {
		t.Errorf("expected fee cost %s, got %s", expected, order.FeeCost)
	}
}

// TestPaperExecutor_PartialFillAlways uses 100% probability to guarantee partial fill.
func TestPaperExecutor_PartialFillAlways(t *testing.T) {
	cfg := PaperConfig{
		PartialFillProbability: 1.0,
		PartialFillMinRatio:    0.50,
	}
	pe := NewPaperExecutorWithConfig(cfg, 4)

	order := newTestOrder(models.OrderSideBuy, 0.70, 20.0)
	if _, err := pe.PlaceOrder(context.Background(), order); err != nil {
		t.Fatal(err)
	}
	if order.Status != models.OrderStatusPartiallyFilled {
		t.Errorf("expected PartiallyFilled, got %s", order.Status)
	}
	if order.FilledQuantity.GreaterThanOrEqual(order.Quantity) {
		t.Errorf("partial fill qty %s should be < requested %s", order.FilledQuantity, order.Quantity)
	}
	minQty := order.Quantity.Mul(decimal.NewFromFloat(0.50))
	if order.FilledQuantity.LessThan(minQty) {
		t.Errorf("partial fill qty %s below min ratio (50%%) of %s", order.FilledQuantity, order.Quantity)
	}
}

// TestPaperExecutor_PartialFillNever uses 0% probability to guarantee full fill.
func TestPaperExecutor_PartialFillNever(t *testing.T) {
	cfg := PaperConfig{PartialFillProbability: 0.0}
	pe := NewPaperExecutorWithConfig(cfg, 5)

	for i := 0; i < 20; i++ {
		order := newTestOrder(models.OrderSideBuy, 0.55, 8.0)
		if _, err := pe.PlaceOrder(context.Background(), order); err != nil {
			t.Fatal(err)
		}
		if order.Status == models.OrderStatusPartiallyFilled {
			t.Error("expected full fill with 0% partial-fill probability")
		}
		if !order.FilledQuantity.Equal(order.Quantity) {
			t.Errorf("expected full qty %s, got %s", order.Quantity, order.FilledQuantity)
		}
	}
}

// TestPaperExecutor_DefaultConfig smoke-tests all features together.
func TestPaperExecutor_DefaultConfig(t *testing.T) {
	pe := NewPaperExecutor()

	for i := 0; i < 100; i++ {
		order := newTestOrder(models.OrderSideBuy, 0.65, 15.0)
		txHash, err := pe.PlaceOrder(context.Background(), order)
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}
		if txHash == "" {
			t.Fatal("expected non-empty tx hash")
		}
		// FillPrice must be >= requested for buys
		if order.FillPrice.LessThan(order.Price) {
			t.Errorf("iteration %d: buy fill price %s < requested %s", i, order.FillPrice, order.Price)
		}
		// FilledQuantity must be <= requested
		if order.FilledQuantity.GreaterThan(order.Quantity) {
			t.Errorf("iteration %d: filled qty %s > requested qty %s", i, order.FilledQuantity, order.Quantity)
		}
		// FeeCost must equal configured amount
		expectedFee := decimal.NewFromFloat(DefaultPaperConfig().GasCostUSD)
		if !order.FeeCost.Equal(expectedFee) {
			t.Errorf("iteration %d: fee cost %s != expected %s", i, order.FeeCost, expectedFee)
		}
	}
}
