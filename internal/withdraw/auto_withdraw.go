package withdraw

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/x10rst/ai-agent-autonom/internal/blockchain"
	"github.com/x10rst/ai-agent-autonom/internal/config"
	"github.com/x10rst/ai-agent-autonom/internal/models"
	"github.com/x10rst/ai-agent-autonom/internal/notification"
)

// AutoWithdrawer handles automatic profit withdrawal to cold wallet.
type AutoWithdrawer struct {
	cfg       config.AutoWithdrawConfig
	tradeCfg  config.TradingConfig
	db        *pgxpool.Pool
	chain     *blockchain.Client
	notifier  *notification.Notifier
	startTime time.Time
}

func NewAutoWithdrawer(
	cfg config.AutoWithdrawConfig,
	tradeCfg config.TradingConfig,
	db *pgxpool.Pool,
	chain *blockchain.Client,
	notifier *notification.Notifier,
) *AutoWithdrawer {
	return &AutoWithdrawer{
		cfg:       cfg,
		tradeCfg:  tradeCfg,
		db:        db,
		chain:     chain,
		notifier:  notifier,
		startTime: time.Now(),
	}
}

// Run starts the auto-withdraw check loop.
func (aw *AutoWithdrawer) Run(ctx context.Context) {
	if !aw.cfg.Enabled {
		slog.Info("auto-withdraw is disabled")
		return
	}

	if aw.cfg.SafeWalletAddress == "" {
		slog.Warn("auto-withdraw enabled but safe_wallet_address is empty, skipping")
		return
	}

	ticker := time.NewTicker(time.Duration(aw.cfg.CheckIntervalSeconds) * time.Second)
	defer ticker.Stop()

	slog.Info("auto-withdraw started",
		"safe_wallet", aw.cfg.SafeWalletAddress,
		"check_interval", aw.cfg.CheckIntervalSeconds,
	)

	for {
		select {
		case <-ctx.Done():
			slog.Info("auto-withdraw stopped")
			return
		case <-ticker.C:
			if err := aw.check(ctx); err != nil {
				slog.Error("auto-withdraw check failed", "error", err)
			}
		}
	}
}

func (aw *AutoWithdrawer) check(ctx context.Context) error {
	// Calculate current day number
	dayNumber := aw.getCurrentDay()

	// Calculate expected balance for today based on trading plan
	expectedBalance := aw.getExpectedBalance(dayNumber)
	nextDayBalance := aw.getExpectedBalance(dayNumber + 1)

	// Get current USDC balance
	balance, err := aw.chain.GetUSDCBalance(ctx)
	if err != nil {
		return fmt.Errorf("get USDC balance: %w", err)
	}

	// USDC has 6 decimals
	currentBalance := decimal.NewFromBigInt(balance, -6)

	slog.Debug("auto-withdraw check",
		"day", dayNumber,
		"current_balance", currentBalance,
		"expected_balance", expectedBalance,
		"next_day_balance", nextDayBalance,
	)

	// Check if expected balance reached
	if currentBalance.LessThan(expectedBalance) {
		return nil // not yet reached target
	}

	// Calculate withdraw amount: current - next day's working capital
	withdrawAmount := currentBalance.Sub(nextDayBalance)

	// Ensure minimum hot wallet balance
	minBalance := decimal.NewFromFloat(aw.cfg.MinHotWalletBalance)
	remainingAfterWithdraw := currentBalance.Sub(withdrawAmount)
	if remainingAfterWithdraw.LessThan(minBalance) {
		withdrawAmount = currentBalance.Sub(minBalance)
	}

	if withdrawAmount.LessThanOrEqual(decimal.Zero) {
		return nil
	}

	slog.Info("auto-withdraw triggered",
		"day", dayNumber,
		"amount", withdrawAmount,
		"remaining", currentBalance.Sub(withdrawAmount),
	)

	// Execute withdrawal
	return aw.executeWithdraw(ctx, dayNumber, withdrawAmount)
}

func (aw *AutoWithdrawer) executeWithdraw(ctx context.Context, dayNumber int, amount decimal.Decimal) error {
	safeAddr := common.HexToAddress(aw.cfg.SafeWalletAddress)

	// Convert to USDC units (6 decimals)
	amountBig := amount.Mul(decimal.NewFromInt(1_000_000)).BigInt()

	tx, err := aw.chain.TransferUSDC(ctx, safeAddr, amountBig)
	if err != nil {
		aw.notifier.Send(ctx, models.AlertCritical, "auto_withdraw_failed",
			fmt.Sprintf("Auto-withdraw of %s USDC failed: %s", amount, err))
		return fmt.Errorf("transfer USDC: %w", err)
	}

	// Record in database
	_, dbErr := aw.db.Exec(ctx, `
		INSERT INTO withdrawals (id, day_number, amount, from_address, to_address, tx_hash, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, uuid.New(), dayNumber, amount, aw.chain.Address().Hex(), safeAddr.Hex(),
		tx.Hash().Hex(), models.WithdrawStatusPending, time.Now())

	if dbErr != nil {
		slog.Error("failed to record withdrawal in DB", "error", dbErr)
	}

	aw.notifier.Send(ctx, models.AlertLow, "auto_withdraw_success",
		fmt.Sprintf("Auto-withdraw: %s USDC sent to cold wallet. Day %d. TX: %s",
			amount, dayNumber, tx.Hash().Hex()))

	return nil
}

// getCurrentDay returns the current trading day number (1-based).
func (aw *AutoWithdrawer) getCurrentDay() int {
	elapsed := time.Since(aw.startTime)
	return int(elapsed.Hours()/24) + 1
}

// getExpectedBalance calculates the expected balance for a given day
// based on the 25% compounding trading plan.
func (aw *AutoWithdrawer) getExpectedBalance(day int) decimal.Decimal {
	initial := aw.tradeCfg.InitialBalance
	rate := aw.tradeCfg.DailyProfitTarget

	// Balance = initial * (1 + rate)^day
	multiplier := math.Pow(1+rate, float64(day))
	expected := initial * multiplier

	return decimal.NewFromFloat(expected)
}

// ToUSDCUnits converts a decimal USDC amount to big.Int with 6 decimals.
func ToUSDCUnits(amount decimal.Decimal) *big.Int {
	return amount.Mul(decimal.NewFromInt(1_000_000)).BigInt()
}
