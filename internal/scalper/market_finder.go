package scalper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	httpClient *http.Client
	seriesSlug string
}

// NewMarketFinder creates a new MarketFinder.
func NewMarketFinder(cfg *Config) *MarketFinder {
	return &MarketFinder{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		seriesSlug: cfg.SeriesSlug,
	}
}

type gammaMarket struct {
	ConditionID     string  `json:"conditionId"`
	ClobTokenIDs    string  `json:"clobTokenIds"` // JSON string e.g. ["tokenUp","tokenDown"]
	EndDate         string  `json:"endDate"`
	AcceptingOrders bool    `json:"acceptingOrders"`
	OrderMinSize    float64 `json:"orderMinSize"`
	Active          bool    `json:"active"`
	Closed          bool    `json:"closed"`
}

// FindActive fetches the current active BTC 5m market.
func (f *MarketFinder) FindActive(ctx context.Context) (*ActiveMarket, error) {
	url := fmt.Sprintf(
		"https://gamma-api.polymarket.com/markets?series_slug=%s&active=true&closed=false&limit=5",
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

	var markets []gammaMarket
	if err := json.NewDecoder(resp.Body).Decode(&markets); err != nil {
		return nil, fmt.Errorf("decode gamma response: %w", err)
	}

	for _, m := range markets {
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
			// Try alternate format
			endTime, err = time.Parse("2006-01-02T15:04:05Z", m.EndDate)
			if err != nil {
				continue
			}
		}

		// Skip already-expired markets
		if time.Now().After(endTime) {
			continue
		}

		return &ActiveMarket{
			ConditionID:     m.ConditionID,
			TokenIDUp:       tokenIDs[0],
			TokenIDDown:     tokenIDs[1],
			EndTime:         endTime,
			AcceptingOrders: m.AcceptingOrders,
			MinOrderSize:    m.OrderMinSize,
		}, nil
	}

	return nil, fmt.Errorf("no active accepting-orders market found for series %q", f.seriesSlug)
}
