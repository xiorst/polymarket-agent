package scalper

// Signal represents a momentum signal derived from order book analysis.
type Signal struct {
	Side           string  // "UP", "DOWN", "NONE"
	Confidence     float64 // imbalance ratio (0.5–1.0)
	Price          float64 // entry price (BestAsk of the winning side)
	ImbalanceRatio float64 // raw ratio for logging
}

// Analyze detects momentum via cross-token imbalance ratio.
//
// Logika:
//   - upPressure  = Up.BidDepth  / (Up.BidDepth  + Down.BidDepth)
//   - downPressure = Down.BidDepth / (Up.BidDepth + Down.BidDepth)
//
// Kalau upPressure >= threshold (default 0.65) → banyak orang mau beli UP → signal UP
// Kalau downPressure >= threshold → signal DOWN
//
// Tambahan filter:
//   - Total liquidity (semua depth) >= minLiquidity → hindari pasar terlalu tipis
//   - BestAsk harus > 0 (ada seller) sebelum entry
func Analyze(snapUp, snapDown OrderBookSnapshot, threshold float64) Signal {
	const minLiquidity = 50.0 // minimum $50 total depth sebelum signal valid

	totalDepth := snapUp.BidDepth + snapUp.AskDepth + snapDown.BidDepth + snapDown.AskDepth
	if totalDepth < minLiquidity {
		return Signal{Side: "NONE"}
	}

	totalBid := snapUp.BidDepth + snapDown.BidDepth
	if totalBid == 0 {
		return Signal{Side: "NONE"}
	}

	upPressure := snapUp.BidDepth / totalBid
	downPressure := snapDown.BidDepth / totalBid

	// UP signal
	if upPressure >= threshold && snapUp.BestAsk > 0 {
		return Signal{
			Side:           "UP",
			Confidence:     upPressure,
			Price:          snapUp.BestAsk,
			ImbalanceRatio: upPressure,
		}
	}

	// DOWN signal
	if downPressure >= threshold && snapDown.BestAsk > 0 {
		return Signal{
			Side:           "DOWN",
			Confidence:     downPressure,
			Price:          snapDown.BestAsk,
			ImbalanceRatio: downPressure,
		}
	}

	return Signal{Side: "NONE", ImbalanceRatio: upPressure}
}
