package reliability

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

type CircuitBreaker struct {
	cfg      config.CircuitBreakerConfig
	notifier *notification.Notifier

	mu                  sync.RWMutex
	state               models.CircuitBreakerState
	consecutiveFailures int
	lastStateChange     time.Time
	balanceHistory      []balancePoint
}

type balancePoint struct {
	Balance float64
	Time    time.Time
}

func NewCircuitBreaker(cfg config.CircuitBreakerConfig, notifier *notification.Notifier) *CircuitBreaker {
	return &CircuitBreaker{
		cfg:             cfg,
		notifier:        notifier,
		state:           models.CBStateClosed,
		lastStateChange: time.Now(),
	}
}

// State returns the current circuit breaker state.
func (cb *CircuitBreaker) State() models.CircuitBreakerState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// AllowTrading returns true if trading is allowed (state is CLOSED or testing in HALF_OPEN).
func (cb *CircuitBreaker) AllowTrading() bool {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	switch cb.state {
	case models.CBStateClosed:
		return true
	case models.CBStateHalfOpen:
		return true // allow one test trade
	default:
		return false
	}
}

// RecordSuccess resets the failure counter and transitions HALF_OPEN → CLOSED.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.consecutiveFailures = 0

	if cb.state == models.CBStateHalfOpen {
		slog.Info("circuit breaker: half_open → closed (test trade succeeded)")
		cb.transition(models.CBStateClosed, "test_trade_success")
	}
}

// RecordFailure increments the failure counter and may trip the breaker.
func (cb *CircuitBreaker) RecordFailure(ctx context.Context, reason string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.consecutiveFailures++
	slog.Warn("circuit breaker: failure recorded",
		"consecutive", cb.consecutiveFailures,
		"reason", reason,
	)

	if cb.state == models.CBStateHalfOpen {
		slog.Warn("circuit breaker: half_open → open (test trade failed)")
		cb.transition(models.CBStateOpen, reason)
		cb.scheduleHalfOpen()
		return
	}

	if cb.consecutiveFailures >= cb.cfg.MaxConsecutiveFailures {
		slog.Error("circuit breaker: TRIPPED",
			"failures", cb.consecutiveFailures,
			"threshold", cb.cfg.MaxConsecutiveFailures,
		)
		cb.transition(models.CBStateOpen, reason)
		cb.scheduleHalfOpen()

		cb.notifier.Send(ctx, models.AlertCritical, "circuit_breaker_tripped",
			fmt.Sprintf("Circuit breaker tripped after %d consecutive failures. Reason: %s. Trading halted for %d seconds.",
				cb.consecutiveFailures, reason, cb.cfg.CooldownSeconds))
	}
}

// RecordBalanceDrop checks if a rapid balance drop exceeds the threshold.
func (cb *CircuitBreaker) RecordBalanceDrop(ctx context.Context, currentBalance float64) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-time.Duration(cb.cfg.RapidDropWindowSeconds) * time.Second)

	// Prune old entries
	filtered := cb.balanceHistory[:0]
	for _, bp := range cb.balanceHistory {
		if bp.Time.After(windowStart) {
			filtered = append(filtered, bp)
		}
	}
	cb.balanceHistory = append(filtered, balancePoint{Balance: currentBalance, Time: now})

	if len(cb.balanceHistory) < 2 {
		return
	}

	// Check drop from max balance in window
	maxBalance := cb.balanceHistory[0].Balance
	for _, bp := range cb.balanceHistory {
		if bp.Balance > maxBalance {
			maxBalance = bp.Balance
		}
	}

	if maxBalance <= 0 {
		return
	}

	dropPct := (maxBalance - currentBalance) / maxBalance
	if dropPct >= cb.cfg.RapidDropThreshold {
		slog.Error("circuit breaker: rapid balance drop detected",
			"max_balance", maxBalance,
			"current_balance", currentBalance,
			"drop_pct", dropPct,
		)
		cb.transition(models.CBStateOpen, "rapid_balance_drop")
		cb.scheduleHalfOpen()

		cb.notifier.Send(ctx, models.AlertCritical, "rapid_balance_drop",
			fmt.Sprintf("Balance dropped %.1f%% in %d seconds (from %.2f to %.2f). Trading halted.",
				dropPct*100, cb.cfg.RapidDropWindowSeconds, maxBalance, currentBalance))
	}
}

func (cb *CircuitBreaker) transition(newState models.CircuitBreakerState, reason string) {
	oldState := cb.state
	cb.state = newState
	cb.lastStateChange = time.Now()

	slog.Info("circuit breaker state transition",
		"from", oldState,
		"to", newState,
		"reason", reason,
	)
}

func (cb *CircuitBreaker) scheduleHalfOpen() {
	go func() {
		time.Sleep(time.Duration(cb.cfg.CooldownSeconds) * time.Second)
		cb.mu.Lock()
		defer cb.mu.Unlock()

		if cb.state == models.CBStateOpen {
			slog.Info("circuit breaker: open → half_open (cooldown expired)")
			cb.transition(models.CBStateHalfOpen, "cooldown_expired")
			cb.consecutiveFailures = 0
		}
	}()
}
