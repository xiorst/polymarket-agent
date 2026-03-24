package scalper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ActiveMarket represents a currently active Polymarket market.
type ActiveMarket struct {
	ConditionID     string
	TokenIDUp       string
	TokenIDDown     string
	EndTime         time.Time
	AcceptingOrders bool
	MinOrderSize    float64
}

// MarketFinder fetches active markets from the Gamma API.
type MarketFinder struct {
	httpClient    *http.Client
	seriesSlug    string
}

// NewMarketFinder creates a new MarketFinder.
func NewMarketFinder(cfg *Config) *MarketFinder {
	return &MarketFinder{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		seriesSlug: cfg.SeriesSlug,
	}
}

// gammaMarket is the nested market inside a Gamma event.
type gammaMarket struct {
	ConditionID     string  `json:"conditionId"`
	ClobTokenIDs    string  `json:"clobTokenIds"` // JSON string e.g. ["tokenUp","tokenDown"]
	EndDate         string  `json:"endDate"`
	AcceptingOrders bool    `json:"acceptingOrders"`
	OrderMinSize    float64 `json:"orderMinSize"`
	Question        string  `json:"question"`
}

// gammaEvent is the Gamma API event response wrapper.
type gammaEvent struct {
	Title     string        `json:"title"`
	EndDate   string        `json:"endDate"`
	StartTime string        `json:"startTime"`
	Active    bool          `json:"active"`
	Closed    bool          `json:"closed"`
	Markets   []gammaMarket `json:"markets"`
}

// containsStr checks if s contains substr.
func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && strings.Contains(s, substr))
}

// FindActive fetches the current active BTC 5m market via Gamma events endpoint.
func (f *MarketFinder) FindActive(ctx context.Context) (*ActiveMarket, error) {
	// Use events endpoint with series_ticker filter — this is the correct way to
	// find rolling 5m markets. series_slug on /markets does not filter correctly.
	url := fmt.Sprintf(
		"https://gamma-api.polymarket.com/events?series_ticker=%s&active=true&closed=false&limit=10&order=startDate&ascending=false",
		f.seriesSlug,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gamma api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gamma api status %d", resp.StatusCode)
	}

	var events []gammaEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, fmt.Errorf("decode gamma response: %w", err)
	}

	now := time.Now().UTC()

	for _, e := range events {
		if e.Closed {
			continue
		}

		// Parse event start time for duration check
		startTime, err := time.Parse(time.RFC3339, e.StartTime)
		if err != nil {
			startTime, _ = time.Parse("2006-01-02T15:04:05Z", e.StartTime)
		}

		for _, m := range e.Markets {
			if !m.AcceptingOrders {
				continue
			}

			// Parse clobTokenIds — JSON string like `["tokenUp", "tokenDown"]`
			var tokenIDs []string
			if err := json.Unmarshal([]byte(m.ClobTokenIDs), &tokenIDs); err != nil {
				continue
			}
			if len(tokenIDs) < 2 {
				continue
			}

			// Parse end date
			endTime, err := time.Parse(time.RFC3339, m.EndDate)
			if err != nil {
				endTime, err = time.Parse("2006-01-02T15:04:05Z", m.EndDate)
				if err != nil {
					continue
				}
			}

			// Skip already-expired markets
			if now.After(endTime) {
				continue
			}

			// Filter: must be a short-duration market (≤ 10 minutes)
			if !startTime.IsZero() {
				duration := endTime.Sub(startTime)
				if duration > 10*time.Minute {
					continue
				}
			}

			// Filter: must be BTC/Bitcoin market (not ETH, SOL, DOGE, etc.)
			title := e.Title
			if title == "" {
				title = m.Question
			}
			isBTC := false
			for _, kw := range []string{"Bitcoin", "BTC", "bitcoin", "btc"} {
				if len(title) >= len(kw) && containsStr(title, kw) {
					isBTC = true
					break
				}
			}
			if !isBTC {
				continue
			}

			return &ActiveMarket{
				ConditionID:     m.ConditionID,
				TokenIDUp:       tokenIDs[0],
				TokenIDDown:     tokenIDs[1],
				EndTime:         endTime,
				AcceptingOrders: true,
				MinOrderSize:    m.OrderMinSize,
			}, nil
		}
	}

	return nil, fmt.Errorf("no active BTC 5m market found for series %q", f.seriesSlug)
}
