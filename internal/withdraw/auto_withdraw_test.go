package withdraw

import (
	"math"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/x10rst/ai-agent-autonom/internal/config"
)

func newTestWithdrawer() *AutoWithdrawer {
	return &AutoWithdrawer{
		cfg: config.AutoWithdrawConfig{
			Enabled:              true,
			SafeWalletAddress:    "0x1234567890abcdef1234567890abcdef12345678",
			MinHotWalletBalance:  10.00,
			CheckIntervalSeconds: 300,
		},
		tradeCfg: config.TradingConfig{
			InitialBalance:    10.00,
			DailyProfitTarget: 0.25,
		},
	}
}

// --- Compounding Calculation Tests ---

func TestGetExpectedBalance_Day1(t *testing.T) {
	aw := newTestWithdrawer()
	expected := aw.getExpectedBalance(1)

	// Day 1: 10 * (1.25)^1 = 12.50
	want := decimal.NewFromFloat(12.50)
	if !expected.Equal(want) {
		t.Errorf("day 1 expected %s, got %s", want, expected)
	}
}

func TestGetExpectedBalance_Day10(t *testing.T) {
	aw := newTestWithdrawer()
	expected := aw.getExpectedBalance(10)

	// Day 10: 10 * (1.25)^10 = 93.13...
	want := 10.0 * math.Pow(1.25, 10)
	got, _ := expected.Float64()

	if math.Abs(got-want) > 0.01 {
		t.Errorf("day 10 expected %.2f, got %.2f", want, got)
	}
}

func TestGetExpectedBalance_Day30(t *testing.T) {
	aw := newTestWithdrawer()
	expected := aw.getExpectedBalance(30)

	// Day 30: 10 * (1.25)^30 ≈ 8077.94
	want := 10.0 * math.Pow(1.25, 30)
	got, _ := expected.Float64()

	if math.Abs(got-want) > 1.0 {
		t.Errorf("day 30 expected %.2f, got %.2f", want, got)
	}
}

func TestGetExpectedBalance_Day0(t *testing.T) {
	aw := newTestWithdrawer()
	expected := aw.getExpectedBalance(0)

	// Day 0: 10 * (1.25)^0 = 10.00
	want := decimal.NewFromFloat(10.00)
	if !expected.Equal(want) {
		t.Errorf("day 0 expected %s, got %s", want, expected)
	}
}

// --- Compounding Table Verification ---
// Verify against the PRD trading plan table

func TestCompoundingTable(t *testing.T) {
	aw := newTestWithdrawer()

	// Expected balances from the PRD 30-day plan (end of day = expected balance for next day)
	expectedBalances := map[int]float64{
		1:  12.50,
		2:  15.63,
		5:  30.51,
		10: 93.13,
		15: 284.22, // approximate
		20: 867.36, // approximate
		30: 8077.94, // approximate
	}

	for day, want := range expectedBalances {
		got, _ := aw.getExpectedBalance(day).Float64()
		tolerance := want * 0.02 // 2% tolerance for float precision

		if math.Abs(got-want) > tolerance {
			t.Errorf("day %d: expected ~%.2f, got %.2f (tolerance %.2f)", day, want, got, tolerance)
		}
	}
}

// --- USDC Conversion Test ---

func TestToUSDCUnits(t *testing.T) {
	amount := decimal.NewFromFloat(12.50)
	units := ToUSDCUnits(amount)

	// 12.50 USDC = 12500000 units (6 decimals)
	expected := int64(12500000)
	if units.Int64() != expected {
		t.Errorf("expected %d units, got %d", expected, units.Int64())
	}
}

func TestToUSDCUnits_SmallAmount(t *testing.T) {
	amount := decimal.NewFromFloat(0.01)
	units := ToUSDCUnits(amount)

	// 0.01 USDC = 10000 units
	expected := int64(10000)
	if units.Int64() != expected {
		t.Errorf("expected %d units, got %d", expected, units.Int64())
	}
}
