package ml

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/x10rst/ai-agent-autonom/internal/models"
)

// StatisticalPredictor is a Go-native ML predictor using feature-based scoring.
//
// Combines 7 signals into a composite confidence score [0, 1]:
//   - Price momentum, slope, mean reversion
//   - Volume acceleration (per-period, not cumulative)
//   - Liquidity trend
//   - Order book bid/ask imbalance
//   - Time-to-expiry penalty
//
// Can be replaced with a Python sidecar (gRPC/HTTP) or external ML API
// by implementing the Predictor interface.
type StatisticalPredictor struct {
	weights PredictorWeights
}

type PredictorWeights struct {
	Momentum        float64
	Slope           float64
	VolumeAccel     float64
	LiquidityTrend  float64
	MeanReversion   float64
	BidAskImbalance float64
	SpreadPenalty   float64
}

func DefaultWeights() PredictorWeights {
	return PredictorWeights{
		Momentum:        0.25, // price trend continuation
		Slope:           0.20, // trend direction & strength
		VolumeAccel:     0.15, // trading activity confirmation
		LiquidityTrend:  0.10, // market health
		MeanReversion:   0.10, // contrarian value
		BidAskImbalance: 0.15, // order book buyer/seller pressure
		SpreadPenalty:   0.05, // spread cost drag
	}
}

func NewStatisticalPredictor(weights PredictorWeights) *StatisticalPredictor {
	return &StatisticalPredictor{weights: weights}
}

func NewDefaultPredictor() *StatisticalPredictor {
	return NewStatisticalPredictor(DefaultWeights())
}

// Predict analyzes market snapshot history and returns the best outcome prediction.
// Snapshots must be ordered most-recent-first. marketEndDate is required for
// the time-to-expiry penalty.
func (sp *StatisticalPredictor) Predict(
	_ context.Context,
	marketID string,
	snapshots []models.MarketSnapshot,
) (*Prediction, error) {
	if len(snapshots) == 0 {
		return nil, nil
	}

	latestSnapshot := snapshots[0]
	outcomeCount := len(latestSnapshot.OutcomePrices)
	if outcomeCount == 0 {
		return nil, nil
	}

	// Use the most recent snapshot's CapturedAt as reference for end date lookup.
	// The pipeline passes market.EndDate separately via PredictWithEndDate.
	// Default fallback: 30 days from latest snapshot.
	marketEndDate := latestSnapshot.CapturedAt.Add(30 * 24 * time.Hour)

	return sp.predict(marketID, snapshots, marketEndDate)
}

// PredictWithEndDate is the full prediction call with end date for accurate expiry penalty.
func (sp *StatisticalPredictor) PredictWithEndDate(
	_ context.Context,
	marketID string,
	snapshots []models.MarketSnapshot,
	marketEndDate time.Time,
) (*Prediction, error) {
	if len(snapshots) == 0 {
		return nil, nil
	}
	latestSnapshot := snapshots[0]
	if len(latestSnapshot.OutcomePrices) == 0 {
		return nil, nil
	}
	return sp.predict(marketID, snapshots, marketEndDate)
}

func (sp *StatisticalPredictor) predict(
	marketID string,
	snapshots []models.MarketSnapshot,
	marketEndDate time.Time,
) (*Prediction, error) {
	latestSnapshot := snapshots[0]
	outcomeCount := len(latestSnapshot.OutcomePrices)

	bestOutcome := ""
	bestConfidence := 0.0

	for i := 0; i < outcomeCount; i++ {
		features := ExtractFeatures(snapshots, i, marketEndDate)
		currentPrice := PriceFromOutcomes(latestSnapshot.OutcomePrices, i)
		outcomeName := latestSnapshot.OutcomePrices[i].Name

		confidence := sp.score(features, currentPrice)

		slog.Debug("ml prediction score",
			"market", marketID,
			"outcome", outcomeName,
			"confidence", confidence,
			"momentum", features.PriceMomentum,
			"slope", features.PriceSlope,
			"vol_accel", features.VolumeAccel,
			"liq_trend", features.LiquidityTrend,
			"bid_ask_imbalance", features.BidAskImbalance,
			"spread_mean", features.SpreadMean,
			"time_to_expiry_h", features.TimeToExpiry,
			"price", currentPrice,
		)

		if confidence > bestConfidence {
			bestConfidence = confidence
			bestOutcome = outcomeName
		}
	}

	return &Prediction{
		MarketID:         marketID,
		PredictedOutcome: bestOutcome,
		Confidence:       bestConfidence,
	}, nil
}

// score computes a composite confidence score from features, returning [0, 1].
func (sp *StatisticalPredictor) score(f FeatureSet, currentPrice float64) float64 {
	w := sp.weights

	// Signal 1: Price Momentum
	// Momentum > 1 = recent prices higher than older → bullish
	momentumSignal := sigmoid((f.PriceMomentum - 1.0) * 10)

	// Signal 2: Price Slope (linear trend)
	// Positive slope = uptrend. 0.01/period is significant in prediction markets.
	slopeSignal := sigmoid(f.PriceSlope * 100)

	// Signal 3: Volume Acceleration (per-period delta, not cumulative)
	// > 1 = trading activity increasing → confirms trend
	volumeSignal := sigmoid((f.VolumeAccel - 1.0) * 5)

	// Signal 4: Liquidity Trend
	// Improving liquidity = market getting healthier
	liquiditySignal := sigmoid(f.LiquidityTrend * 50)

	// Signal 5: Mean Reversion
	// Price well below mean → contrarian buy signal
	meanRevSignal := 0.5
	if f.PriceMean > 0 {
		deviation := (f.PriceMean - currentPrice) / f.PriceMean
		meanRevSignal = sigmoid(deviation * 5)
	}

	// Signal 6: Order Book Bid/Ask Imbalance
	// > 1 = more buying pressure, < 1 = more selling pressure
	// Center at 1.0 → sigmoid gives 0.5 for balanced book
	imbalanceSignal := sigmoid((f.BidAskImbalance - 1.0) * 3)

	// Signal 7: Spread Penalty
	// Wide spread = high transaction cost + thin liquidity → reduce confidence
	// A spread of 0.05 (5 cents) in a 0-1 market is very wide
	spreadPenalty := sigmoid(-f.SpreadMean * 20) // negative: higher spread → lower score

	// Composite weighted score
	composite := w.Momentum*momentumSignal +
		w.Slope*slopeSignal +
		w.VolumeAccel*volumeSignal +
		w.LiquidityTrend*liquiditySignal +
		w.MeanReversion*meanRevSignal +
		w.BidAskImbalance*imbalanceSignal +
		w.SpreadPenalty*spreadPenalty

	// Time-to-expiry penalty:
	// Markets expiring in < 6 hours are highly uncertain — cap confidence.
	// Markets expiring in > 72 hours: no penalty.
	// This prevents the agent from taking positions in nearly-expired markets.
	if f.TimeToExpiry > 0 {
		composite = applyExpiryPenalty(composite, f.TimeToExpiry)
	}

	return clamp(composite, 0, 1)
}

// applyExpiryPenalty reduces confidence for markets close to expiration.
//
//	< 6h  → max confidence 0.55 (too risky, prices are erratic)
//	6-24h → gradual reduction
//	> 72h → no penalty
func applyExpiryPenalty(score, hoursLeft float64) float64 {
	switch {
	case hoursLeft < 6:
		return math.Min(score, 0.55)
	case hoursLeft < 24:
		// Linear scale from 0.55 (at 6h) to full score (at 24h)
		ratio := (hoursLeft - 6) / (24 - 6) // 0→1
		cap := 0.55 + ratio*(1.0-0.55)
		return math.Min(score, cap)
	default:
		return score
	}
}

// sigmoid maps any real value to (0, 1).
func sigmoid(x float64) float64 {
	return 1.0 / (1.0 + math.Exp(-x))
}

func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
