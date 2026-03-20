package resolution

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/x10rst/ai-agent-autonom/internal/config"
	"github.com/x10rst/ai-agent-autonom/internal/models"
	"github.com/x10rst/ai-agent-autonom/internal/notification"
)

// MarketResolver is the interface for checking market resolution status and claiming winnings.
type MarketResolver interface {
	// CheckResolution returns the resolved outcome for a market, or empty string if not resolved.
	CheckResolution(ctx context.Context, externalID string) (resolvedOutcome string, err error)

	// ClaimWinnings sends the on-chain claim transaction for a resolved market.
	ClaimWinnings(ctx context.Context, externalID string) (txHash string, err error)
}

// Handler automatically checks for resolved markets and claims winnings.
type Handler struct {
	cfg      config.ResolutionConfig
	db       *pgxpool.Pool
	resolver MarketResolver
	notifier *notification.Notifier
}

func NewHandler(cfg config.ResolutionConfig, db *pgxpool.Pool, resolver MarketResolver, notifier *notification.Notifier) *Handler {
	return &Handler{cfg: cfg, db: db, resolver: resolver, notifier: notifier}
}

// Run starts the market resolution check loop.
func (h *Handler) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(h.cfg.CheckIntervalSeconds) * time.Second)
	defer ticker.Stop()

	slog.Info("resolution handler started", "interval_seconds", h.cfg.CheckIntervalSeconds)

	for {
		select {
		case <-ctx.Done():
			slog.Info("resolution handler stopped")
			return
		case <-ticker.C:
			if err := h.checkResolutions(ctx); err != nil {
				slog.Error("resolution check failed", "error", err)
			}
		}
	}
}

func (h *Handler) checkResolutions(ctx context.Context) error {
	if h.resolver == nil {
		slog.Debug("resolution handler: no resolver configured, skipping check")
		return nil
	}

	// Get all markets with open positions
	rows, err := h.db.Query(ctx, `
		SELECT DISTINCT m.id, m.external_id
		FROM markets m
		INNER JOIN positions p ON p.market_id = m.id
		WHERE p.status = 'open' AND m.status = 'active'
	`)
	if err != nil {
		return fmt.Errorf("query markets with open positions: %w", err)
	}
	defer rows.Close()

	type marketInfo struct {
		ID         uuid.UUID
		ExternalID string
	}

	var markets []marketInfo
	for rows.Next() {
		var m marketInfo
		if err := rows.Scan(&m.ID, &m.ExternalID); err != nil {
			return fmt.Errorf("scan market: %w", err)
		}
		markets = append(markets, m)
	}

	for _, m := range markets {
		resolvedOutcome, err := h.resolver.CheckResolution(ctx, m.ExternalID)
		if err != nil {
			slog.Error("check resolution failed", "market", m.ExternalID, "error", err)
			continue
		}

		if resolvedOutcome == "" {
			continue // not resolved yet
		}

		slog.Info("market resolved", "market", m.ExternalID, "outcome", resolvedOutcome)

		// Update market status
		if _, err := h.db.Exec(ctx,
			"UPDATE markets SET status = 'resolved', updated_at = NOW() WHERE id = $1",
			m.ID,
		); err != nil {
			slog.Error("update market status", "error", err)
		}

		// Process positions
		if err := h.processResolvedPositions(ctx, m.ID, m.ExternalID, resolvedOutcome); err != nil {
			slog.Error("process resolved positions", "market", m.ExternalID, "error", err)
		}
	}

	return nil
}

func (h *Handler) processResolvedPositions(ctx context.Context, marketID uuid.UUID, externalID, resolvedOutcome string) error {
	rows, err := h.db.Query(ctx, `
		SELECT id, outcome, entry_price, quantity
		FROM positions
		WHERE market_id = $1 AND status = 'open'
	`, marketID)
	if err != nil {
		return fmt.Errorf("query positions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var posID uuid.UUID
		var outcome string
		var entryPrice, quantity decimal.Decimal
		if err := rows.Scan(&posID, &outcome, &entryPrice, &quantity); err != nil {
			return fmt.Errorf("scan position: %w", err)
		}

		var pnl decimal.Decimal
		won := outcome == resolvedOutcome

		if won {
			// Winning position: payout is quantity * 1.0 (full value)
			pnl = quantity.Sub(entryPrice.Mul(quantity))

			// Claim winnings on-chain
			if err := h.claimWithRetry(ctx, externalID); err != nil {
				slog.Error("claim winnings failed", "market", externalID, "error", err)
				h.notifier.Send(ctx, models.AlertHigh, "claim_failed",
					fmt.Sprintf("Failed to claim winnings for market %s: %s", externalID, err))
				continue
			}

			slog.Info("winnings claimed", "market", externalID, "position", posID, "pnl", pnl)
		} else {
			// Losing position: total loss
			pnl = entryPrice.Mul(quantity).Neg()
			slog.Info("position lost", "market", externalID, "position", posID, "pnl", pnl)
		}

		// Close position
		now := time.Now()
		if _, err := h.db.Exec(ctx, `
			UPDATE positions SET status = 'closed', realized_pnl = $1, closed_at = $2 WHERE id = $3
		`, pnl, now, posID); err != nil {
			slog.Error("close position", "position", posID, "error", err)
		}
	}

	return nil
}

func (h *Handler) claimWithRetry(ctx context.Context, externalID string) error {
	var lastErr error
	for i := 0; i < h.cfg.ClaimMaxRetries; i++ {
		txHash, err := h.resolver.ClaimWinnings(ctx, externalID)
		if err == nil {
			slog.Info("claim transaction sent", "market", externalID, "tx_hash", txHash)
			return nil
		}
		lastErr = err
		slog.Warn("claim attempt failed, retrying",
			"market", externalID,
			"attempt", i+1,
			"max_retries", h.cfg.ClaimMaxRetries,
			"error", err,
		)

		// Exponential backoff
		backoff := time.Duration(1<<uint(i)) * time.Second
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
	return fmt.Errorf("claim failed after %d retries: %w", h.cfg.ClaimMaxRetries, lastErr)
}
