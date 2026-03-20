package risk

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/x10rst/ai-agent-autonom/internal/config"
	"github.com/x10rst/ai-agent-autonom/internal/models"
	"github.com/x10rst/ai-agent-autonom/internal/notification"
)

type Manager struct {
	cfg      config.RiskConfig
	db       *pgxpool.Pool
	notifier *notification.Notifier

	dailyPnL       decimal.Decimal
	dailyPnLDate   time.Time
	dailyLimitHit  bool
}

func NewManager(cfg config.RiskConfig, db *pgxpool.Pool, notifier *notification.Notifier) *Manager {
	return &Manager{
		cfg:          cfg,
		db:           db,
		notifier:     notifier,
		dailyPnLDate: time.Now().Truncate(24 * time.Hour),
	}
}

// PreTradeCheck validates an order against all risk rules before execution.
func (m *Manager) PreTradeCheck(ctx context.Context, order *models.Order, portfolioBalance decimal.Decimal) error {
	// Check daily loss limit
	if err := m.checkDailyLossLimit(ctx, portfolioBalance); err != nil {
		return err
	}

	// Check position sizing per market
	if err := m.checkPositionSize(ctx, order, portfolioBalance); err != nil {
		return err
	}

	// Check total exposure
	if err := m.checkTotalExposure(ctx, order, portfolioBalance); err != nil {
		return err
	}

	// Check slippage tolerance (for limit orders, price is already set)
	if err := m.checkSlippage(order); err != nil {
		return err
	}

	return nil
}

// ShouldSplit returns true if the order should be split into chunks.
func (m *Manager) ShouldSplit(orderValue decimal.Decimal) bool {
	threshold := decimal.NewFromFloat(m.cfg.OrderSplitThreshold)
	return orderValue.GreaterThan(threshold)
}

// SplitOrder divides a large order into smaller chunks.
func (m *Manager) SplitOrder(order *models.Order) []models.Order {
	totalQty := order.Quantity
	maxChunks := m.cfg.OrderSplitMaxChunks
	chunkQty := totalQty.Div(decimal.NewFromInt(int64(maxChunks)))

	var chunks []models.Order
	remaining := totalQty

	for i := 0; i < maxChunks && remaining.IsPositive(); i++ {
		qty := chunkQty
		if i == maxChunks-1 || qty.GreaterThan(remaining) {
			qty = remaining
		}

		chunk := *order
		chunk.Quantity = qty
		chunks = append(chunks, chunk)
		remaining = remaining.Sub(qty)
	}

	return chunks
}

// CheckStopLoss checks if any open position has hit the stop-loss threshold.
func (m *Manager) CheckStopLoss(ctx context.Context, positions []models.Position, currentPrices map[string]decimal.Decimal) []models.Position {
	stopLossThreshold := decimal.NewFromFloat(m.cfg.StopLoss)
	var triggered []models.Position

	for _, pos := range positions {
		if pos.Status != models.PositionStatusOpen {
			continue
		}

		currentPrice, ok := currentPrices[pos.Outcome]
		if !ok {
			continue
		}

		// Calculate unrealized loss
		pnlPct := currentPrice.Sub(pos.EntryPrice).Div(pos.EntryPrice)

		if pnlPct.IsNegative() && pnlPct.Abs().GreaterThanOrEqual(stopLossThreshold) {
			slog.Warn("stop-loss triggered",
				"market", pos.MarketID,
				"outcome", pos.Outcome,
				"entry_price", pos.EntryPrice,
				"current_price", currentPrice,
				"loss_pct", pnlPct,
			)
			triggered = append(triggered, pos)
		}
	}

	return triggered
}

// RecordDailyPnL updates the daily PnL tracker.
func (m *Manager) RecordDailyPnL(pnl decimal.Decimal) {
	today := time.Now().Truncate(24 * time.Hour)

	// Reset if new day
	if !today.Equal(m.dailyPnLDate) {
		m.dailyPnL = decimal.Zero
		m.dailyPnLDate = today
		m.dailyLimitHit = false
	}

	m.dailyPnL = m.dailyPnL.Add(pnl)
}

// IsDailyLimitHit returns true if trading should stop for the day.
func (m *Manager) IsDailyLimitHit() bool {
	return m.dailyLimitHit
}

func (m *Manager) checkDailyLossLimit(ctx context.Context, portfolioBalance decimal.Decimal) error {
	today := time.Now().Truncate(24 * time.Hour)
	if !today.Equal(m.dailyPnLDate) {
		m.dailyPnL = decimal.Zero
		m.dailyPnLDate = today
		m.dailyLimitHit = false
	}

	if m.dailyLimitHit {
		return fmt.Errorf("daily loss limit already hit, trading paused until next day")
	}

	limitAmount := portfolioBalance.Mul(decimal.NewFromFloat(m.cfg.DailyLossLimit)).Neg()
	if m.dailyPnL.LessThan(limitAmount) {
		m.dailyLimitHit = true
		m.notifier.Send(ctx, models.AlertHigh, "daily_loss_limit",
			fmt.Sprintf("Daily loss limit hit: PnL = %s, Limit = %s", m.dailyPnL, limitAmount))
		return fmt.Errorf("daily loss limit reached: %s (limit: %s)", m.dailyPnL, limitAmount)
	}

	return nil
}

func (m *Manager) checkPositionSize(ctx context.Context, order *models.Order, portfolioBalance decimal.Decimal) error {
	maxPositionValue := portfolioBalance.Mul(decimal.NewFromFloat(m.cfg.MaxPositionPerMarket))
	orderValue := order.Price.Mul(order.Quantity)

	// Get existing position value in this market
	var existingValue decimal.Decimal
	err := m.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(entry_price * quantity), 0)
		FROM positions
		WHERE market_id = $1 AND status = 'open'
	`, order.MarketID).Scan(&existingValue)
	if err != nil {
		return fmt.Errorf("query existing position: %w", err)
	}

	totalValue := existingValue.Add(orderValue)
	if totalValue.GreaterThan(maxPositionValue) {
		return fmt.Errorf("position size exceeds limit: %s > %s (%d%% of portfolio)",
			totalValue, maxPositionValue, int(m.cfg.MaxPositionPerMarket*100))
	}

	return nil
}

func (m *Manager) checkTotalExposure(ctx context.Context, order *models.Order, portfolioBalance decimal.Decimal) error {
	maxExposure := portfolioBalance.Mul(decimal.NewFromFloat(m.cfg.MaxTotalExposure))

	var currentExposure decimal.Decimal
	err := m.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(entry_price * quantity), 0)
		FROM positions
		WHERE status = 'open'
	`).Scan(&currentExposure)
	if err != nil {
		return fmt.Errorf("query total exposure: %w", err)
	}

	orderValue := order.Price.Mul(order.Quantity)
	newExposure := currentExposure.Add(orderValue)

	if newExposure.GreaterThan(maxExposure) {
		return fmt.Errorf("total exposure exceeds limit: %s > %s (%d%% of portfolio)",
			newExposure, maxExposure, int(m.cfg.MaxTotalExposure*100))
	}

	return nil
}

func (m *Manager) checkSlippage(order *models.Order) error {
	// For limit orders, slippage is controlled by the limit price itself.
	// This check is mainly for market orders or when we estimate fill price.
	if order.OrderType == models.OrderTypeLimit {
		return nil
	}

	// Market orders must have a slippage guard
	slog.Warn("market order detected — slippage tolerance will be applied at execution", "market", order.MarketID)
	return nil
}

// CalculatePriceImpact estimates the price impact of an order against the order book.
func CalculatePriceImpact(orderQty float64, orderSide models.OrderSide, bids, asks []struct{ Price, Qty float64 }) (float64, float64) {
	var book []struct{ Price, Qty float64 }
	if orderSide == models.OrderSideBuy {
		book = asks
	} else {
		book = bids
	}

	if len(book) == 0 {
		return 0, 0
	}

	midPrice := (bids[0].Price + asks[0].Price) / 2
	remaining := orderQty
	totalCost := 0.0

	for _, level := range book {
		fill := level.Qty
		if fill > remaining {
			fill = remaining
		}
		totalCost += fill * level.Price
		remaining -= fill
		if remaining <= 0 {
			break
		}
	}

	if orderQty <= 0 {
		return 0, midPrice
	}

	avgFillPrice := totalCost / (orderQty - remaining)
	priceImpact := (avgFillPrice - midPrice) / midPrice
	if orderSide == models.OrderSideSell {
		priceImpact = -priceImpact
	}

	return priceImpact, avgFillPrice
}
