package scalper

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

const (
	cycleInterval    = 2 * time.Second
	marketWaitSleep  = 30 * time.Second
)

// Engine is the main scalper engine that orchestrates market finding,
// order book subscription, momentum analysis, entry, and exit management.
type Engine struct {
	cfg      *Config
	finder   *MarketFinder
	book     *OrderBook
	executor *CLOBExecutor
	exits    *ExitManager
}

// NewEngine creates a new scalper Engine.
func NewEngine(cfg *Config) *Engine {
	executor := NewCLOBExecutor(cfg)
	return &Engine{
		cfg:      cfg,
		finder:   NewMarketFinder(cfg),
		book:     NewOrderBook(cfg),
		executor: executor,
		exits:    NewExitManager(cfg, executor),
	}
}

// Run is the main loop for the scalper engine. It blocks until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	slog.Info("scalper engine starting", "series", e.cfg.SeriesSlug)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			if err := e.runCycle(ctx); err != nil {
				slog.Error("scalper cycle error", "error", err)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(5 * time.Second):
				}
			}
		}
	}
}

// runCycle handles one full market lifecycle: find → subscribe → trade → exit → repeat.
func (e *Engine) runCycle(ctx context.Context) error {
	// 1. Find active market
	market, err := e.finder.FindActive(ctx)
	if err != nil {
		slog.Warn("no active market found, retrying", "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(marketWaitSleep):
		}
		return nil
	}

	slog.Info("active market found",
		"conditionID", market.ConditionID,
		"endTime", market.EndTime,
		"tokenUp", market.TokenIDUp,
		"tokenDown", market.TokenIDDown,
	)

	// 2. Subscribe order book for Up and Down tokens
	if err := e.book.Subscribe(ctx, market.TokenIDUp, market.TokenIDDown); err != nil {
		return fmt.Errorf("subscribe orderbook: %w", err)
	}

	// Give WS a moment to receive first snapshot
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
	}

	// 3. Trading loop until market expires or ctx cancelled
	ticker := time.NewTicker(cycleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		// Check if market has expired
		remaining := time.Until(market.EndTime)
		if remaining <= 0 {
			slog.Info("market expired, finding next market")
			return nil
		}

		// a. Get snapshots
		snapUp, okUp := e.book.GetSnapshot(market.TokenIDUp)
		snapDown, okDown := e.book.GetSnapshot(market.TokenIDDown)
		if !okUp || !okDown {
			slog.Debug("waiting for order book snapshots...")
			continue
		}

		snapshots := map[string]OrderBookSnapshot{
			market.TokenIDUp:   snapUp,
			market.TokenIDDown: snapDown,
		}

		// b. Check exits
		e.exits.CheckExits(ctx, snapshots, market.EndTime)

		// c. Entry: only if no open position and capital available
		if !e.exits.HasOpenPosition() && remaining > 60*time.Second {
			e.tryEntry(ctx, market, snapUp, snapDown)
		}

		// d. When market < 60s from end: force close and wait for next window
		if remaining < 60*time.Second {
			slog.Info("market nearing end — closing all positions", "remaining", remaining)
			e.exits.CloseAll(ctx, snapshots)

			// Sleep until market is fully past, then wait for next window to open
			sleepUntil := market.EndTime.Add(3 * time.Second)
			sleepDur := time.Until(sleepUntil)
			if sleepDur > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(sleepDur):
				}
			}

			// Wait for next window to open (poll every 2s until acceptingOrders=true)
			slog.Info("waiting for next 5m window to open...")
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(2 * time.Second):
				}
				next, err := e.finder.FindActive(ctx)
				if err == nil && next != nil && next.ConditionID != market.ConditionID {
					slog.Info("new market window found", "slug", next.Slug, "endTime", next.EndTime)
					return nil // triggers new runCycle with new market
				}
			}
		}
	}
}

// tryEntry analyzes momentum and places an entry order if signal is strong.
func (e *Engine) tryEntry(ctx context.Context, market *ActiveMarket, snapUp, snapDown OrderBookSnapshot) {
	signal := Analyze(snapUp, snapDown, e.cfg.MomentumThreshold)
	if signal.Side == "NONE" || signal.Price <= 0 {
		return
	}

	// Determine which token to buy
	var tokenID string
	switch signal.Side {
	case "UP":
		tokenID = market.TokenIDUp
	case "DOWN":
		tokenID = market.TokenIDDown
	default:
		return
	}

	// Capital check
	balance, err := e.executor.GetBalance(ctx)
	if err != nil {
		slog.Warn("failed to get balance for entry check", "error", err)
		return
	}

	tradeSize := e.cfg.TradeSize
	if balance < tradeSize {
		slog.Warn("insufficient balance for trade", "balance", balance, "tradeSize", tradeSize)
		return
	}

	slog.Info("momentum signal detected, placing entry",
		"side", signal.Side,
		"confidence", signal.Confidence,
		"price", signal.Price,
		"tokenID", tokenID,
		"tradeSize", tradeSize,
	)

	// Place market buy
	result, err := e.executor.PlaceMarketBuy(ctx, tokenID, tradeSize)
	if err != nil {
		slog.Error("failed to place entry order", "error", err)
		return
	}

	slog.Info("entry order placed",
		"orderID", result.OrderID,
		"status", result.Status,
		"filledAmt", result.FilledAmt,
	)

	if result.Status == "" || result.OrderID == "" {
		slog.Warn("entry order may not have filled", "result", result)
		return
	}

	// Estimate shares from filled amount / entry price
	shares := tradeSize / signal.Price
	if result.FilledAmt > 0 {
		shares = result.FilledAmt
	}

	// Place take-profit limit sell
	tpPrice := signal.Price * (1 + e.cfg.TakeProfitMin)
	tpResult, err := e.executor.PlaceLimitSell(ctx, tokenID, shares, tpPrice)
	if err != nil {
		slog.Error("failed to place TP order", "error", err)
	}

	tpOrderID := ""
	if tpResult != nil {
		tpOrderID = tpResult.OrderID
	}

	// Register position
	e.exits.AddPosition(&Position{
		OrderID:    result.OrderID,
		TokenID:    tokenID,
		Side:       signal.Side,
		EntryPrice: signal.Price,
		Shares:     shares,
		USDCSpent:  tradeSize,
		EntryTime:  time.Now(),
		TPOrderID:  tpOrderID,
	})
}
