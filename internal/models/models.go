package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// --- Enums ---

type MarketStatus string

const (
	MarketStatusActive   MarketStatus = "active"
	MarketStatusResolved MarketStatus = "resolved"
	MarketStatusClosed   MarketStatus = "closed"
)

type OrderSide string

const (
	OrderSideBuy  OrderSide = "buy"
	OrderSideSell OrderSide = "sell"
)

type OrderType string

const (
	OrderTypeLimit  OrderType = "limit"
	OrderTypeMarket OrderType = "market"
)

type OrderStatus string

const (
	OrderStatusPending        OrderStatus = "pending"
	OrderStatusFilled         OrderStatus = "filled"
	OrderStatusPartiallyFilled OrderStatus = "partially_filled"
	OrderStatusCancelled      OrderStatus = "cancelled"
	OrderStatusFailed         OrderStatus = "failed"
)

type PositionStatus string

const (
	PositionStatusOpen   PositionStatus = "open"
	PositionStatusClosed PositionStatus = "closed"
)

type TxStatus string

const (
	TxStatusPending   TxStatus = "pending"
	TxStatusConfirmed TxStatus = "confirmed"
	TxStatusStuck     TxStatus = "stuck"
	TxStatusReplaced  TxStatus = "replaced"
	TxStatusFailed    TxStatus = "failed"
)

type TxType string

const (
	TxTypeOrder    TxType = "order"
	TxTypeApprove  TxType = "approve"
	TxTypeClaim    TxType = "claim"
	TxTypeWithdraw TxType = "withdraw"
	TxTypeCancel   TxType = "cancel"
)

type WithdrawStatus string

const (
	WithdrawStatusPending   WithdrawStatus = "pending"
	WithdrawStatusConfirmed WithdrawStatus = "confirmed"
	WithdrawStatusFailed    WithdrawStatus = "failed"
)

type CircuitBreakerState string

const (
	CBStateClosed   CircuitBreakerState = "closed"
	CBStateOpen     CircuitBreakerState = "open"
	CBStateHalfOpen CircuitBreakerState = "half_open"
)

type AlertSeverity string

const (
	AlertCritical AlertSeverity = "critical"
	AlertHigh     AlertSeverity = "high"
	AlertMedium   AlertSeverity = "medium"
	AlertLow      AlertSeverity = "low"
	AlertInfo     AlertSeverity = "info"
)

type AgentStatus string

const (
	AgentRunning AgentStatus = "running"
	AgentPaused  AgentStatus = "paused"
	AgentStopped AgentStatus = "stopped"
)

type TradingMode string

const (
	TradingModeLive     TradingMode = "live"
	TradingModePaper    TradingMode = "paper"
	TradingModeBacktest TradingMode = "backtest"
)

// --- Core Entities ---

type Market struct {
	ID         uuid.UUID        `json:"id" db:"id"`
	ExternalID string           `json:"external_id" db:"external_id"`
	Question   string           `json:"question" db:"question"`
	Outcomes   []OutcomePrice   `json:"outcomes" db:"outcomes"`
	EndDate    time.Time        `json:"end_date" db:"end_date"`
	Status     MarketStatus     `json:"status" db:"status"`
	CreatedAt  time.Time        `json:"created_at" db:"created_at"`
	UpdatedAt  time.Time        `json:"updated_at" db:"updated_at"`
}

type OutcomePrice struct {
	Name  string          `json:"name"`
	Price decimal.Decimal `json:"price"`
}

type MarketSnapshot struct {
	ID              uuid.UUID       `json:"id" db:"id"`
	MarketID        uuid.UUID       `json:"market_id" db:"market_id"`
	OutcomePrices   []OutcomePrice  `json:"outcome_prices" db:"outcome_prices"`
	Volume          decimal.Decimal `json:"volume" db:"volume"`           // Cumulative volume (from API)
	VolumePerPeriod decimal.Decimal `json:"volume_per_period" db:"volume_per_period"` // Delta vs previous snapshot
	Liquidity       decimal.Decimal `json:"liquidity" db:"liquidity"`
	BidDepth        decimal.Decimal `json:"bid_depth" db:"bid_depth"`     // Total quantity on bid side
	AskDepth        decimal.Decimal `json:"ask_depth" db:"ask_depth"`     // Total quantity on ask side
	Spread          decimal.Decimal `json:"spread" db:"spread"`           // Best ask - best bid
	CapturedAt      time.Time       `json:"captured_at" db:"captured_at"`

	// ExternalSignal is not stored in DB — injected at prediction time from Telegram feed.
	// Pointer so nil = no external context available.
	ExternalSignal interface{} `json:"-" db:"-"` // use *scorer.ExternalSignal via ml package
}

type Signal struct {
	ID               uuid.UUID       `json:"id" db:"id"`
	MarketID         uuid.UUID       `json:"market_id" db:"market_id"`
	PredictedOutcome string          `json:"predicted_outcome" db:"predicted_outcome"`
	Confidence       decimal.Decimal `json:"confidence" db:"confidence"`
	MarketPrice      decimal.Decimal `json:"market_price" db:"market_price"`
	CreatedAt        time.Time       `json:"created_at" db:"created_at"`
}

type Order struct {
	ID             uuid.UUID       `json:"id" db:"id"`
	MarketID       uuid.UUID       `json:"market_id" db:"market_id"`
	Side           OrderSide       `json:"side" db:"side"`
	OrderType      OrderType       `json:"order_type" db:"order_type"`
	Outcome        string          `json:"outcome" db:"outcome"`
	Price          decimal.Decimal `json:"price" db:"price"`         // Requested price
	Quantity       decimal.Decimal `json:"quantity" db:"quantity"`   // Requested quantity
	FillPrice      decimal.Decimal `json:"fill_price" db:"fill_price"`       // Actual fill price (after slippage)
	FilledQuantity decimal.Decimal `json:"filled_quantity" db:"filled_quantity"` // Actual filled qty (partial fill)
	FeeCost        decimal.Decimal `json:"fee_cost" db:"fee_cost"`           // Gas/fee cost in USD
	Status         OrderStatus     `json:"status" db:"status"`
	TxHash         *string         `json:"tx_hash,omitempty" db:"tx_hash"`
	IdempotencyKey string          `json:"idempotency_key" db:"idempotency_key"`
	IsPaper        bool            `json:"is_paper" db:"is_paper"`
	CreatedAt      time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at" db:"updated_at"`
}

type Position struct {
	ID          uuid.UUID       `json:"id" db:"id"`
	MarketID    uuid.UUID       `json:"market_id" db:"market_id"`
	Outcome     string          `json:"outcome" db:"outcome"`
	EntryPrice  decimal.Decimal `json:"entry_price" db:"entry_price"`
	Quantity    decimal.Decimal `json:"quantity" db:"quantity"`
	Status      PositionStatus  `json:"status" db:"status"`
	RealizedPnL decimal.Decimal `json:"realized_pnl" db:"realized_pnl"`
	CreatedAt   time.Time       `json:"created_at" db:"created_at"`
	ClosedAt    *time.Time      `json:"closed_at,omitempty" db:"closed_at"`
}

// --- Reliability Entities ---

type PendingTransaction struct {
	ID          uuid.UUID       `json:"id" db:"id"`
	TxHash      string          `json:"tx_hash" db:"tx_hash"`
	Nonce       uint64          `json:"nonce" db:"nonce"`
	Type        TxType          `json:"type" db:"type"`
	GasPrice    decimal.Decimal `json:"gas_price" db:"gas_price"`
	Status      TxStatus        `json:"status" db:"status"`
	RetryCount  int             `json:"retry_count" db:"retry_count"`
	CreatedAt   time.Time       `json:"created_at" db:"created_at"`
	ConfirmedAt *time.Time      `json:"confirmed_at,omitempty" db:"confirmed_at"`
}

type Withdrawal struct {
	ID          uuid.UUID       `json:"id" db:"id"`
	DayNumber   int             `json:"day_number" db:"day_number"`
	Amount      decimal.Decimal `json:"amount" db:"amount"`
	FromAddress string          `json:"from_address" db:"from_address"`
	ToAddress   string          `json:"to_address" db:"to_address"`
	TxHash      string          `json:"tx_hash" db:"tx_hash"`
	Status      WithdrawStatus  `json:"status" db:"status"`
	CreatedAt   time.Time       `json:"created_at" db:"created_at"`
}

type CircuitBreakerEvent struct {
	ID          uuid.UUID           `json:"id" db:"id"`
	TriggerType string              `json:"trigger_type" db:"trigger_type"`
	StateFrom   CircuitBreakerState `json:"state_from" db:"state_from"`
	StateTo     CircuitBreakerState `json:"state_to" db:"state_to"`
	Details     map[string]any      `json:"details" db:"details"`
	CreatedAt   time.Time           `json:"created_at" db:"created_at"`
}

type ReconciliationLog struct {
	ID             uuid.UUID       `json:"id" db:"id"`
	Type           string          `json:"type" db:"type"`
	OnchainBalance decimal.Decimal `json:"onchain_balance" db:"onchain_balance"`
	DBBalance      decimal.Decimal `json:"db_balance" db:"db_balance"`
	Discrepancy    decimal.Decimal `json:"discrepancy" db:"discrepancy"`
	ActionTaken    string          `json:"action_taken" db:"action_taken"`
	CreatedAt      time.Time       `json:"created_at" db:"created_at"`
}

type Alert struct {
	ID        uuid.UUID     `json:"id" db:"id"`
	Severity  AlertSeverity `json:"severity" db:"severity"`
	EventType string        `json:"event_type" db:"event_type"`
	Message   string        `json:"message" db:"message"`
	Channel   string        `json:"channel" db:"channel"`
	Delivered bool          `json:"delivered" db:"delivered"`
	CreatedAt time.Time     `json:"created_at" db:"created_at"`
}

// --- Dashboard DTOs ---

type PortfolioSummary struct {
	Balance       decimal.Decimal `json:"balance"`
	TotalPnL      decimal.Decimal `json:"total_pnl"`
	OpenPositions int             `json:"open_positions"`
	DayNumber     int             `json:"day_number"`
	WinRate       float64         `json:"win_rate"`
}

type AgentStatusResponse struct {
	Status      AgentStatus `json:"status"`
	TradingMode TradingMode `json:"trading_mode"`
	Uptime      string      `json:"uptime"`
	CBState     CircuitBreakerState `json:"circuit_breaker_state"`
}
