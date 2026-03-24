package scalper

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Position represents an open scalper position.
type Position struct {
	OrderID    string
	TokenID    string
	Side       string // "UP" or "DOWN"
	EntryPrice float64
	Shares     float64
	USDCSpent  float64
	EntryTime  time.Time
	TPOrderID  string
}

// ExitManager monitors open positions and triggers exits.
type ExitManager struct {
	mu        sync.Mutex
	positions []*Position
	executor  *CLOBExecutor
	cfg       *Config
}

// NewExitManager creates a new ExitManager.
func NewExitManager(cfg *Config, executor *CLOBExecutor) *ExitManager {
	return &ExitManager{
		executor: executor,
		cfg:      cfg,
	}
}

// AddPosition registers a new open position to monitor.
func (m *ExitManager) AddPosition(p *Position) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.positions = append(m.positions, p)
	slog.Info("position added",
		"tokenID", p.TokenID,
		"side", p.Side,
		"entryPrice", p.EntryPrice,
		"shares", p.Shares,
	)
}

// HasOpenPosition returns true if there are any open positions.
func (m *ExitManager) HasOpenPosition() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.positions) > 0
}

// CheckExits monitors open positions and exits if TP/SL/expiry conditions are met.
func (m *ExitManager) CheckExits(ctx context.Context, snapshots map[string]OrderBookSnapshot, marketEndTime time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	remaining := m.positions[:0]

	// Force-close everything if market is near expiry
	if time.Until(marketEndTime) < 60*time.Second {
		slog.Info("market nearing expiry — force closing all positions")
		for _, pos := range m.positions {
			m.forceClose(ctx, pos, snapshots)
		}
		m.positions = nil
		return
	}

	for _, pos := range m.positions {
		snap, ok := snapshots[pos.TokenID]
		if !ok {
			remaining = append(remaining, pos)
			continue
		}

		currentPrice := snap.BestBid

		// Check stop-loss
		slPrice := pos.EntryPrice * (1 - m.cfg.StopLoss)
		if currentPrice <= slPrice {
			slog.Warn("stop-loss triggered",
				"tokenID", pos.TokenID,
				"entryPrice", pos.EntryPrice,
				"currentPrice", currentPrice,
				"slPrice", slPrice,
			)
			// Cancel TP order first
			if pos.TPOrderID != "" {
				if err := m.executor.CancelOrder(ctx, pos.TPOrderID); err != nil {
					slog.Warn("cancel TP order failed", "orderID", pos.TPOrderID, "error", err)
				}
			}
			m.forceClose(ctx, pos, snapshots)
			continue
		}

		// Check if TP order is filled
		if pos.TPOrderID != "" {
			status, err := m.executor.GetOrderStatus(ctx, pos.TPOrderID)
			if err != nil {
				slog.Warn("get TP order status failed", "orderID", pos.TPOrderID, "error", err)
				remaining = append(remaining, pos)
				continue
			}
			if status == "MATCHED" || status == "FILLED" {
				slog.Info("take-profit filled",
					"tokenID", pos.TokenID,
					"tpOrderID", pos.TPOrderID,
					"status", status,
				)
				// Position closed — don't add to remaining
				continue
			}
		}

		remaining = append(remaining, pos)
	}

	m.positions = remaining
}

// CloseAll force-closes all open positions immediately.
func (m *ExitManager) CloseAll(ctx context.Context, snapshots map[string]OrderBookSnapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, pos := range m.positions {
		m.forceClose(ctx, pos, snapshots)
	}
	m.positions = nil
}

// forceClose sells a position at market price (must be called with lock held or unlocked — caller manages lock).
func (m *ExitManager) forceClose(ctx context.Context, pos *Position, snapshots map[string]OrderBookSnapshot) {
	snap, ok := snapshots[pos.TokenID]
	if !ok {
		slog.Warn("no snapshot for force close", "tokenID", pos.TokenID)
		return
	}

	// Place a market sell at current BestBid
	// We use PlaceMarketBuy with side=SELL effectively — but our executor doesn't have a MarketSell.
	// Use a very low limit sell to ensure fill (at BestBid price).
	if snap.BestBid <= 0 {
		slog.Warn("best bid is zero, cannot force close", "tokenID", pos.TokenID)
		return
	}

	_, err := m.executor.PlaceLimitSell(ctx, pos.TokenID, pos.Shares, snap.BestBid)
	if err != nil {
		slog.Error("force close failed",
			"tokenID", pos.TokenID,
			"shares", pos.Shares,
			"price", snap.BestBid,
			"error", err,
		)
		return
	}

	slog.Info("force close executed",
		"tokenID", pos.TokenID,
		"shares", pos.Shares,
		"price", snap.BestBid,
	)
}
