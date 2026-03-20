package reliability

import (
	"context"
	"testing"
	"time"

	"github.com/x10rst/ai-agent-autonom/internal/config"
	"github.com/x10rst/ai-agent-autonom/internal/models"
	"github.com/x10rst/ai-agent-autonom/internal/notification"
)

func newTestCB() *CircuitBreaker {
	cfg := config.CircuitBreakerConfig{
		Enabled:                true,
		CooldownSeconds:        1, // 1 second for fast tests
		MaxConsecutiveFailures: 3,
		RapidDropThreshold:     0.10,
		RapidDropWindowSeconds: 300,
	}
	notifier := notification.New(config.NotificationConfig{})
	return NewCircuitBreaker(cfg, notifier)
}

// --- State Machine Tests ---

func TestCB_InitialStateClosed(t *testing.T) {
	cb := newTestCB()
	if cb.State() != models.CBStateClosed {
		t.Errorf("expected initial state CLOSED, got %s", cb.State())
	}
}

func TestCB_AllowTrading_WhenClosed(t *testing.T) {
	cb := newTestCB()
	if !cb.AllowTrading() {
		t.Error("expected AllowTrading = true when CLOSED")
	}
}

func TestCB_TripsAfterConsecutiveFailures(t *testing.T) {
	cb := newTestCB()
	ctx := context.Background()

	// Record 3 failures (threshold)
	cb.RecordFailure(ctx, "test error 1")
	cb.RecordFailure(ctx, "test error 2")

	// Still closed after 2
	if cb.State() != models.CBStateClosed {
		t.Errorf("expected CLOSED after 2 failures, got %s", cb.State())
	}

	cb.RecordFailure(ctx, "test error 3")

	// Should be OPEN now
	if cb.State() != models.CBStateOpen {
		t.Errorf("expected OPEN after 3 failures, got %s", cb.State())
	}

	if cb.AllowTrading() {
		t.Error("expected AllowTrading = false when OPEN")
	}
}

func TestCB_SuccessResetsCounter(t *testing.T) {
	cb := newTestCB()
	ctx := context.Background()

	cb.RecordFailure(ctx, "fail 1")
	cb.RecordFailure(ctx, "fail 2")
	cb.RecordSuccess() // reset

	// Should be back to 0 failures
	cb.RecordFailure(ctx, "fail after reset 1")
	cb.RecordFailure(ctx, "fail after reset 2")

	// Should still be CLOSED (only 2 after reset)
	if cb.State() != models.CBStateClosed {
		t.Errorf("expected CLOSED after reset + 2 failures, got %s", cb.State())
	}
}

func TestCB_TransitionToHalfOpen(t *testing.T) {
	cb := newTestCB()
	ctx := context.Background()

	// Trip the breaker
	cb.RecordFailure(ctx, "f1")
	cb.RecordFailure(ctx, "f2")
	cb.RecordFailure(ctx, "f3")

	if cb.State() != models.CBStateOpen {
		t.Fatalf("expected OPEN, got %s", cb.State())
	}

	// Wait for cooldown (1 second in test config)
	time.Sleep(1500 * time.Millisecond)

	if cb.State() != models.CBStateHalfOpen {
		t.Errorf("expected HALF_OPEN after cooldown, got %s", cb.State())
	}

	// Should allow one test trade
	if !cb.AllowTrading() {
		t.Error("expected AllowTrading = true when HALF_OPEN")
	}
}

func TestCB_HalfOpen_SuccessCloses(t *testing.T) {
	cb := newTestCB()
	ctx := context.Background()

	// Trip → wait cooldown → half_open
	cb.RecordFailure(ctx, "f1")
	cb.RecordFailure(ctx, "f2")
	cb.RecordFailure(ctx, "f3")
	time.Sleep(1500 * time.Millisecond)

	if cb.State() != models.CBStateHalfOpen {
		t.Fatalf("expected HALF_OPEN, got %s", cb.State())
	}

	// Success in half_open → closed
	cb.RecordSuccess()

	if cb.State() != models.CBStateClosed {
		t.Errorf("expected CLOSED after success in HALF_OPEN, got %s", cb.State())
	}
}

func TestCB_HalfOpen_FailureReopens(t *testing.T) {
	cb := newTestCB()
	ctx := context.Background()

	// Trip → wait cooldown → half_open
	cb.RecordFailure(ctx, "f1")
	cb.RecordFailure(ctx, "f2")
	cb.RecordFailure(ctx, "f3")
	time.Sleep(1500 * time.Millisecond)

	if cb.State() != models.CBStateHalfOpen {
		t.Fatalf("expected HALF_OPEN, got %s", cb.State())
	}

	// Failure in half_open → back to open
	cb.RecordFailure(ctx, "half_open_fail")

	if cb.State() != models.CBStateOpen {
		t.Errorf("expected OPEN after failure in HALF_OPEN, got %s", cb.State())
	}
}

// --- Rapid Balance Drop Tests ---

func TestCB_RapidBalanceDrop_Triggers(t *testing.T) {
	cb := newTestCB()
	ctx := context.Background()

	// Record a balance of 100, then drop to 85 (15% drop > 10% threshold)
	cb.RecordBalanceDrop(ctx, 100.0)
	cb.RecordBalanceDrop(ctx, 85.0)

	if cb.State() != models.CBStateOpen {
		t.Errorf("expected OPEN after 15%% drop, got %s", cb.State())
	}
}

func TestCB_RapidBalanceDrop_SmallDropIgnored(t *testing.T) {
	cb := newTestCB()
	ctx := context.Background()

	// Record a balance of 100, then drop to 95 (5% drop < 10% threshold)
	cb.RecordBalanceDrop(ctx, 100.0)
	cb.RecordBalanceDrop(ctx, 95.0)

	if cb.State() != models.CBStateClosed {
		t.Errorf("expected CLOSED after 5%% drop, got %s", cb.State())
	}
}

func TestCB_RapidBalanceDrop_IncreaseIgnored(t *testing.T) {
	cb := newTestCB()
	ctx := context.Background()

	cb.RecordBalanceDrop(ctx, 100.0)
	cb.RecordBalanceDrop(ctx, 120.0) // balance went UP

	if cb.State() != models.CBStateClosed {
		t.Errorf("expected CLOSED after balance increase, got %s", cb.State())
	}
}
