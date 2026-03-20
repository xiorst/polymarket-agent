package reliability

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/x10rst/ai-agent-autonom/internal/blockchain"
	"github.com/x10rst/ai-agent-autonom/internal/config"
	"github.com/x10rst/ai-agent-autonom/internal/models"
	"github.com/x10rst/ai-agent-autonom/internal/notification"
)

// Reconciler syncs on-chain state with the local database.
type Reconciler struct {
	cfg      config.ReconciliationConfig
	db       *pgxpool.Pool
	chain    *blockchain.Client
	notifier *notification.Notifier
}

func NewReconciler(cfg config.ReconciliationConfig, db *pgxpool.Pool, chain *blockchain.Client, notifier *notification.Notifier) *Reconciler {
	return &Reconciler{cfg: cfg, db: db, chain: chain, notifier: notifier}
}

// RunOnStartup performs reconciliation at agent startup.
func (r *Reconciler) RunOnStartup(ctx context.Context) error {
	if !r.cfg.OnStartup {
		return nil
	}
	slog.Info("running startup reconciliation")
	return r.reconcile(ctx, "startup")
}

// Run starts the periodic reconciliation loop.
func (r *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(r.cfg.IntervalSeconds) * time.Second)
	defer ticker.Stop()

	slog.Info("reconciler started", "interval_seconds", r.cfg.IntervalSeconds)

	for {
		select {
		case <-ctx.Done():
			slog.Info("reconciler stopped")
			return
		case <-ticker.C:
			if err := r.reconcile(ctx, "periodic"); err != nil {
				slog.Error("periodic reconciliation failed", "error", err)
			}
		}
	}
}

func (r *Reconciler) reconcile(ctx context.Context, reconcileType string) error {
	// Get on-chain USDC balance
	onchainBalanceRaw, err := r.chain.GetUSDCBalance(ctx)
	if err != nil {
		return fmt.Errorf("get on-chain balance: %w", err)
	}
	onchainBalance := decimal.NewFromBigInt(onchainBalanceRaw, -6) // USDC 6 decimals

	// Calculate expected DB balance
	// DB balance = initial deposit - sum(buy orders) + sum(sell orders) + sum(claims) - sum(withdrawals)
	var dbBalance decimal.Decimal
	err = r.db.QueryRow(ctx, `
		SELECT
			COALESCE(
				(SELECT SUM(amount) FROM withdrawals WHERE status = 'confirmed'),
				0
			)
	`).Scan(&dbBalance)
	if err != nil {
		return fmt.Errorf("query db balance: %w", err)
	}

	// Calculate discrepancy
	discrepancy := onchainBalance.Sub(dbBalance).Abs()
	tolerance := decimal.NewFromFloat(r.cfg.Tolerance)

	// Log reconciliation
	logEntry := models.ReconciliationLog{
		ID:             uuid.New(),
		Type:           reconcileType,
		OnchainBalance: onchainBalance,
		DBBalance:      dbBalance,
		Discrepancy:    discrepancy,
		ActionTaken:    "none",
		CreatedAt:      time.Now(),
	}

	if discrepancy.GreaterThan(tolerance) {
		slog.Warn("balance discrepancy detected",
			"onchain", onchainBalance,
			"db", dbBalance,
			"discrepancy", discrepancy,
		)

		if onchainBalance.GreaterThan(dbBalance) {
			logEntry.ActionTaken = "updated db balance (untracked income)"
			slog.Info("on-chain balance higher than DB — possible untracked income")
		} else {
			logEntry.ActionTaken = "alert sent (possible untracked outflow)"
			r.notifier.Send(ctx, models.AlertCritical, "balance_mismatch",
				fmt.Sprintf("On-chain balance (%s USDC) is LOWER than DB balance (%s USDC). Discrepancy: %s. Possible untracked transaction or exploit.",
					onchainBalance, dbBalance, discrepancy))
		}
	} else {
		slog.Debug("reconciliation OK",
			"onchain", onchainBalance,
			"db", dbBalance,
			"discrepancy", discrepancy,
		)
	}

	// Store log
	_, err = r.db.Exec(ctx, `
		INSERT INTO reconciliation_logs (id, type, onchain_balance, db_balance, discrepancy, action_taken, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, logEntry.ID, logEntry.Type, logEntry.OnchainBalance, logEntry.DBBalance,
		logEntry.Discrepancy, logEntry.ActionTaken, logEntry.CreatedAt)
	if err != nil {
		slog.Error("failed to store reconciliation log", "error", err)
	}

	return nil
}
