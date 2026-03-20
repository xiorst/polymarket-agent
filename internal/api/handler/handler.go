package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/x10rst/ai-agent-autonom/internal/config"
	"github.com/x10rst/ai-agent-autonom/internal/models"
	"github.com/x10rst/ai-agent-autonom/internal/reliability"
)

type Handler struct {
	db             *pgxpool.Pool
	cfg            *config.Config
	cfgMu          sync.RWMutex
	circuitBreaker *reliability.CircuitBreaker
	agentStatus    *AgentState
}

// AgentState tracks the runtime state of the agent.
type AgentState struct {
	Status    models.AgentStatus
	StartedAt time.Time
}

func New(db *pgxpool.Pool, cfg *config.Config, cb *reliability.CircuitBreaker, state *AgentState) *Handler {
	return &Handler{
		db:             db,
		cfg:            cfg,
		circuitBreaker: cb,
		agentStatus:    state,
	}
}

// Health is the health check endpoint.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GetAgentStatus returns the current agent status.
func (h *Handler) GetAgentStatus(w http.ResponseWriter, r *http.Request) {
	uptime := time.Since(h.agentStatus.StartedAt).Truncate(time.Second)
	resp := models.AgentStatusResponse{
		Status:      h.agentStatus.Status,
		TradingMode: models.TradingMode(h.cfg.Trading.Mode),
		Uptime:      uptime.String(),
		CBState:     h.circuitBreaker.State(),
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetPortfolio returns the portfolio summary.
func (h *Handler) GetPortfolio(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var totalPnL decimal.Decimal
	var openPositions int

	h.db.QueryRow(ctx,
		"SELECT COALESCE(SUM(realized_pnl), 0) FROM positions WHERE status = 'closed'",
	).Scan(&totalPnL)

	h.db.QueryRow(ctx,
		"SELECT COUNT(*) FROM positions WHERE status = 'open'",
	).Scan(&openPositions)

	balance := decimal.NewFromFloat(h.cfg.Trading.InitialBalance).Add(totalPnL)

	// Calculate win rate
	var totalClosed, totalWins int
	h.db.QueryRow(ctx, "SELECT COUNT(*) FROM positions WHERE status = 'closed'").Scan(&totalClosed)
	h.db.QueryRow(ctx, "SELECT COUNT(*) FROM positions WHERE status = 'closed' AND realized_pnl > 0").Scan(&totalWins)

	winRate := 0.0
	if totalClosed > 0 {
		winRate = float64(totalWins) / float64(totalClosed) * 100
	}

	resp := models.PortfolioSummary{
		Balance:       balance,
		TotalPnL:      totalPnL,
		OpenPositions: openPositions,
		WinRate:       winRate,
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetPositions returns all open positions.
func (h *Handler) GetPositions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rows, err := h.db.Query(ctx, `
		SELECT id, market_id, outcome, entry_price, quantity, status, realized_pnl, created_at, closed_at
		FROM positions
		WHERE status = 'open'
		ORDER BY created_at DESC
	`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	var positions []models.Position
	for rows.Next() {
		var p models.Position
		if err := rows.Scan(&p.ID, &p.MarketID, &p.Outcome, &p.EntryPrice, &p.Quantity, &p.Status, &p.RealizedPnL, &p.CreatedAt, &p.ClosedAt); err != nil {
			continue
		}
		positions = append(positions, p)
	}

	if positions == nil {
		positions = []models.Position{}
	}
	writeJSON(w, http.StatusOK, positions)
}

// GetOrders returns recent orders with pagination.
func (h *Handler) GetOrders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rows, err := h.db.Query(ctx, `
		SELECT id, market_id, side, order_type, outcome, price, quantity, status, tx_hash, is_paper, created_at, updated_at
		FROM orders
		ORDER BY created_at DESC
		LIMIT 50
	`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	var orders []models.Order
	for rows.Next() {
		var o models.Order
		if err := rows.Scan(&o.ID, &o.MarketID, &o.Side, &o.OrderType, &o.Outcome, &o.Price, &o.Quantity, &o.Status, &o.TxHash, &o.IsPaper, &o.CreatedAt, &o.UpdatedAt); err != nil {
			continue
		}
		orders = append(orders, o)
	}

	if orders == nil {
		orders = []models.Order{}
	}
	writeJSON(w, http.StatusOK, orders)
}

// PostAgentStart starts the trading agent.
func (h *Handler) PostAgentStart(w http.ResponseWriter, r *http.Request) {
	h.agentStatus.Status = models.AgentRunning
	writeJSON(w, http.StatusOK, map[string]string{"status": "running"})
}

// PostAgentPause pauses the trading agent.
func (h *Handler) PostAgentPause(w http.ResponseWriter, r *http.Request) {
	h.agentStatus.Status = models.AgentPaused
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

// PostAgentStop stops the trading agent.
func (h *Handler) PostAgentStop(w http.ResponseWriter, r *http.Request) {
	h.agentStatus.Status = models.AgentStopped
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// GetConfig returns the current trading configuration.
func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"trading":  h.cfg.Trading,
		"risk":     h.cfg.Risk,
		"liquidity": h.cfg.Liquidity,
	})
}

// PostForceClosePosition force-closes an open position by ID.
// POST /api/v1/positions/{id}/close
func (h *Handler) PostForceClosePosition(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	positionID := r.PathValue("id")
	if positionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "position id is required"})
		return
	}

	posUUID, err := uuid.Parse(positionID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid position id format"})
		return
	}

	// Verify position exists and is open
	var status models.PositionStatus
	var marketID uuid.UUID
	var outcome string
	var entryPrice, quantity decimal.Decimal

	err = h.db.QueryRow(ctx, `
		SELECT status, market_id, outcome, entry_price, quantity
		FROM positions WHERE id = $1
	`, posUUID).Scan(&status, &marketID, &outcome, &entryPrice, &quantity)

	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "position not found"})
		return
	}

	if status != models.PositionStatusOpen {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "position is already closed"})
		return
	}

	// Close the position with realized PnL = 0 (force close, actual PnL depends on market)
	now := time.Now()
	_, err = h.db.Exec(ctx, `
		UPDATE positions SET status = 'closed', closed_at = $1, realized_pnl = 0 WHERE id = $2
	`, now, posUUID)

	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("close position: %s", err)})
		return
	}

	slog.Info("position force-closed via API",
		"position_id", posUUID,
		"market_id", marketID,
		"outcome", outcome,
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"message":     "position closed",
		"position_id": posUUID,
		"market_id":   marketID,
		"outcome":     outcome,
		"closed_at":   now,
	})
}

// PutConfig updates the runtime trading configuration.
// PUT /api/v1/config
func (h *Handler) PutConfig(w http.ResponseWriter, r *http.Request) {
	var req ConfigUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	h.cfgMu.Lock()
	defer h.cfgMu.Unlock()

	updated := []string{}

	// Trading params
	if req.TradingMode != nil {
		validModes := map[string]bool{"paper": true, "live": true, "backtest": true}
		if !validModes[*req.TradingMode] {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid trading mode"})
			return
		}

		// Safety: switching to live requires confirmation
		if *req.TradingMode == "live" && h.cfg.Trading.Mode != "live" {
			if !req.ConfirmLive {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": "switching to live mode requires confirm_live: true",
				})
				return
			}
		}

		h.cfg.Trading.Mode = *req.TradingMode
		updated = append(updated, "trading.mode")
	}

	if req.DailyProfitTarget != nil {
		h.cfg.Trading.DailyProfitTarget = *req.DailyProfitTarget
		updated = append(updated, "trading.daily_profit_target")
	}

	// Risk params
	if req.StopLoss != nil {
		if *req.StopLoss <= 0 || *req.StopLoss > 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "stop_loss must be between 0 and 1"})
			return
		}
		h.cfg.Risk.StopLoss = *req.StopLoss
		updated = append(updated, "risk.stop_loss")
	}

	if req.DailyLossLimit != nil {
		if *req.DailyLossLimit <= 0 || *req.DailyLossLimit > 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "daily_loss_limit must be between 0 and 1"})
			return
		}
		h.cfg.Risk.DailyLossLimit = *req.DailyLossLimit
		updated = append(updated, "risk.daily_loss_limit")
	}

	if req.MaxSlippageTolerance != nil {
		if *req.MaxSlippageTolerance <= 0 || *req.MaxSlippageTolerance > 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "max_slippage_tolerance must be between 0 and 1"})
			return
		}
		h.cfg.Risk.MaxSlippageTolerance = *req.MaxSlippageTolerance
		updated = append(updated, "risk.max_slippage_tolerance")
	}

	if req.MaxPositionPerMarket != nil {
		if *req.MaxPositionPerMarket <= 0 || *req.MaxPositionPerMarket > 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "max_position_per_market must be between 0 and 1"})
			return
		}
		h.cfg.Risk.MaxPositionPerMarket = *req.MaxPositionPerMarket
		updated = append(updated, "risk.max_position_per_market")
	}

	if req.MaxTotalExposure != nil {
		if *req.MaxTotalExposure <= 0 || *req.MaxTotalExposure > 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "max_total_exposure must be between 0 and 1"})
			return
		}
		h.cfg.Risk.MaxTotalExposure = *req.MaxTotalExposure
		updated = append(updated, "risk.max_total_exposure")
	}

	// ML params
	if req.ConfidenceThreshold != nil {
		if *req.ConfidenceThreshold <= 0 || *req.ConfidenceThreshold > 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "confidence_threshold must be between 0 and 1"})
			return
		}
		h.cfg.MarketAnalysis.ConfidenceThreshold = *req.ConfidenceThreshold
		updated = append(updated, "market_analysis.confidence_threshold")
	}

	if req.MispricingThreshold != nil {
		h.cfg.MarketAnalysis.MispricingThreshold = *req.MispricingThreshold
		updated = append(updated, "market_analysis.mispricing_threshold")
	}

	slog.Info("config updated via API", "fields", updated)

	writeJSON(w, http.StatusOK, map[string]any{
		"message":        "config updated",
		"updated_fields": updated,
		"current_config": map[string]any{
			"trading":         h.cfg.Trading,
			"risk":            h.cfg.Risk,
			"market_analysis": h.cfg.MarketAnalysis,
		},
	})
}

// ConfigUpdateRequest is the request body for PUT /api/v1/config.
type ConfigUpdateRequest struct {
	// Trading
	TradingMode       *string  `json:"trading_mode,omitempty"`
	DailyProfitTarget *float64 `json:"daily_profit_target,omitempty"`
	ConfirmLive       bool     `json:"confirm_live,omitempty"`

	// Risk
	StopLoss              *float64 `json:"stop_loss,omitempty"`
	DailyLossLimit        *float64 `json:"daily_loss_limit,omitempty"`
	MaxSlippageTolerance  *float64 `json:"max_slippage_tolerance,omitempty"`
	MaxPositionPerMarket  *float64 `json:"max_position_per_market,omitempty"`
	MaxTotalExposure      *float64 `json:"max_total_exposure,omitempty"`

	// ML
	ConfidenceThreshold  *float64 `json:"confidence_threshold,omitempty"`
	MispricingThreshold  *float64 `json:"mispricing_threshold,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
