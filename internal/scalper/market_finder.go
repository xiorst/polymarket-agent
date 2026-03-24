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
	Slug            string
	TokenIDUp       string
	TokenIDDown     string
	StartTime       time.Time
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

// gammaMarket is the Gamma API market response.
type gammaMarket struct {
	ID              string  `json:"id"`
	ConditionID     string  `json:"conditionId"`
	Slug            string  `json:"slug"`
	Question        string  `json:"question"`
	ClobTokenIDs    string  `json:"clobTokenIds"`
	EndDate         string  `json:"endDate"`
	StartDate       string  `json:"startDate"`
	AcceptingOrders bool    `json:"acceptingOrders"`
	OrderMinSize    float64 `json:"orderMinSize"`
	Active          bool    `json:"active"`
	Closed          bool    `json:"closed"`
}

// windowTimestamp returns the Unix timestamp for the current or next 5-minute window.
// offset=0 → current window, offset=1 → next window, offset=-1 → previous window.
func windowTimestamp(offset int) int64 {
	now := time.Now().Unix()
	current := (now / 300) * 300
	return current + int64(offset)*300
}

// slugForTimestamp returns the market slug for a given window timestamp.
func slugForTimestamp(ts int64) string {
	return fmt.Sprintf("btc-updown-5m-%d", ts)
}

// FindActive fetches the current active BTC 5m market using deterministic slug calculation.
// It tries current window first, then next window, then previous window.
func (f *MarketFinder) FindActive(ctx context.Context) (*ActiveMarket, error) {
	// Try offsets: current → next → next+1 (markets sometimes pre-created)
	offsets := []int{0, 1, 2, -1}

	for _, offset := range offsets {
		ts := windowTimestamp(offset)
		slug := slugForTimestamp(ts)

		market, err := f.fetchBySlug(ctx, slug)
		if err != nil {
			continue
		}
		if market == nil {
			continue
		}
		if !market.AcceptingOrders {
			continue
		}

		return market, nil
	}

	return nil, fmt.Errorf("no active BTC 5m market found (tried offsets %v)", offsets)
}

// fetchBySlug fetches a single market by slug from Gamma API.
func (f *MarketFinder) fetchBySlug(ctx context.Context, slug string) (*ActiveMarket, error) {
	url := fmt.Sprintf("https://gamma-api.polymarket.com/markets/slug/%s", slug)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gamma api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, nil // market belum exist untuk window ini
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gamma api status %d", resp.StatusCode)
	}

	var m gammaMarket
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode gamma response: %w", err)
	}

	// Validasi: harus Bitcoin market
	q := strings.ToLower(m.Question + m.Slug)
	if !strings.Contains(q, "bitcoin") && !strings.Contains(q, "btc") {
		return nil, nil
	}

	// Parse clobTokenIds
	var tokenIDs []string
	if err := json.Unmarshal([]byte(m.ClobTokenIDs), &tokenIDs); err != nil || len(tokenIDs) < 2 {
		return nil, fmt.Errorf("invalid clobTokenIds for %s", slug)
	}

	// Parse end date
	endTime, err := time.Parse(time.RFC3339, m.EndDate)
	if err != nil {
		endTime, err = time.Parse("2006-01-02T15:04:05Z", m.EndDate)
		if err != nil {
			return nil, fmt.Errorf("parse endDate: %w", err)
		}
	}

	// Skip expired
	if time.Now().UTC().After(endTime) {
		return nil, nil
	}

	// Parse start date
	startTime, _ := time.Parse(time.RFC3339, m.StartDate)

	return &ActiveMarket{
		ConditionID:     m.ConditionID,
		Slug:            m.Slug,
		TokenIDUp:       tokenIDs[0],
		TokenIDDown:     tokenIDs[1],
		StartTime:       startTime,
		EndTime:         endTime,
		AcceptingOrders: m.AcceptingOrders,
		MinOrderSize:    m.OrderMinSize,
	}, nil
}

// NextWindowDuration returns how long until the next 5-minute window starts.
func NextWindowDuration() time.Duration {
	now := time.Now().Unix()
	nextWindow := ((now / 300) + 1) * 300
	return time.Duration(nextWindow-now) * time.Second
}

// containsStr checks if s contains substr.
func containsStr(s, substr string) bool {
	return strings.Contains(s, substr)
}
