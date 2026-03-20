package ml

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/x10rst/ai-agent-autonom/internal/models"
)

// endDateFar is a market expiry far in the future (no expiry penalty).
var endDateFar = time.Now().Add(30 * 24 * time.Hour)

// --- Feature Extraction Tests ---

func TestExtractFeatures_EmptySnapshots(t *testing.T) {
	fs := ExtractFeatures(nil, 0, endDateFar)
	if fs.PriceMean != 0 {
		t.Errorf("expected 0 mean for empty, got %f", fs.PriceMean)
	}
}

func TestExtractFeatures_SingleSnapshot(t *testing.T) {
	snapshots := []models.MarketSnapshot{
		makeSnapshot(0.60, 1000, 500),
	}
	fs := ExtractFeatures(snapshots, 0, endDateFar)
	if fs.PriceMean != 0.60 {
		t.Errorf("expected mean 0.60, got %f", fs.PriceMean)
	}
	if fs.PriceStdDev != 0 {
		t.Errorf("expected stddev 0 for single point, got %f", fs.PriceStdDev)
	}
}

func TestExtractFeatures_UptrendDetection(t *testing.T) {
	snapshots := []models.MarketSnapshot{
		makeSnapshot(0.55, 1000, 500),
		makeSnapshot(0.50, 1000, 500),
		makeSnapshot(0.45, 900, 500),
		makeSnapshot(0.40, 800, 500),
		makeSnapshot(0.35, 700, 500),
		makeSnapshot(0.30, 600, 500),
	}
	fs := ExtractFeatures(snapshots, 0, endDateFar)
	if fs.PriceSlope <= 0 {
		t.Errorf("expected positive slope for uptrend, got %f", fs.PriceSlope)
	}
	if fs.PriceMomentum <= 1.0 {
		t.Errorf("expected momentum > 1 for uptrend, got %f", fs.PriceMomentum)
	}
}

func TestExtractFeatures_DowntrendDetection(t *testing.T) {
	snapshots := []models.MarketSnapshot{
		makeSnapshot(0.45, 1000, 500),
		makeSnapshot(0.50, 1000, 500),
		makeSnapshot(0.55, 900, 500),
		makeSnapshot(0.60, 800, 500),
		makeSnapshot(0.65, 700, 500),
		makeSnapshot(0.70, 600, 500),
	}
	fs := ExtractFeatures(snapshots, 0, endDateFar)
	if fs.PriceSlope >= 0 {
		t.Errorf("expected negative slope for downtrend, got %f", fs.PriceSlope)
	}
	if fs.PriceMomentum >= 1.0 {
		t.Errorf("expected momentum < 1 for downtrend, got %f", fs.PriceMomentum)
	}
}

func TestExtractFeatures_VolumeAcceleration(t *testing.T) {
	// VolumePerPeriod (delta) increasing over time — most recent first
	snapshots := []models.MarketSnapshot{
		makeSnapshotFull(0.50, 2000, 500, 200, 150, 0.02),
		makeSnapshotFull(0.50, 1800, 500, 180, 150, 0.02),
		makeSnapshotFull(0.50, 1500, 500, 150, 150, 0.02),
		makeSnapshotFull(0.50, 1200, 500, 120, 150, 0.02),
		makeSnapshotFull(0.50, 1000, 500, 100, 150, 0.02),
		makeSnapshotFull(0.50, 800,  500, 80,  150, 0.02),
	}
	fs := ExtractFeatures(snapshots, 0, endDateFar)
	if fs.VolumeAccel <= 1.0 {
		t.Errorf("expected volume accel > 1 for rising per-period volume, got %f", fs.VolumeAccel)
	}
}

func TestExtractFeatures_BidAskImbalance_BuyersPressure(t *testing.T) {
	// bid_depth >> ask_depth → buyers dominate
	snapshots := []models.MarketSnapshot{
		makeSnapshotFull(0.50, 1000, 500, 500, 100, 0.01),
		makeSnapshotFull(0.50, 1000, 500, 500, 100, 0.01),
		makeSnapshotFull(0.50, 1000, 500, 500, 100, 0.01),
	}
	fs := ExtractFeatures(snapshots, 0, endDateFar)
	if fs.BidAskImbalance <= 1.0 {
		t.Errorf("expected bid/ask imbalance > 1 when bids dominate, got %f", fs.BidAskImbalance)
	}
}

func TestExtractFeatures_TimeToExpiry(t *testing.T) {
	inFourHours := time.Now().Add(4 * time.Hour)
	snapshots := []models.MarketSnapshot{makeSnapshot(0.50, 1000, 500)}
	fs := ExtractFeatures(snapshots, 0, inFourHours)
	if fs.TimeToExpiry < 3 || fs.TimeToExpiry > 5 {
		t.Errorf("expected TimeToExpiry ~4h, got %f", fs.TimeToExpiry)
	}
}

// --- Statistical Helper Tests ---

func TestMean(t *testing.T) {
	tests := []struct {
		data     []float64
		expected float64
	}{
		{[]float64{1, 2, 3, 4, 5}, 3.0},
		{[]float64{10}, 10.0},
		{[]float64{}, 0.0},
		{[]float64{0.5, 0.5}, 0.5},
	}
	for _, tt := range tests {
		result := mean(tt.data)
		if math.Abs(result-tt.expected) > 0.0001 {
			t.Errorf("mean(%v) = %f, expected %f", tt.data, result, tt.expected)
		}
	}
}

func TestStdDev(t *testing.T) {
	data := []float64{2, 4, 4, 4, 5, 5, 7, 9}
	avg := mean(data)
	sd := stddev(data, avg)
	if math.Abs(sd-2.138) > 0.01 {
		t.Errorf("stddev = %f, expected ~2.138", sd)
	}
}

func TestLinearSlope_Positive(t *testing.T) {
	data := []float64{1, 2, 3, 4, 5}
	if slope := linearSlope(data); slope != 1.0 {
		t.Errorf("expected slope 1.0, got %f", slope)
	}
}

func TestLinearSlope_Flat(t *testing.T) {
	data := []float64{5, 5, 5, 5}
	if slope := linearSlope(data); slope != 0 {
		t.Errorf("expected slope 0, got %f", slope)
	}
}

func TestLinearSlope_Negative(t *testing.T) {
	data := []float64{5, 4, 3, 2, 1}
	if slope := linearSlope(data); slope != -1.0 {
		t.Errorf("expected slope -1.0, got %f", slope)
	}
}

// --- Predictor Scoring Tests ---

func TestPredictor_HighConfidenceUptrend(t *testing.T) {
	predictor := NewDefaultPredictor()
	snapshots := []models.MarketSnapshot{
		makeSnapshotTwo("Yes", 0.70, "No", 0.30, 2000, 800),
		makeSnapshotTwo("Yes", 0.65, "No", 0.35, 1800, 750),
		makeSnapshotTwo("Yes", 0.60, "No", 0.40, 1600, 700),
		makeSnapshotTwo("Yes", 0.55, "No", 0.45, 1400, 650),
		makeSnapshotTwo("Yes", 0.50, "No", 0.50, 1200, 600),
		makeSnapshotTwo("Yes", 0.45, "No", 0.55, 1000, 550),
	}
	prediction, err := predictor.Predict(context.Background(), "test-market", snapshots)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prediction == nil {
		t.Fatal("expected prediction, got nil")
	}
	if prediction.PredictedOutcome != "Yes" {
		t.Errorf("expected predicted outcome 'Yes', got %q", prediction.PredictedOutcome)
	}
	if prediction.Confidence < 0.5 {
		t.Errorf("expected confidence > 0.5 for strong uptrend, got %f", prediction.Confidence)
	}
}

func TestPredictor_ReturnsNilForNoData(t *testing.T) {
	predictor := NewDefaultPredictor()
	prediction, err := predictor.Predict(context.Background(), "test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prediction != nil {
		t.Error("expected nil prediction for no data")
	}
}

func TestPredictor_ConfidenceBounds(t *testing.T) {
	predictor := NewDefaultPredictor()
	snapshots := []models.MarketSnapshot{
		makeSnapshot(0.50, 1000, 500),
		makeSnapshot(0.50, 1000, 500),
		makeSnapshot(0.50, 1000, 500),
		makeSnapshot(0.50, 1000, 500),
		makeSnapshot(0.50, 1000, 500),
	}
	prediction, _ := predictor.Predict(context.Background(), "test", snapshots)
	if prediction == nil {
		t.Fatal("expected prediction")
	}
	if prediction.Confidence < 0 || prediction.Confidence > 1 {
		t.Errorf("confidence %f out of bounds [0, 1]", prediction.Confidence)
	}
}

func TestPredictor_ExpiryPenalty_NearExpiry(t *testing.T) {
	predictor := NewDefaultPredictor()
	// Market expiring in 2 hours — confidence should be capped at 0.55
	endDate := time.Now().Add(2 * time.Hour)

	snapshots := make([]models.MarketSnapshot, 6)
	for i := range snapshots {
		snapshots[i] = makeSnapshot(0.80, 5000, 2000) // very strong signal
	}

	prediction, err := predictor.PredictWithEndDate(context.Background(), "expiring", snapshots, endDate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prediction == nil {
		t.Fatal("expected prediction")
	}
	if prediction.Confidence > 0.55+0.001 {
		t.Errorf("expected confidence capped at 0.55 for near-expiry market, got %f", prediction.Confidence)
	}
}

func TestPredictor_ExpiryPenalty_FarExpiry(t *testing.T) {
	predictor := NewDefaultPredictor()
	endDate := time.Now().Add(7 * 24 * time.Hour) // 7 days away

	snapshots := []models.MarketSnapshot{
		makeSnapshotTwo("Yes", 0.80, "No", 0.20, 3000, 1000),
		makeSnapshotTwo("Yes", 0.75, "No", 0.25, 2800, 950),
		makeSnapshotTwo("Yes", 0.70, "No", 0.30, 2600, 900),
		makeSnapshotTwo("Yes", 0.65, "No", 0.35, 2400, 850),
		makeSnapshotTwo("Yes", 0.60, "No", 0.40, 2200, 800),
		makeSnapshotTwo("Yes", 0.55, "No", 0.45, 2000, 750),
	}

	prediction, err := predictor.PredictWithEndDate(context.Background(), "far-expiry", snapshots, endDate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prediction == nil {
		t.Fatal("expected prediction")
	}
	// No penalty: confidence should reflect the strong uptrend
	if prediction.Confidence < 0.5 {
		t.Errorf("expected higher confidence for far-expiry strong uptrend, got %f", prediction.Confidence)
	}
}

// --- Idempotency Key Tests ---

func TestGenerateIdempotencyKey_Deterministic(t *testing.T) {
	marketID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	ts := time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC)

	key1 := GenerateIdempotencyKey(marketID, "Yes", ts)
	key2 := GenerateIdempotencyKey(marketID, "Yes", ts)
	if key1 != key2 {
		t.Errorf("expected deterministic key, got %s vs %s", key1, key2)
	}
}

func TestGenerateIdempotencyKey_DifferentInputs(t *testing.T) {
	marketID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	ts := time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC)

	key1 := GenerateIdempotencyKey(marketID, "Yes", ts)
	key2 := GenerateIdempotencyKey(marketID, "No", ts)
	if key1 == key2 {
		t.Error("expected different keys for different outcomes")
	}
}

// --- Helpers ---

func makeSnapshot(price, volume, liquidity float64) models.MarketSnapshot {
	return models.MarketSnapshot{
		ID:       uuid.New(),
		MarketID: uuid.New(),
		OutcomePrices: []models.OutcomePrice{
			{Name: "Yes", Price: decimal.NewFromFloat(price)},
		},
		Volume:          decimal.NewFromFloat(volume),
		VolumePerPeriod: decimal.NewFromFloat(volume / 10), // simulate delta
		Liquidity:       decimal.NewFromFloat(liquidity),
		BidDepth:        decimal.NewFromFloat(liquidity / 2),
		AskDepth:        decimal.NewFromFloat(liquidity / 2),
		Spread:          decimal.NewFromFloat(0.02),
		CapturedAt:      time.Now(),
	}
}

func makeSnapshotFull(price, volume, liquidity, bidDepth, askDepth, spread float64) models.MarketSnapshot {
	return models.MarketSnapshot{
		ID:       uuid.New(),
		MarketID: uuid.New(),
		OutcomePrices: []models.OutcomePrice{
			{Name: "Yes", Price: decimal.NewFromFloat(price)},
		},
		Volume:          decimal.NewFromFloat(volume),
		VolumePerPeriod: decimal.NewFromFloat(volume / 10),
		Liquidity:       decimal.NewFromFloat(liquidity),
		BidDepth:        decimal.NewFromFloat(bidDepth),
		AskDepth:        decimal.NewFromFloat(askDepth),
		Spread:          decimal.NewFromFloat(spread),
		CapturedAt:      time.Now(),
	}
}

func makeSnapshotTwo(name1 string, price1 float64, name2 string, price2, volume, liquidity float64) models.MarketSnapshot {
	return models.MarketSnapshot{
		ID:       uuid.New(),
		MarketID: uuid.New(),
		OutcomePrices: []models.OutcomePrice{
			{Name: name1, Price: decimal.NewFromFloat(price1)},
			{Name: name2, Price: decimal.NewFromFloat(price2)},
		},
		Volume:          decimal.NewFromFloat(volume),
		VolumePerPeriod: decimal.NewFromFloat(volume / 10),
		Liquidity:       decimal.NewFromFloat(liquidity),
		BidDepth:        decimal.NewFromFloat(liquidity / 2),
		AskDepth:        decimal.NewFromFloat(liquidity / 2),
		Spread:          decimal.NewFromFloat(0.02),
		CapturedAt:      time.Now(),
	}
}
