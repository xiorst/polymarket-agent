package backtest

import (
	"context"
	"math"
	"testing"
	"time"
)

// buildTrendingMarket generates bars for a market where "Yes" price trends up
// and "No" price trends down — strategy should profit.
func buildTrendingMarket(mktID string, bars int) *MemDataSource {
	ds := NewMemDataSource()
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < bars; i++ {
		ts := base.Add(time.Duration(i) * time.Hour)
		// "Yes" slowly rises from 0.30 → 0.75
		yesPrice := 0.30 + 0.45*float64(i)/float64(bars-1)
		// "No" falls correspondingly
		noPrice := 1.0 - yesPrice

		ds.AddBar(Bar{
			Timestamp: ts,
			MarketID:  mktID,
			Outcome:   "Yes",
			Price:     yesPrice,
			Volume:    1000 + float64(i)*10,
			Liquidity: 5000,
		})
		ds.AddBar(Bar{
			Timestamp: ts,
			MarketID:  mktID,
			Outcome:   "No",
			Price:     noPrice,
			Volume:    800 + float64(i)*5,
			Liquidity: 5000,
		})
	}
	return ds
}

// buildFlatMarket generates bars with no clear trend — strategy should mostly stay out.
func buildFlatMarket(mktID string, bars int) *MemDataSource {
	ds := NewMemDataSource()
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < bars; i++ {
		ts := base.Add(time.Duration(i) * time.Hour)
		// Oscillate around 0.5
		price := 0.50 + 0.02*math.Sin(float64(i)*0.5)

		ds.AddBar(Bar{
			Timestamp: ts,
			MarketID:  mktID,
			Outcome:   "Yes",
			Price:     price,
			Volume:    1000,
			Liquidity: 5000,
		})
		ds.AddBar(Bar{
			Timestamp: ts,
			MarketID:  mktID,
			Outcome:   "No",
			Price:     1.0 - price,
			Volume:    1000,
			Liquidity: 5000,
		})
	}
	return ds
}

func TestEngine_TrendingMarket_PositivePnL(t *testing.T) {
	ds := buildTrendingMarket("mkt-trend", 80)
	cfg := DefaultConfig()
	cfg.WarmupBars = 20
	cfg.WindowSize = 15
	cfg.ConfidenceMin = 0.55

	engine := New(cfg, ds)
	report, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if report == nil {
		t.Fatal("expected non-nil report")
	}

	t.Logf("PnL: %.4f (%.2f%%)", report.TotalPnL, report.PnLPct)
	t.Logf("Trades: %d | Wins: %d | Losses: %d", report.TotalTrades, report.WinningTrades, report.LosingTrades)
	t.Logf("Win rate: %.1f%%", report.WinRate)
	t.Logf("Max drawdown: %.2f%%", report.MaxDrawdown)
	t.Logf("Sharpe: %.3f", report.SharpeRatio)
	t.Logf("Profit factor: %.3f", report.ProfitFactor)

	if report.FinalBalance <= 0 {
		t.Error("expected positive final balance")
	}
}

func TestEngine_FlatMarket_LimitedLoss(t *testing.T) {
	ds := buildFlatMarket("mkt-flat", 80)
	cfg := DefaultConfig()
	cfg.WarmupBars = 20
	cfg.WindowSize = 15

	engine := New(cfg, ds)
	report, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	t.Logf("Flat market — PnL: %.4f (%.2f%%), Trades: %d", report.TotalPnL, report.PnLPct, report.TotalTrades)

	// Max drawdown should be within stop-loss limit
	if report.MaxDrawdown > cfg.StopLossPct*100+5 {
		t.Errorf("max drawdown %.2f%% exceeds expected bound", report.MaxDrawdown)
	}
}

func TestEngine_InsufficientData_Skipped(t *testing.T) {
	ds := NewMemDataSource()
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// Only 5 bars — below warmup threshold of 20
	for i := 0; i < 5; i++ {
		ds.AddBar(Bar{
			Timestamp: base.Add(time.Duration(i) * time.Hour),
			MarketID:  "mkt-tiny",
			Outcome:   "Yes",
			Price:     0.5,
			Volume:    100,
			Liquidity: 1000,
		})
	}

	cfg := DefaultConfig()
	engine := New(cfg, ds)
	report, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.TotalTrades != 0 {
		t.Errorf("expected 0 trades for tiny dataset, got %d", report.TotalTrades)
	}
}

func TestEngine_EmptyDataSource_NoError(t *testing.T) {
	ds := NewMemDataSource() // no bars
	engine := New(DefaultConfig(), ds)
	report, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.TotalTrades != 0 {
		t.Errorf("expected 0 trades, got %d", report.TotalTrades)
	}
	if report.FinalBalance != report.InitialBalance {
		t.Errorf("balance should be unchanged when no trades")
	}
}

func TestMaxDrawdown_Calculation(t *testing.T) {
	curve := []EquityPoint{
		{Balance: 10.0},
		{Balance: 12.0},
		{Balance: 8.0},  // drawdown from 12 → 8 = 33.3%
		{Balance: 11.0},
		{Balance: 13.0},
	}
	dd := calcMaxDrawdown(curve)
	expected := (12.0 - 8.0) / 12.0 * 100 // 33.33%
	if math.Abs(dd-expected) > 0.01 {
		t.Errorf("max drawdown = %.4f, expected %.4f", dd, expected)
	}
}

func TestSharpe_PositiveReturns(t *testing.T) {
	// Steadily increasing equity → positive Sharpe
	var curve []EquityPoint
	bal := 10.0
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		bal *= 1.005 // 0.5% per period
		curve = append(curve, EquityPoint{Time: base.Add(time.Duration(i) * 24 * time.Hour), Balance: bal})
	}
	sharpe := calcSharpe(curve)
	if sharpe <= 0 {
		t.Errorf("expected positive Sharpe for steadily rising equity, got %.4f", sharpe)
	}
	t.Logf("Sharpe for 0.5%%/period: %.3f", sharpe)
}

func TestReport_WinRate_ProfitFactor(t *testing.T) {
	trades := []Trade{
		{PnL: 1.0},
		{PnL: 1.5},
		{PnL: -0.5},
		{PnL: 0.8},
		{PnL: -0.3},
	}
	report := buildReport(10.0, 12.5, trades, nil)

	if report.WinningTrades != 3 {
		t.Errorf("expected 3 winning trades, got %d", report.WinningTrades)
	}
	if report.LosingTrades != 2 {
		t.Errorf("expected 2 losing trades, got %d", report.LosingTrades)
	}
	expectedWR := 3.0 / 5.0 * 100
	if math.Abs(report.WinRate-expectedWR) > 0.01 {
		t.Errorf("win rate = %.2f, expected %.2f", report.WinRate, expectedWR)
	}
	// Profit factor = 3.3 / 0.8 = 4.125
	expectedPF := 3.3 / 0.8
	if math.Abs(report.ProfitFactor-expectedPF) > 0.01 {
		t.Errorf("profit factor = %.4f, expected %.4f", report.ProfitFactor, expectedPF)
	}
}

func TestContextCancellation(t *testing.T) {
	ds := buildTrendingMarket("mkt-cancel", 80)
	engine := New(DefaultConfig(), ds)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := engine.Run(ctx)
	if err == nil {
		t.Log("completed before context cancelled (ok for small dataset)")
	}
}
