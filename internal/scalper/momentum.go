package scalper

// Signal represents a momentum signal derived from order book analysis.
type Signal struct {
	Side       string  // "UP", "DOWN", "NONE"
	Confidence float64 // ratio distance from 0.5
	Price      float64 // entry price (BestAsk of the winning side)
}

// Analyze detects momentum from order book snapshots for the UP and DOWN tokens.
// threshold: e.g. 0.6 → bid ratio >= 0.6 means UP momentum.
func Analyze(snapUp, snapDown OrderBookSnapshot, threshold float64) Signal {
	totalUp := snapUp.BidDepth + snapUp.AskDepth
	if totalUp == 0 {
		return Signal{Side: "NONE"}
	}
	bidRatioUp := snapUp.BidDepth / totalUp

	if bidRatioUp >= threshold {
		return Signal{
			Side:       "UP",
			Confidence: bidRatioUp,
			Price:      snapUp.BestAsk,
		}
	}

	totalDown := snapDown.BidDepth + snapDown.AskDepth
	if totalDown == 0 {
		return Signal{Side: "NONE"}
	}
	bidRatioDown := snapDown.BidDepth / totalDown

	if bidRatioDown >= threshold {
		return Signal{
			Side:       "DOWN",
			Confidence: bidRatioDown,
			Price:      snapDown.BestAsk,
		}
	}

	return Signal{Side: "NONE"}
}
