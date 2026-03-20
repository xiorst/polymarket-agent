// Package backtest provides a historical data replay engine to validate
// trading strategy performance before deploying to paper or live mode.
package backtest

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/x10rst/ai-agent-autonom/internal/ml"
	"github.com/x10rst/ai-agent-autonom/internal/models"
)

// ──────────────────────────────────────────
// Data types
// ──────────────────────────────────────────

// Bar is a single OHLCV-style data point for one market outcome.
type Bar struct {
	Timestamp time.Time
	MarketID  string
	Outcome   string // e.g. "Yes" / "No"
	Price     float64
	Volume    float64
	Liquidity float64
}

// DataSource supplies historical bars ordered by timestamp (ascending).
type DataSource interface {
	// Bars returns all bars for a given market, oldest first.
	Bars(marketID string) ([]Bar, error)
	// Markets returns all unique market IDs in the dataset.
	Markets() ([]string, error)
}

// ──────────────────────────────────────────
// Config
// ──────────────────────────────────────────

// Config controls backtest parameters.
type Config struct {
	InitialBalance    float64 // Starting USDC (e.g. 10.0)
	ConfidenceMin     float64 // Min ML confidence to open a position (0–1)
	MaxPositionPct    float64 // Max % of balance per position (0–1)
	SlippagePct       float64 // Simulated slippage on fill (0–1)
	WarmupBars        int     // Bars fed to ML before trading starts
	WindowSize        int     // Rolling lookback window for ML features
	StopLossPct       float64 // Auto-close if loss > X% of entry (0–1)
	TakeProfitPct     float64 // Auto-close if profit > X% of entry (0–1)
}

func DefaultConfig() Config {
	return Config{
		InitialBalance: 10.0,
		ConfidenceMin:  0.60,
		MaxPositionPct: 0.20,
		SlippagePct:    0.005, // 0.5%
		WarmupBars:     20,
		WindowSize:     30,
		StopLossPct:    0.05,  // 5%
		TakeProfitPct:  0.15,  // 15%
	}
}

// ──────────────────────────────────────────
// Results
// ──────────────────────────────────────────

// Trade records a completed round-trip trade (open + close).
type Trade struct {
	MarketID   string
	Outcome    string
	EntryTime  time.Time
	ExitTime   time.Time
	EntryPrice float64
	ExitPrice  float64
	Quantity   float64
	PnL        float64
	ExitReason string // "take_profit" | "stop_loss" | "expired" | "close"
}

// Report summarises backtest results.
type Report struct {
	InitialBalance  float64
	FinalBalance    float64
	TotalPnL        float64
	PnLPct          float64
	WinRate         float64
	TotalTrades     int
	WinningTrades   int
	LosingTrades    int
	MaxDrawdown     float64 // percentage
	SharpeRatio     float64 // annualised (daily returns)
	AvgWin          float64
	AvgLoss         float64
	ProfitFactor    float64
	Trades          []Trade
	EquityCurve     []EquityPoint
}

// EquityPoint is a single point on the equity curve.
type EquityPoint struct {
	Time    time.Time
	Balance float64
}

// ──────────────────────────────────────────
// Engine
// ──────────────────────────────────────────

// Engine replays historical data through the ML predictor and simulates trades.
type Engine struct {
	cfg       Config
	predictor *ml.StatisticalPredictor
	ds        DataSource
}

func New(cfg Config, ds DataSource) *Engine {
	return &Engine{
		cfg:       cfg,
		predictor: ml.NewDefaultPredictor(),
		ds:        ds,
	}
}

func NewWithPredictor(cfg Config, ds DataSource, p *ml.StatisticalPredictor) *Engine {
	return &Engine{cfg: cfg, predictor: p, ds: ds}
}

// Run executes the backtest and returns a Report.
func (e *Engine) Run(ctx context.Context) (*Report, error) {
	markets, err := e.ds.Markets()
	if err != nil {
		return nil, fmt.Errorf("backtest: list markets: %w", err)
	}

	balance := e.cfg.InitialBalance
	var allTrades []Trade
	var equityCurve []EquityPoint

	equityCurve = append(equityCurve, EquityPoint{Time: time.Now(), Balance: balance})

	for _, mktID := range markets {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		bars, err := e.ds.Bars(mktID)
		if err != nil {
			return nil, fmt.Errorf("backtest: bars for %s: %w", mktID, err)
		}
		if len(bars) < e.cfg.WarmupBars+2 {
			continue // not enough data
		}

		trades, balanceAfter, curve := e.simulateMarket(ctx, mktID, bars, balance)
		balance = balanceAfter
		allTrades = append(allTrades, trades...)
		equityCurve = append(equityCurve, curve...)
	}

	return buildReport(e.cfg.InitialBalance, balance, allTrades, equityCurve), nil
}

// simulateMarket runs the strategy on a single market's bar series.
func (e *Engine) simulateMarket(ctx context.Context, mktID string, bars []Bar, startBalance float64) ([]Trade, float64, []EquityPoint) {
	balance := startBalance
	var trades []Trade
	var curve []EquityPoint

	// Track unique outcomes in this market
	outcomeSet := map[string]struct{}{}
	for _, b := range bars {
		outcomeSet[b.Outcome] = struct{}{}
	}
	outcomes := make([]string, 0, len(outcomeSet))
	for o := range outcomeSet {
		outcomes = append(outcomes, o)
	}
	sort.Strings(outcomes)

	// Group bars by outcome → price series (indexed by bar position)
	// We need a unified timeline across all outcomes.
	// Build timeline from the "Yes" or first outcome.
	primary := outcomes[0]
	var timeline []Bar
	for _, b := range bars {
		if b.Outcome == primary {
			timeline = append(timeline, b)
		}
	}
	sort.Slice(timeline, func(i, j int) bool {
		return timeline[i].Timestamp.Before(timeline[j].Timestamp)
	})

	type openPosition struct {
		outcome    string
		entryPrice float64
		quantity   float64
		entryTime  time.Time
		entryBar   int
	}

	var pos *openPosition

	window := e.cfg.WindowSize
	warmup := e.cfg.WarmupBars

	for i := warmup; i < len(timeline)-1; i++ {
		if ctx.Err() != nil {
			break
		}

		// Build snapshot window
		start := i - window + 1
		if start < 0 {
			start = 0
		}
		windowBars := timeline[start : i+1]
		snapshots := barsToSnapshots(windowBars, outcomes, bars)

		// ML prediction
		pred, err := e.predictor.Predict(ctx, mktID, snapshots)
		if err != nil || pred == nil || pred.Confidence < e.cfg.ConfidenceMin {
			// No signal — check stop/take on open position
			if pos != nil {
				curPrice := priceAtBar(bars, timeline[i].Timestamp, pos.outcome)
				pnlPct := (curPrice - pos.entryPrice) / pos.entryPrice
				if pnlPct <= -e.cfg.StopLossPct {
					t, newBal := closePosition(pos.outcome, pos.entryPrice, curPrice, pos.quantity, pos.entryTime, timeline[i].Timestamp, balance, "stop_loss", mktID)
					balance = newBal
					trades = append(trades, t)
					curve = append(curve, EquityPoint{Time: timeline[i].Timestamp, Balance: balance})
					pos = nil
				} else if pnlPct >= e.cfg.TakeProfitPct {
					t, newBal := closePosition(pos.outcome, pos.entryPrice, curPrice, pos.quantity, pos.entryTime, timeline[i].Timestamp, balance, "take_profit", mktID)
					balance = newBal
					trades = append(trades, t)
					curve = append(curve, EquityPoint{Time: timeline[i].Timestamp, Balance: balance})
					pos = nil
				}
			}
			continue
		}

		// New signal — if no open position or different outcome, open one
		if pos == nil || pos.outcome != pred.PredictedOutcome {
			// Close existing position first
			if pos != nil {
				curPrice := priceAtBar(bars, timeline[i].Timestamp, pos.outcome)
				t, newBal := closePosition(pos.outcome, pos.entryPrice, curPrice, pos.quantity, pos.entryTime, timeline[i].Timestamp, balance, "close", mktID)
				balance = newBal
				trades = append(trades, t)
				curve = append(curve, EquityPoint{Time: timeline[i].Timestamp, Balance: balance})
				pos = nil
			}

			// Open new position at next bar's open (avoid look-ahead)
			entryPrice := priceAtBar(bars, timeline[i+1].Timestamp, pred.PredictedOutcome)
			if entryPrice <= 0 || balance <= 0 {
				continue
			}
			// Apply slippage
			entryPrice *= (1 + e.cfg.SlippagePct)

			posValue := balance * e.cfg.MaxPositionPct
			qty := posValue / entryPrice

			pos = &openPosition{
				outcome:    pred.PredictedOutcome,
				entryPrice: entryPrice,
				quantity:   qty,
				entryTime:  timeline[i+1].Timestamp,
				entryBar:   i + 1,
			}
		}
	}

	// Force-close any open position at last bar
	if pos != nil && len(timeline) > 0 {
		last := timeline[len(timeline)-1]
		exitPrice := priceAtBar(bars, last.Timestamp, pos.outcome)
		if exitPrice <= 0 {
			exitPrice = pos.entryPrice // no data, break even
		}
		t, newBal := closePosition(pos.outcome, pos.entryPrice, exitPrice, pos.quantity, pos.entryTime, last.Timestamp, balance, "expired", mktID)
		balance = newBal
		trades = append(trades, t)
		curve = append(curve, EquityPoint{Time: last.Timestamp, Balance: balance})
	}

	return trades, balance, curve
}

// closePosition calculates PnL and returns a Trade + updated balance.
func closePosition(outcome string, entry, exit, qty float64, entryTime, exitTime time.Time, balance float64, reason, mktID string) (Trade, float64) {
	pnl := (exit - entry) * qty
	return Trade{
		MarketID:   mktID,
		Outcome:    outcome,
		EntryTime:  entryTime,
		ExitTime:   exitTime,
		EntryPrice: entry,
		ExitPrice:  exit,
		Quantity:   qty,
		PnL:        pnl,
		ExitReason: reason,
	}, balance + pnl
}

// ──────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────

// barsToSnapshots converts a window of bars into the ML snapshot format.
// Most-recent-first (index 0 = latest).
func barsToSnapshots(window []Bar, outcomes []string, allBars []Bar) []models.MarketSnapshot {
	snapshots := make([]models.MarketSnapshot, len(window))
	for i, b := range window {
		// Build outcome prices at this timestamp
		ops := make([]models.OutcomePrice, len(outcomes))
		for j, o := range outcomes {
			p := priceAtBar(allBars, b.Timestamp, o)
			ops[j] = models.OutcomePrice{
				Name:  o,
				Price: decimal.NewFromFloat(p),
			}
		}
		// Use nil UUID for backtest — market ID is tracked via Bar.MarketID (string)
		mktUUID, _ := uuid.Parse(b.MarketID)
		snapshots[len(window)-1-i] = models.MarketSnapshot{
			MarketID:      mktUUID,
			OutcomePrices: ops,
			Volume:        decimal.NewFromFloat(b.Volume),
			Liquidity:     decimal.NewFromFloat(b.Liquidity),
			CapturedAt:    b.Timestamp,
		}
	}
	return snapshots
}

// priceAtBar returns the price for a given outcome at a given timestamp.
// Falls back to the nearest previous bar if exact match not found.
func priceAtBar(bars []Bar, ts time.Time, outcome string) float64 {
	var best Bar
	for _, b := range bars {
		if b.Outcome != outcome {
			continue
		}
		if b.Timestamp.Equal(ts) {
			return b.Price
		}
		if b.Timestamp.Before(ts) && b.Timestamp.After(best.Timestamp) {
			best = b
		}
	}
	return best.Price
}

// ──────────────────────────────────────────
// Report builder
// ──────────────────────────────────────────

func buildReport(initial, final float64, trades []Trade, curve []EquityPoint) *Report {
	r := &Report{
		InitialBalance: initial,
		FinalBalance:   final,
		TotalPnL:       final - initial,
		TotalTrades:    len(trades),
		Trades:         trades,
		EquityCurve:    curve,
	}
	if initial > 0 {
		r.PnLPct = (r.TotalPnL / initial) * 100
	}

	var totalWin, totalLoss float64
	for _, t := range trades {
		if t.PnL > 0 {
			r.WinningTrades++
			totalWin += t.PnL
		} else {
			r.LosingTrades++
			totalLoss += math.Abs(t.PnL)
		}
	}
	if r.TotalTrades > 0 {
		r.WinRate = float64(r.WinningTrades) / float64(r.TotalTrades) * 100
	}
	if r.WinningTrades > 0 {
		r.AvgWin = totalWin / float64(r.WinningTrades)
	}
	if r.LosingTrades > 0 {
		r.AvgLoss = totalLoss / float64(r.LosingTrades)
	}
	if totalLoss > 0 {
		r.ProfitFactor = totalWin / totalLoss
	}

	r.MaxDrawdown = calcMaxDrawdown(curve)
	r.SharpeRatio = calcSharpe(curve)

	return r
}

func calcMaxDrawdown(curve []EquityPoint) float64 {
	if len(curve) < 2 {
		return 0
	}
	peak := curve[0].Balance
	maxDD := 0.0
	for _, p := range curve {
		if p.Balance > peak {
			peak = p.Balance
		}
		if peak > 0 {
			dd := (peak - p.Balance) / peak * 100
			if dd > maxDD {
				maxDD = dd
			}
		}
	}
	return maxDD
}

func calcSharpe(curve []EquityPoint) float64 {
	if len(curve) < 3 {
		return 0
	}
	// Daily returns
	var returns []float64
	for i := 1; i < len(curve); i++ {
		prev := curve[i-1].Balance
		if prev > 0 {
			returns = append(returns, (curve[i].Balance-prev)/prev)
		}
	}
	if len(returns) < 2 {
		return 0
	}
	// Mean and stddev of returns
	sum := 0.0
	for _, r := range returns {
		sum += r
	}
	avg := sum / float64(len(returns))

	variance := 0.0
	for _, r := range returns {
		d := r - avg
		variance += d * d
	}
	variance /= float64(len(returns) - 1)
	sd := math.Sqrt(variance)
	if sd == 0 {
		return 0
	}
	// Annualise assuming ~252 trading periods
	return (avg / sd) * math.Sqrt(252)
}

// ──────────────────────────────────────────
// CSV DataSource
// ──────────────────────────────────────────

// CSVDataSource loads bar data from a CSV file.
// Expected columns (header required):
//
//	timestamp,market_id,outcome,price,volume,liquidity
//
// timestamp format: RFC3339 (e.g. 2024-01-01T00:00:00Z)
type CSVDataSource struct {
	bars    []Bar
	markets []string
}

func NewCSVDataSource(path string) (*CSVDataSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("csv datasource: open %s: %w", path, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.TrimLeadingSpace = true

	// Read header
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("csv datasource: read header: %w", err)
	}
	idx := csvColumnIndex(header)

	var bars []Bar
	mktSet := map[string]struct{}{}

	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("csv datasource: read row: %w", err)
		}
		b, err := parseCSVRow(row, idx)
		if err != nil {
			continue // skip malformed rows
		}
		bars = append(bars, b)
		mktSet[b.MarketID] = struct{}{}
	}

	markets := make([]string, 0, len(mktSet))
	for m := range mktSet {
		markets = append(markets, m)
	}
	sort.Strings(markets)

	return &CSVDataSource{bars: bars, markets: markets}, nil
}

func (c *CSVDataSource) Markets() ([]string, error) {
	return c.markets, nil
}

func (c *CSVDataSource) Bars(marketID string) ([]Bar, error) {
	var result []Bar
	for _, b := range c.bars {
		if b.MarketID == marketID {
			result = append(result, b)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp.Before(result[j].Timestamp)
	})
	return result, nil
}

type colIdx struct{ ts, mkt, outcome, price, vol, liq int }

func csvColumnIndex(header []string) colIdx {
	idx := colIdx{-1, -1, -1, -1, -1, -1}
	for i, h := range header {
		switch h {
		case "timestamp":
			idx.ts = i
		case "market_id":
			idx.mkt = i
		case "outcome":
			idx.outcome = i
		case "price":
			idx.price = i
		case "volume":
			idx.vol = i
		case "liquidity":
			idx.liq = i
		}
	}
	return idx
}

func parseCSVRow(row []string, idx colIdx) (Bar, error) {
	get := func(i int) string {
		if i < 0 || i >= len(row) {
			return ""
		}
		return row[i]
	}

	ts, err := time.Parse(time.RFC3339, get(idx.ts))
	if err != nil {
		return Bar{}, err
	}
	price, err := strconv.ParseFloat(get(idx.price), 64)
	if err != nil {
		return Bar{}, err
	}
	vol, _ := strconv.ParseFloat(get(idx.vol), 64)
	liq, _ := strconv.ParseFloat(get(idx.liq), 64)

	return Bar{
		Timestamp: ts,
		MarketID:  get(idx.mkt),
		Outcome:   get(idx.outcome),
		Price:     price,
		Volume:    vol,
		Liquidity: liq,
	}, nil
}

// ──────────────────────────────────────────
// In-memory DataSource (for tests)
// ──────────────────────────────────────────

// MemDataSource holds bars in memory — useful for testing.
type MemDataSource struct {
	data    map[string][]Bar
	markets []string
}

func NewMemDataSource() *MemDataSource {
	return &MemDataSource{data: map[string][]Bar{}}
}

func (m *MemDataSource) AddBar(b Bar) {
	if _, ok := m.data[b.MarketID]; !ok {
		m.markets = append(m.markets, b.MarketID)
		sort.Strings(m.markets)
	}
	m.data[b.MarketID] = append(m.data[b.MarketID], b)
}

func (m *MemDataSource) Markets() ([]string, error) { return m.markets, nil }
func (m *MemDataSource) Bars(mktID string) ([]Bar, error) {
	bars := m.data[mktID]
	sort.Slice(bars, func(i, j int) bool { return bars[i].Timestamp.Before(bars[j].Timestamp) })
	return bars, nil
}
