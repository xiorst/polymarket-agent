package trading

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/x10rst/ai-agent-autonom/internal/config"
	"github.com/x10rst/ai-agent-autonom/internal/market"
	"github.com/x10rst/ai-agent-autonom/internal/ml"
	"github.com/x10rst/ai-agent-autonom/internal/models"
	"github.com/x10rst/ai-agent-autonom/internal/notification"
	"github.com/x10rst/ai-agent-autonom/internal/reliability"
	"github.com/x10rst/ai-agent-autonom/internal/risk"
)

// Executor is the interface for order execution backends (live on-chain, paper simulation).
type Executor interface {
	PlaceOrder(ctx context.Context, order *models.Order) (txHash string, err error)
	CancelOrder(ctx context.Context, orderID uuid.UUID) error
}

// Engine is the core trading execution engine.
type Engine struct {
	cfg            config.TradingConfig
	riskCfg        config.RiskConfig
	db             *pgxpool.Pool
	executor       Executor
	riskMgr        *risk.Manager
	circuitBreaker *reliability.CircuitBreaker
	liqMonitor     *market.LiquidityMonitor
	notifier       *notification.Notifier
	mlPipeline     *ml.Pipeline
}

func NewEngine(
	cfg config.TradingConfig,
	riskCfg config.RiskConfig,
	db *pgxpool.Pool,
	executor Executor,
	riskMgr *risk.Manager,
	cb *reliability.CircuitBreaker,
	liqMonitor *market.LiquidityMonitor,
	notifier *notification.Notifier,
	mlPipeline *ml.Pipeline,
) *Engine {
	return &Engine{
		cfg:            cfg,
		riskCfg:        riskCfg,
		db:             db,
		executor:       executor,
		riskMgr:        riskMgr,
		circuitBreaker: cb,
		liqMonitor:     liqMonitor,
		notifier:       notifier,
		mlPipeline:     mlPipeline,
	}
}

// RunCycle executes one full trading cycle: generate signals → validate → execute.
func (e *Engine) RunCycle(ctx context.Context) error {
	// Check circuit breaker
	if !e.circuitBreaker.AllowTrading() {
		slog.Info("trading cycle skipped: circuit breaker is open")
		return nil
	}

	// Check daily loss limit
	if e.riskMgr.IsDailyLimitHit() {
		slog.Info("trading cycle skipped: daily loss limit hit")
		return nil
	}

	// 1. Generate ML signals
	signals, err := e.mlPipeline.GenerateSignals(ctx)
	if err != nil {
		return fmt.Errorf("generate signals: %w", err)
	}

	if len(signals) == 0 {
		slog.Debug("no trading signals in this cycle")
		return nil
	}

	slog.Info("trading signals generated", "count", len(signals))

	// 2. Get current portfolio balance
	portfolioBalance, err := e.getPortfolioBalance(ctx)
	if err != nil {
		return fmt.Errorf("get portfolio balance: %w", err)
	}

	// 3. Process each signal
	for _, signal := range signals {
		if err := e.processSignal(ctx, signal, portfolioBalance); err != nil {
			slog.Error("failed to process signal",
				"market", signal.MarketID,
				"outcome", signal.PredictedOutcome,
				"error", err,
			)
			e.circuitBreaker.RecordFailure(ctx, err.Error())
			continue
		}
		e.circuitBreaker.RecordSuccess()
	}

	// 4. Check stop-losses on open positions
	if err := e.checkStopLosses(ctx); err != nil {
		slog.Error("stop-loss check failed", "error", err)
	}

	return nil
}

func (e *Engine) processSignal(ctx context.Context, signal models.Signal, portfolioBalance decimal.Decimal) error {
	// Check liquidity
	if e.liqMonitor.IsMarketHalted(signal.MarketID.String()) {
		return fmt.Errorf("market %s is halted due to low liquidity", signal.MarketID)
	}

	// Determine order side: if predicted probability > market price, buy; otherwise sell
	var side models.OrderSide
	if signal.Confidence.GreaterThan(signal.MarketPrice) {
		side = models.OrderSideBuy
	} else {
		side = models.OrderSideSell
	}

	// Calculate position size (% of portfolio)
	maxPositionValue := portfolioBalance.Mul(decimal.NewFromFloat(e.riskCfg.MaxPositionPerMarket))
	quantity := maxPositionValue.Div(signal.MarketPrice)

	// Generate idempotency key
	idempotencyKey := ml.GenerateIdempotencyKey(signal.MarketID, signal.PredictedOutcome, signal.CreatedAt)

	// Check idempotency
	var exists bool
	err := e.db.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM orders WHERE idempotency_key = $1)",
		idempotencyKey,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check idempotency: %w", err)
	}
	if exists {
		slog.Debug("order already exists for this signal, skipping", "key", idempotencyKey)
		return nil
	}

	order := &models.Order{
		ID:             uuid.New(),
		MarketID:       signal.MarketID,
		Side:           side,
		OrderType:      models.OrderTypeLimit,
		Outcome:        signal.PredictedOutcome,
		Price:          signal.MarketPrice,
		Quantity:       quantity,
		Status:         models.OrderStatusPending,
		IdempotencyKey: idempotencyKey,
		IsPaper:        e.cfg.Mode == "paper",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	// Pre-trade risk check
	if err := e.riskMgr.PreTradeCheck(ctx, order, portfolioBalance); err != nil {
		return fmt.Errorf("risk check failed: %w", err)
	}

	// Check if order needs splitting
	orderValue := order.Price.Mul(order.Quantity)
	if e.riskMgr.ShouldSplit(orderValue) {
		return e.executeSplitOrder(ctx, order)
	}

	return e.executeOrder(ctx, order)
}

func (e *Engine) executeOrder(ctx context.Context, order *models.Order) error {
	// Pre-fill defaults so DB insert is always valid
	order.FillPrice = order.Price
	order.FilledQuantity = order.Quantity

	// Store order in DB (status = pending)
	if err := e.storeOrder(ctx, order); err != nil {
		return fmt.Errorf("store order: %w", err)
	}

	// Execute — executor may mutate FillPrice, FilledQuantity, FeeCost, Status (partial)
	txHash, err := e.executor.PlaceOrder(ctx, order)
	if err != nil {
		order.Status = models.OrderStatusFailed
		e.updateOrderStatus(ctx, order)
		return fmt.Errorf("place order: %w", err)
	}

	order.TxHash = &txHash
	// Only override status to Filled if executor didn't set PartiallyFilled
	if order.Status != models.OrderStatusPartiallyFilled {
		order.Status = models.OrderStatusFilled
	}
	order.UpdatedAt = time.Now()
	e.updateOrderStatus(ctx, order)

	// Create/update position using actual fill data
	if err := e.upsertPosition(ctx, order); err != nil {
		slog.Error("failed to upsert position after order fill", "error", err)
	}

	slog.Info("order executed",
		"id", order.ID,
		"market", order.MarketID,
		"side", order.Side,
		"outcome", order.Outcome,
		"requested_price", order.Price,
		"fill_price", order.FillPrice,
		"requested_qty", order.Quantity,
		"filled_qty", order.FilledQuantity,
		"fee_cost", order.FeeCost,
		"status", order.Status,
		"tx_hash", txHash,
		"paper", order.IsPaper,
	)

	return nil
}

func (e *Engine) executeSplitOrder(ctx context.Context, order *models.Order) error {
	chunks := e.riskMgr.SplitOrder(order)
	slog.Info("splitting order into chunks",
		"market", order.MarketID,
		"chunks", len(chunks),
		"total_quantity", order.Quantity,
	)

	for i, chunk := range chunks {
		chunk.ID = uuid.New()
		chunk.IdempotencyKey = fmt.Sprintf("%s_chunk_%d", order.IdempotencyKey, i)

		if err := e.executeOrder(ctx, &chunk); err != nil {
			slog.Error("chunk execution failed",
				"chunk", i+1,
				"total", len(chunks),
				"error", err,
			)
			return err
		}

		// Delay between chunks
		if i < len(chunks)-1 {
			time.Sleep(time.Duration(e.riskCfg.OrderSplitDelayMs) * time.Millisecond)
		}
	}

	return nil
}

func (e *Engine) checkStopLosses(ctx context.Context) error {
	rows, err := e.db.Query(ctx, `
		SELECT id, market_id, outcome, entry_price, quantity, status, realized_pnl, created_at, closed_at
		FROM positions WHERE status = 'open'
	`)
	if err != nil {
		return fmt.Errorf("query open positions: %w", err)
	}
	defer rows.Close()

	var positions []models.Position
	for rows.Next() {
		var p models.Position
		if err := rows.Scan(&p.ID, &p.MarketID, &p.Outcome, &p.EntryPrice, &p.Quantity, &p.Status, &p.RealizedPnL, &p.CreatedAt, &p.ClosedAt); err != nil {
			return fmt.Errorf("scan position: %w", err)
		}
		positions = append(positions, p)
	}

	// TODO: fetch current prices from market provider
	// For now, this is a placeholder that would be wired to the market data provider
	currentPrices := make(map[string]decimal.Decimal)

	triggered := e.riskMgr.CheckStopLoss(ctx, positions, currentPrices)
	for _, pos := range triggered {
		// Create sell order to close position
		closeOrder := &models.Order{
			ID:             uuid.New(),
			MarketID:       pos.MarketID,
			Side:           models.OrderSideSell,
			OrderType:      models.OrderTypeMarket,
			Outcome:        pos.Outcome,
			Price:          pos.EntryPrice, // will be filled at market
			Quantity:       pos.Quantity,
			Status:         models.OrderStatusPending,
			IdempotencyKey: fmt.Sprintf("stoploss_%s_%d", pos.ID, time.Now().Unix()),
			IsPaper:        e.cfg.Mode == "paper",
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		}

		if err := e.executeOrder(ctx, closeOrder); err != nil {
			slog.Error("failed to execute stop-loss order", "position", pos.ID, "error", err)
			e.notifier.Send(ctx, models.AlertHigh, "stop_loss_failed",
				fmt.Sprintf("Stop-loss execution failed for position %s: %s", pos.ID, err))
		}
	}

	return nil
}

func (e *Engine) getPortfolioBalance(ctx context.Context) (decimal.Decimal, error) {
	if e.cfg.Mode == "paper" {
		initial := decimal.NewFromFloat(e.cfg.InitialBalance)

		// realized PnL from closed positions
		var totalPnL decimal.Decimal
		if err := e.db.QueryRow(ctx,
			"SELECT COALESCE(SUM(realized_pnl), 0) FROM positions WHERE status = 'closed'",
		).Scan(&totalPnL); err != nil {
			return decimal.Zero, err
		}

		// total fee costs paid on all paper orders (simulated gas)
		var totalFees decimal.Decimal
		if err := e.db.QueryRow(ctx,
			"SELECT COALESCE(SUM(fee_cost), 0) FROM orders WHERE is_paper = true AND status IN ('filled','partially_filled')",
		).Scan(&totalFees); err != nil {
			return decimal.Zero, err
		}

		return initial.Add(totalPnL).Sub(totalFees), nil
	}

	// In live mode, query on-chain USDC balance
	// Placeholder: should be wired to blockchain.Client.GetUSDCBalance
	return decimal.NewFromFloat(e.cfg.InitialBalance), nil
}

func (e *Engine) storeOrder(ctx context.Context, order *models.Order) error {
	_, err := e.db.Exec(ctx, `
		INSERT INTO orders (
			id, market_id, side, order_type, outcome,
			price, quantity, fill_price, filled_quantity, fee_cost,
			status, idempotency_key, is_paper, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
	`,
		order.ID, order.MarketID, order.Side, order.OrderType, order.Outcome,
		order.Price, order.Quantity, order.FillPrice, order.FilledQuantity, order.FeeCost,
		order.Status, order.IdempotencyKey, order.IsPaper, order.CreatedAt, order.UpdatedAt,
	)
	return err
}

func (e *Engine) updateOrderStatus(ctx context.Context, order *models.Order) {
	_, err := e.db.Exec(ctx, `
		UPDATE orders
		SET status = $1, tx_hash = $2, fill_price = $3, filled_quantity = $4, fee_cost = $5, updated_at = $6
		WHERE id = $7
	`, order.Status, order.TxHash, order.FillPrice, order.FilledQuantity, order.FeeCost, time.Now(), order.ID)
	if err != nil {
		slog.Error("failed to update order status", "order", order.ID, "error", err)
	}
}

func (e *Engine) upsertPosition(ctx context.Context, order *models.Order) error {
	if order.Side == models.OrderSideBuy {
		_, err := e.db.Exec(ctx, `
			INSERT INTO positions (id, market_id, outcome, entry_price, quantity, status, created_at)
			VALUES ($1, $2, $3, $4, $5, 'open', $6)
		`, uuid.New(), order.MarketID, order.Outcome, order.FillPrice, order.FilledQuantity, time.Now())
		return err
	}

	// For sell orders, close existing position using actual fill data.
	// realized_pnl = (fill_price - entry_price) * filled_quantity - fee_cost
	now := time.Now()
	_, err := e.db.Exec(ctx, `
		UPDATE positions SET status = 'closed', closed_at = $1,
			realized_pnl = ($2 - entry_price) * $3 - $4
		WHERE market_id = $5 AND outcome = $6 AND status = 'open'
	`, now, order.FillPrice, order.FilledQuantity, order.FeeCost, order.MarketID, order.Outcome)
	return err
}
