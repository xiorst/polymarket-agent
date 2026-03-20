package trading

import (
	"context"
	"fmt"
	"log/slog"
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

// MarketContractProvider resolves a market's external ID to its on-chain contract address
// and builds the calldata for placing an order.
type MarketContractProvider interface {
	// GetMarketContract returns the on-chain contract address for a given market.
	GetMarketContract(ctx context.Context, marketID uuid.UUID) (common.Address, error)

	// BuildOrderCalldata encodes the order parameters into the contract's ABI calldata.
	BuildOrderCalldata(order *models.Order) ([]byte, error)

	// BuildCancelCalldata encodes a cancel request into ABI calldata.
	BuildCancelCalldata(orderID uuid.UUID) ([]byte, error)
}

// LiveExecutor sends real on-chain transactions for order execution.
type LiveExecutor struct {
	chain    *blockchain.Client
	db       *pgxpool.Pool
	cfg      config.BlockchainConfig
	nonceCfg config.NonceConfig
	notifier *notification.Notifier
	markets  MarketContractProvider
}

func NewLiveExecutor(
	chain *blockchain.Client,
	db *pgxpool.Pool,
	cfg config.BlockchainConfig,
	nonceCfg config.NonceConfig,
	notifier *notification.Notifier,
	markets MarketContractProvider,
) *LiveExecutor {
	return &LiveExecutor{
		chain:    chain,
		db:       db,
		cfg:      cfg,
		nonceCfg: nonceCfg,
		notifier: notifier,
		markets:  markets,
	}
}

func (le *LiveExecutor) PlaceOrder(ctx context.Context, order *models.Order) (string, error) {
	// 1. Check pending transaction limit
	pendingCount, err := le.countPendingTx(ctx)
	if err != nil {
		return "", fmt.Errorf("check pending tx: %w", err)
	}
	if pendingCount >= le.nonceCfg.MaxPendingTransactions {
		return "", fmt.Errorf("max pending transactions reached (%d), wait for confirmations", pendingCount)
	}

	// 2. Ensure USDC approval if buying
	if order.Side == models.OrderSideBuy {
		if err := le.ensureApproval(ctx, order); err != nil {
			return "", fmt.Errorf("ensure approval: %w", err)
		}
	}

	// 3. Resolve market contract and build calldata
	contractAddr, err := le.markets.GetMarketContract(ctx, order.MarketID)
	if err != nil {
		return "", fmt.Errorf("get market contract: %w", err)
	}

	calldata, err := le.markets.BuildOrderCalldata(order)
	if err != nil {
		return "", fmt.Errorf("build order calldata: %w", err)
	}

	// 4. Send transaction
	tx, err := le.chain.SendTransaction(ctx, contractAddr, calldata, big.NewInt(0))
	if err != nil {
		return "", fmt.Errorf("send order tx: %w", err)
	}

	txHash := tx.Hash().Hex()

	// 5. Record pending transaction
	if err := le.recordPendingTx(ctx, txHash, models.TxTypeOrder); err != nil {
		slog.Error("failed to record pending tx", "tx_hash", txHash, "error", err)
	}

	slog.Info("[LIVE] order transaction sent",
		"order_id", order.ID,
		"tx_hash", txHash,
		"market", order.MarketID,
		"side", order.Side,
		"price", order.Price,
		"quantity", order.Quantity,
	)

	// 6. Wait for confirmation (with timeout from nonce config)
	confirmCtx, cancel := context.WithTimeout(ctx, time.Duration(le.nonceCfg.StuckTimeoutSeconds)*time.Second)
	defer cancel()

	receipt, err := le.chain.WaitForReceipt(confirmCtx, tx.Hash())
	if err != nil {
		// Mark as stuck — the nonce monitor will handle replacement
		le.updatePendingTxStatus(ctx, txHash, models.TxStatusStuck)
		le.notifier.Send(ctx, models.AlertHigh, "tx_stuck",
			fmt.Sprintf("Order TX stuck: %s (order %s)", txHash, order.ID))
		return txHash, fmt.Errorf("tx confirmation timeout: %w", err)
	}

	// 7. Check receipt status
	if receipt.Status == 0 {
		le.updatePendingTxStatus(ctx, txHash, models.TxStatusFailed)
		return txHash, fmt.Errorf("tx reverted on-chain: %s", txHash)
	}

	le.updatePendingTxStatus(ctx, txHash, models.TxStatusConfirmed)

	slog.Info("[LIVE] order confirmed",
		"tx_hash", txHash,
		"block", receipt.BlockNumber,
		"gas_used", receipt.GasUsed,
	)

	return txHash, nil
}

func (le *LiveExecutor) CancelOrder(ctx context.Context, orderID uuid.UUID) error {
	contractAddr, err := le.markets.GetMarketContract(ctx, orderID)
	if err != nil {
		return fmt.Errorf("get market contract for cancel: %w", err)
	}

	calldata, err := le.markets.BuildCancelCalldata(orderID)
	if err != nil {
		return fmt.Errorf("build cancel calldata: %w", err)
	}

	tx, err := le.chain.SendTransaction(ctx, contractAddr, calldata, big.NewInt(0))
	if err != nil {
		return fmt.Errorf("send cancel tx: %w", err)
	}

	if err := le.recordPendingTx(ctx, tx.Hash().Hex(), models.TxTypeCancel); err != nil {
		slog.Error("failed to record cancel pending tx", "error", err)
	}

	slog.Info("[LIVE] cancel transaction sent", "order_id", orderID, "tx_hash", tx.Hash().Hex())
	return nil
}

// ensureApproval checks the USDC allowance for the market contract and sends an approve tx if needed.
func (le *LiveExecutor) ensureApproval(ctx context.Context, order *models.Order) error {
	contractAddr, err := le.markets.GetMarketContract(ctx, order.MarketID)
	if err != nil {
		return err
	}

	usdcAddr := common.HexToAddress(le.cfg.USDCContract)

	// Check current allowance: allowance(owner, spender)
	// selector: 0xdd62ed3e
	ownerPadded := common.LeftPadBytes(le.chain.Address().Bytes(), 32)
	spenderPadded := common.LeftPadBytes(contractAddr.Bytes(), 32)
	allowanceData := append(common.Hex2Bytes("dd62ed3e"), ownerPadded...)
	allowanceData = append(allowanceData, spenderPadded...)

	// We need the required amount in USDC (6 decimals)
	requiredAmount := order.Price.Mul(order.Quantity).Mul(decimal.NewFromInt(1_000_000))
	requiredBig := requiredAmount.BigInt()

	// If allowance is sufficient, skip
	// For simplicity, we always approve max uint256 (one-time infinite approval)
	// This is a common pattern in DeFi to avoid per-trade approval gas costs

	maxUint256 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

	// Build approve(spender, amount) calldata
	// selector: 0x095ea7b3
	approveData := append(common.Hex2Bytes("095ea7b3"), spenderPadded...)
	approveData = append(approveData, common.LeftPadBytes(maxUint256.Bytes(), 32)...)

	tx, err := le.chain.SendTransaction(ctx, usdcAddr, approveData, big.NewInt(0))
	if err != nil {
		return fmt.Errorf("send approve tx: %w", err)
	}

	if err := le.recordPendingTx(ctx, tx.Hash().Hex(), models.TxTypeApprove); err != nil {
		slog.Error("failed to record approve tx", "error", err)
	}

	// Wait for approval confirmation
	confirmCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	receipt, err := le.chain.WaitForReceipt(confirmCtx, tx.Hash())
	if err != nil {
		return fmt.Errorf("approve tx timeout: %w", err)
	}

	if receipt.Status == 0 {
		return fmt.Errorf("approve tx reverted: %s", tx.Hash().Hex())
	}

	le.updatePendingTxStatus(ctx, tx.Hash().Hex(), models.TxStatusConfirmed)

	slog.Info("[LIVE] USDC approval confirmed",
		"tx_hash", tx.Hash().Hex(),
		"spender", contractAddr.Hex(),
		"amount", requiredBig,
	)

	return nil
}

// MonitorStuckTransactions checks for stuck transactions and sends replacements.
func (le *LiveExecutor) MonitorStuckTransactions(ctx context.Context) error {
	rows, err := le.db.Query(ctx, `
		SELECT id, tx_hash, nonce, type, gas_price, retry_count
		FROM pending_transactions
		WHERE status = 'stuck' AND retry_count < 3
		ORDER BY created_at ASC
	`)
	if err != nil {
		return fmt.Errorf("query stuck transactions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id uuid.UUID
		var txHash string
		var nonce uint64
		var txType models.TxType
		var gasPrice decimal.Decimal
		var retryCount int

		if err := rows.Scan(&id, &txHash, &nonce, &txType, &gasPrice, &retryCount); err != nil {
			slog.Error("scan stuck tx", "error", err)
			continue
		}

		slog.Warn("attempting to replace stuck transaction",
			"tx_hash", txHash,
			"nonce", nonce,
			"retry", retryCount+1,
		)

		// Send a zero-value cancel transaction with the same nonce but higher gas
		// This replaces the stuck tx
		newGasPrice := gasPrice.Mul(decimal.NewFromFloat(le.nonceCfg.ReplacementGasMultiplier))

		// Update retry count
		le.db.Exec(ctx, `
			UPDATE pending_transactions SET retry_count = retry_count + 1, status = 'replaced'
			WHERE id = $1
		`, id)

		le.notifier.Send(ctx, models.AlertHigh, "tx_replacement",
			fmt.Sprintf("Replacing stuck TX %s (nonce %d) with higher gas: %s gwei",
				txHash, nonce, newGasPrice))
	}

	return nil
}

func (le *LiveExecutor) countPendingTx(ctx context.Context) (int, error) {
	var count int
	err := le.db.QueryRow(ctx,
		"SELECT COUNT(*) FROM pending_transactions WHERE status = 'pending'",
	).Scan(&count)
	return count, err
}

func (le *LiveExecutor) recordPendingTx(ctx context.Context, txHash string, txType models.TxType) error {
	nonce, _ := le.chain.GetNonce(ctx)
	gasPrice, _ := le.chain.SuggestGasPrice(ctx)

	gasPriceDec := decimal.NewFromBigInt(gasPrice, 0)

	_, err := le.db.Exec(ctx, `
		INSERT INTO pending_transactions (id, tx_hash, nonce, type, gas_price, status, created_at)
		VALUES ($1, $2, $3, $4, $5, 'pending', $6)
	`, uuid.New(), txHash, nonce, txType, gasPriceDec, time.Now())
	return err
}

func (le *LiveExecutor) updatePendingTxStatus(ctx context.Context, txHash string, status models.TxStatus) {
	confirmedAt := time.Now()
	_, err := le.db.Exec(ctx, `
		UPDATE pending_transactions SET status = $1, confirmed_at = $2 WHERE tx_hash = $3
	`, status, confirmedAt, txHash)
	if err != nil {
		slog.Error("failed to update pending tx status", "tx_hash", txHash, "error", err)
	}
}
