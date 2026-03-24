package scalper

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

const (
	cycleInterval   = 2 * time.Second
	marketWaitSleep = 30 * time.Second

	// Setelah entry berhasil, jangan entry lagi di window yang sama
	// (boleh entry di window berikutnya meski ada posisi open dari window lama)
	entryCooldownPerWindow = true
)

// Engine is the main scalper engine that orchestrates market finding,
// order book subscription, momentum analysis, entry, and exit management.
type Engine struct {
	cfg      *Config
	finder   *MarketFinder
	book     *OrderBook
	executor *CLOBExecutor
	exits    *ExitManager

	// enteredThisWindow = true setelah 1 order berhasil placed di window aktif.
	// Di-reset setiap kali runCycle() dipanggil (= window baru).
	// Posisi dari window lama tetap dimonitor ExitManager — boleh entry window berikutnya.
	enteredThisWindow bool
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
	// Reset entry flag — setiap window baru boleh entry 1x
	e.enteredThisWindow = false

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

		// c. Log imbalance ratio setiap cycle (untuk monitoring)
		signal := Analyze(snapUp, snapDown, e.cfg.MomentumThreshold)
		slog.Debug("order book imbalance",
			"side", signal.Side,
			"imbalanceRatio", fmt.Sprintf("%.3f", signal.ImbalanceRatio),
			"upBid", fmt.Sprintf("%.2f", snapUp.BidDepth),
			"downBid", fmt.Sprintf("%.2f", snapDown.BidDepth),
			"remaining", remaining.Round(time.Second),
		)

		// e. Entry: max 1x per window. Posisi open dari window lama tidak menghalangi.
		if signal.Side != "NONE" && !e.enteredThisWindow && remaining > 60*time.Second {
			e.tryEntryWithSignal(ctx, market, signal)
		}

		// f. When market < 60s from end: force close and exit cycle
		if remaining < 60*time.Second {
			slog.Info("market nearing end — closing all positions", "remaining", remaining)
			e.exits.CloseAll(ctx, snapshots)

			// Sleep until window fully closes
			sleepDur := time.Until(market.EndTime.Add(2 * time.Second))
			if sleepDur > 0 {
				slog.Info("sleeping until market closes", "sleepDur", sleepDur.Round(time.Second))
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(sleepDur):
				}
			}

			slog.Info("market closed — starting new cycle")
			return nil // runCycle loop akan fetch market window baru
		}
	}
}

// tryEntryWithSignal places an entry order using a pre-computed signal.
func (e *Engine) tryEntryWithSignal(ctx context.Context, market *ActiveMarket, signal Signal) {
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

	// Place market buy (price diperlukan untuk hitung takerAmount)
	result, err := e.executor.PlaceMarketBuy(ctx, tokenID, tradeSize, signal.Price)
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

	// Lock window — tidak boleh entry lagi di window ini
	e.enteredThisWindow = true

	// Estimate shares from filled amount / entry price
	shares := tradeSize / signal.Price
	if result.FilledAmt > 0 {
		shares = result.FilledAmt
	}

	// Take-profit: gunakan midpoint antara TakeProfitMin dan TakeProfitMax
	// Default config: min=2%, max=5% → target 20-30% gain dari entry
	// Harga prediction market 0–1, jadi TP harga = entry + (1-entry)*targetPct
	// Contoh: entry 0.60, target +30% → TP = 0.60 + (1-0.60)*0.30 = 0.72
	tpTargetPct := (e.cfg.TakeProfitMin + e.cfg.TakeProfitMax) / 2
	tpPrice := signal.Price + (1-signal.Price)*tpTargetPct
	if tpPrice > 0.98 {
		tpPrice = 0.98 // max cap — 0.99+ sulit terfill
	}

	slog.Info("placing TP order",
		"entryPrice", signal.Price,
		"tpPrice", tpPrice,
		"tpPct", fmt.Sprintf("+%.0f%%", tpTargetPct*100),
		"shares", shares,
	)

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
