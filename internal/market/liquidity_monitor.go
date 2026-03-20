package market

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/x10rst/ai-agent-autonom/internal/config"
	"github.com/x10rst/ai-agent-autonom/internal/models"
	"github.com/x10rst/ai-agent-autonom/internal/notification"
)

// LiquidityMonitor tracks bid-ask spreads and halts trading on illiquid markets.
type LiquidityMonitor struct {
	cfg      config.LiquidityConfig
	provider Provider
	notifier *notification.Notifier

	mu            sync.RWMutex
	haltedMarkets map[string]time.Time         // external_id → halted since
	spreadHistory map[string][]spreadDataPoint // external_id → recent spreads
}

type spreadDataPoint struct {
	Spread float64
	Time   time.Time
}

func NewLiquidityMonitor(cfg config.LiquidityConfig, provider Provider, notifier *notification.Notifier) *LiquidityMonitor {
	return &LiquidityMonitor{
		cfg:           cfg,
		provider:      provider,
		notifier:      notifier,
		haltedMarkets: make(map[string]time.Time),
		spreadHistory: make(map[string][]spreadDataPoint),
	}
}

// IsMarketHalted returns true if trading is halted for the given market.
func (lm *LiquidityMonitor) IsMarketHalted(externalID string) bool {
	lm.mu.RLock()
	defer lm.mu.RUnlock()
	_, halted := lm.haltedMarkets[externalID]
	return halted
}

// GetNormalSpread returns the average spread for a market within the normalization window.
func (lm *LiquidityMonitor) GetNormalSpread(externalID string) float64 {
	lm.mu.RLock()
	defer lm.mu.RUnlock()

	history, ok := lm.spreadHistory[externalID]
	if !ok || len(history) == 0 {
		return 0
	}

	windowStart := time.Now().Add(-time.Duration(lm.cfg.SpreadNormalizationWindow) * time.Second)

	var sum float64
	var count int
	for _, dp := range history {
		if dp.Time.After(windowStart) {
			sum += dp.Spread
			count++
		}
	}

	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

// Run starts the liquidity monitoring loop.
func (lm *LiquidityMonitor) Run(ctx context.Context, getActiveMarkets func() []string) {
	ticker := time.NewTicker(time.Duration(lm.cfg.SpreadCheckInterval) * time.Second)
	defer ticker.Stop()

	slog.Info("liquidity monitor started", "interval_seconds", lm.cfg.SpreadCheckInterval)

	for {
		select {
		case <-ctx.Done():
			slog.Info("liquidity monitor stopped")
			return
		case <-ticker.C:
			marketIDs := getActiveMarkets()
			for _, id := range marketIDs {
				lm.checkSpread(ctx, id)
			}
		}
	}
}

func (lm *LiquidityMonitor) checkSpread(ctx context.Context, externalID string) {
	ob, err := lm.provider.FetchOrderBook(ctx, externalID)
	if err != nil {
		slog.Error("failed to fetch order book for spread check", "market", externalID, "error", err)
		return
	}

	currentSpread := ob.Spread
	normalSpread := lm.GetNormalSpread(externalID)

	// Record spread
	lm.mu.Lock()
	lm.spreadHistory[externalID] = append(lm.spreadHistory[externalID], spreadDataPoint{
		Spread: currentSpread,
		Time:   time.Now(),
	})
	// Prune old entries
	windowStart := time.Now().Add(-time.Duration(lm.cfg.SpreadNormalizationWindow) * time.Second)
	filtered := lm.spreadHistory[externalID][:0]
	for _, dp := range lm.spreadHistory[externalID] {
		if dp.Time.After(windowStart) {
			filtered = append(filtered, dp)
		}
	}
	lm.spreadHistory[externalID] = filtered

	if normalSpread <= 0 {
		lm.mu.Unlock()
		return
	}

	spreadRatio := currentSpread / normalSpread

	// Check HALT threshold
	if spreadRatio >= lm.cfg.SpreadHaltMultiplier {
		lm.haltedMarkets[externalID] = time.Now()
		lm.mu.Unlock()

		slog.Warn("market HALTED due to extreme spread",
			"market", externalID,
			"spread", currentSpread,
			"normal", normalSpread,
			"ratio", spreadRatio,
		)
		lm.notifier.Send(ctx, models.AlertMedium, "spread_halt",
			"Trading halted on market "+externalID+" — spread "+
				"is "+fmt.Sprintf("%.1fx", spreadRatio)+" above normal")
		return
	}

	// Check WARNING threshold
	if spreadRatio >= lm.cfg.SpreadWarningMultiplier {
		lm.mu.Unlock()
		slog.Warn("market spread WARNING",
			"market", externalID,
			"spread", currentSpread,
			"normal", normalSpread,
			"ratio", spreadRatio,
		)
		return
	}

	// Un-halt if spread normalized
	if _, halted := lm.haltedMarkets[externalID]; halted {
		delete(lm.haltedMarkets, externalID)
		slog.Info("market UN-HALTED, spread normalized", "market", externalID)
	}
	lm.mu.Unlock()
}

