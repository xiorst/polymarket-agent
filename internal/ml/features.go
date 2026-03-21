package ml

import (
	"math"
	"time"

	"github.com/shopspring/decimal"
	"github.com/x10rst/ai-agent-autonom/internal/feeds/scorer"
	"github.com/x10rst/ai-agent-autonom/internal/models"
)

// FeatureSet represents extracted statistical features from market snapshot history.
type FeatureSet struct {
	// Price trend
	PriceMean     float64 // Average price over window
	PriceStdDev   float64 // Volatility
	PriceMomentum float64 // Recent avg / older avg — > 1 = upward momentum
	PriceSlope    float64 // Linear regression slope (trend direction & speed)

	// Volume — uses VolumePerPeriod (delta), not cumulative
	VolumeMean  float64 // Average per-period volume
	VolumeAccel float64 // Recent volume / older volume — > 1 = accelerating

	// Liquidity
	LiquidityMean  float64
	LiquidityTrend float64 // Slope of liquidity over time

	// Order book depth
	BidAskImbalance float64 // bid_depth / ask_depth — > 1 = more buyers
	SpreadMean      float64 // Average spread (lower = tighter, healthier)

	// Time
	TimeToExpiry float64 // Hours until market end_date — affects confidence penalty

	// External context — signals from Telegram news feed
	// Nil if no relevant news found for this market's category.
	ExternalSignal *scorer.ExternalSignal
}

// ExtractFeatures computes statistical features from a series of market snapshots.
// Snapshots must be ordered most-recent-first (as returned by DB ORDER BY captured_at DESC).
// marketEndDate is used to compute TimeToExpiry.
func ExtractFeatures(snapshots []models.MarketSnapshot, outcomeIdx int, marketEndDate time.Time) FeatureSet {
	if len(snapshots) == 0 {
		return FeatureSet{}
	}

	n := len(snapshots)

	// Reverse to chronological order for time-series computation
	prices := make([]float64, n)
	volumesPerPeriod := make([]float64, n)
	liquidities := make([]float64, n)
	bidDepths := make([]float64, n)
	askDepths := make([]float64, n)
	spreads := make([]float64, n)

	for i, s := range snapshots {
		idx := n - 1 - i
		if outcomeIdx < len(s.OutcomePrices) {
			prices[idx], _ = s.OutcomePrices[outcomeIdx].Price.Float64()
		}
		volumesPerPeriod[idx], _ = s.VolumePerPeriod.Float64()
		liquidities[idx], _ = s.Liquidity.Float64()
		bidDepths[idx], _ = s.BidDepth.Float64()
		askDepths[idx], _ = s.AskDepth.Float64()
		spreads[idx], _ = s.Spread.Float64()
	}

	fs := FeatureSet{}

	// Price features
	fs.PriceMean = mean(prices)
	fs.PriceStdDev = stddev(prices, fs.PriceMean)
	fs.PriceMomentum = momentum(prices)
	fs.PriceSlope = linearSlope(prices)

	// Volume features — using per-period delta, not cumulative
	fs.VolumeMean = mean(volumesPerPeriod)
	fs.VolumeAccel = momentum(volumesPerPeriod)

	// Liquidity features
	fs.LiquidityMean = mean(liquidities)
	fs.LiquidityTrend = linearSlope(liquidities)

	// Order book features
	avgBid := mean(bidDepths)
	avgAsk := mean(askDepths)
	if avgAsk > 0 {
		fs.BidAskImbalance = avgBid / avgAsk
	} else {
		fs.BidAskImbalance = 1.0
	}
	fs.SpreadMean = mean(spreads)

	// Time to expiry
	fs.TimeToExpiry = time.Until(marketEndDate).Hours()

	// External signal — taken from the most recent snapshot (index 0 = latest)
	// Pipeline injects this before calling ExtractFeatures
	if len(snapshots) > 0 && snapshots[0].ExternalSignal != nil {
		if sig, ok := snapshots[0].ExternalSignal.(*scorer.ExternalSignal); ok {
			fs.ExternalSignal = sig
		}
	}

	return fs
}

// PriceFromOutcomes extracts the price for a specific outcome index.
func PriceFromOutcomes(outcomes []models.OutcomePrice, idx int) float64 {
	if idx < len(outcomes) {
		f, _ := outcomes[idx].Price.Float64()
		return f
	}
	return 0
}

// DecimalToFloat converts a shopspring decimal to float64.
func DecimalToFloat(d decimal.Decimal) float64 {
	f, _ := d.Float64()
	return f
}

// --- Statistical helpers ---

func mean(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range data {
		sum += v
	}
	return sum / float64(len(data))
}

func stddev(data []float64, avg float64) float64 {
	if len(data) < 2 {
		return 0
	}
	sumSq := 0.0
	for _, v := range data {
		diff := v - avg
		sumSq += diff * diff
	}
	return math.Sqrt(sumSq / float64(len(data)-1))
}

// momentum: ratio of recent half average to older half average.
// > 1 = upward momentum, < 1 = downward.
func momentum(data []float64) float64 {
	if len(data) < 4 {
		return 1.0
	}
	mid := len(data) / 2
	recentAvg := mean(data[mid:])
	olderAvg := mean(data[:mid])
	if olderAvg == 0 {
		return 1.0
	}
	return recentAvg / olderAvg
}

// linearSlope returns the slope b of linear regression y = a + b*x.
func linearSlope(data []float64) float64 {
	n := float64(len(data))
	if n < 2 {
		return 0
	}
	var sumX, sumY, sumXY, sumX2 float64
	for i, y := range data {
		x := float64(i)
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
	}
	denom := n*sumX2 - sumX*sumX
	if denom == 0 {
		return 0
	}
	return (n*sumXY - sumX*sumY) / denom
}
