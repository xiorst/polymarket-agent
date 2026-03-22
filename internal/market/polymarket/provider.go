package polymarket

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/x10rst/ai-agent-autonom/internal/market"
	"github.com/x10rst/ai-agent-autonom/internal/models"
)

const (
	defaultCLOBBaseURL   = "https://clob.polymarket.com"
	defaultGammaBaseURL  = "https://gamma-api.polymarket.com"
)

// Provider implements the market.Provider interface for Polymarket.
type Provider struct {
	clobBaseURL  string
	gammaBaseURL string
	httpClient   *http.Client
	apiKey       string
}

type ProviderConfig struct {
	CLOBBaseURL  string
	GammaBaseURL string
	APIKey       string
	Timeout      time.Duration
}

func NewProvider(cfg ProviderConfig) *Provider {
	clobURL := cfg.CLOBBaseURL
	if clobURL == "" {
		clobURL = defaultCLOBBaseURL
	}
	gammaURL := cfg.GammaBaseURL
	if gammaURL == "" {
		gammaURL = defaultGammaBaseURL
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}

	return &Provider{
		clobBaseURL:  clobURL,
		gammaBaseURL: gammaURL,
		httpClient:   &http.Client{Timeout: timeout},
		apiKey:       cfg.APIKey,
	}
}

// FetchActiveMarkets returns all currently active markets from Polymarket Gamma API.
func (p *Provider) FetchActiveMarkets(ctx context.Context) ([]models.Market, error) {
	url := fmt.Sprintf("%s/markets?active=true&closed=false&limit=100", p.gammaBaseURL)

	body, err := p.doGet(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetch active markets: %w", err)
	}

	var apiMarkets []APIMarket
	if err := json.Unmarshal(body, &apiMarkets); err != nil {
		return nil, fmt.Errorf("parse markets response: %w", err)
	}

	var markets []models.Market
	for _, am := range apiMarkets {
		m, err := normalizeMarket(am)
		if err != nil {
			slog.Warn("skip market normalization", "condition_id", am.ConditionID, "error", err)
			continue
		}
		markets = append(markets, m)
	}

	slog.Debug("fetched polymarket markets", "count", len(markets))
	return markets, nil
}

// FetchMarketSnapshot returns the current snapshot for a specific market.
func (p *Provider) FetchMarketSnapshot(ctx context.Context, externalID string) (*models.MarketSnapshot, error) {
	url := fmt.Sprintf("%s/markets/%s", p.gammaBaseURL, externalID)

	body, err := p.doGet(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetch market snapshot: %w", err)
	}

	var am APIMarket
	if err := json.Unmarshal(body, &am); err != nil {
		return nil, fmt.Errorf("parse market snapshot: %w", err)
	}

	var outcomePrices []models.OutcomePrice
	for _, t := range am.Tokens {
		price, _ := decimal.NewFromString(t.Price)
		outcomePrices = append(outcomePrices, models.OutcomePrice{
			Name:    t.Outcome,
			Price:   price,
			TokenID: t.TokenID,
		})
	}

	volume, _ := strconv.ParseFloat(am.Volume, 64)
	liquidity, _ := strconv.ParseFloat(am.Liquidity, 64)

	snapshot := &models.MarketSnapshot{
		ID:            uuid.New(),
		OutcomePrices: outcomePrices,
		Volume:        decimal.NewFromFloat(volume),
		Liquidity:     decimal.NewFromFloat(liquidity),
		CapturedAt:    time.Now(),
	}

	return snapshot, nil
}

// FetchOrderBook returns the current order book for a market from the CLOB API.
func (p *Provider) FetchOrderBook(ctx context.Context, externalID string) (*market.OrderBook, error) {
	// First get the token IDs for this market
	url := fmt.Sprintf("%s/markets/%s", p.gammaBaseURL, externalID)
	body, err := p.doGet(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetch market for order book: %w", err)
	}

	var am APIMarket
	if err := json.Unmarshal(body, &am); err != nil {
		return nil, fmt.Errorf("parse market: %w", err)
	}

	if len(am.Tokens) == 0 {
		return nil, fmt.Errorf("market %s has no tokens", externalID)
	}

	// Fetch order book for the first token (Yes outcome typically)
	tokenID := am.Tokens[0].TokenID
	obURL := fmt.Sprintf("%s/book?token_id=%s", p.clobBaseURL, tokenID)

	obBody, err := p.doGet(ctx, obURL)
	if err != nil {
		return nil, fmt.Errorf("fetch order book: %w", err)
	}

	var apiOB APIOrderBook
	if err := json.Unmarshal(obBody, &apiOB); err != nil {
		return nil, fmt.Errorf("parse order book: %w", err)
	}

	ob := &market.OrderBook{
		MarketID: externalID,
	}

	for _, bid := range apiOB.Bids {
		price, _ := strconv.ParseFloat(bid.Price, 64)
		qty, _ := strconv.ParseFloat(bid.Size, 64)
		ob.Bids = append(ob.Bids, market.OrderBookEntry{Price: price, Quantity: qty})
	}

	for _, ask := range apiOB.Asks {
		price, _ := strconv.ParseFloat(ask.Price, 64)
		qty, _ := strconv.ParseFloat(ask.Size, 64)
		ob.Asks = append(ob.Asks, market.OrderBookEntry{Price: price, Quantity: qty})
	}

	// Calculate spread and mid price
	if len(ob.Bids) > 0 && len(ob.Asks) > 0 {
		bestBid := ob.Bids[0].Price
		bestAsk := ob.Asks[0].Price
		ob.Spread = bestAsk - bestBid
		ob.MidPrice = (bestBid + bestAsk) / 2
	}

	return ob, nil
}

func (p *Provider) doGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}

func normalizeMarket(am APIMarket) (models.Market, error) {
	endDate, err := time.Parse(time.RFC3339, am.EndDateISO)
	if err != nil {
		endDate = time.Now().Add(30 * 24 * time.Hour) // fallback
	}

	var status models.MarketStatus
	switch {
	case am.Closed:
		status = models.MarketStatusClosed
	case am.Active:
		status = models.MarketStatusActive
	default:
		status = models.MarketStatusClosed
	}

	var outcomes []models.OutcomePrice
	for _, t := range am.Tokens {
		price, _ := decimal.NewFromString(t.Price)
		outcomes = append(outcomes, models.OutcomePrice{
			Name:    t.Outcome,
			Price:   price,
			TokenID: t.TokenID,
		})
	}

	return models.Market{
		ID:         uuid.New(),
		ExternalID: am.ConditionID,
		Question:   am.Question,
		Outcomes:   outcomes,
		EndDate:    endDate,
		Status:     status,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}, nil
}
