package market

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/x10rst/ai-agent-autonom/internal/config"
	"github.com/x10rst/ai-agent-autonom/internal/models"
)

// Provider is the interface for market data sources (Polymarket, etc).
type Provider interface {
	FetchActiveMarkets(ctx context.Context) ([]models.Market, error)
	FetchMarketSnapshot(ctx context.Context, externalID string) (*models.MarketSnapshot, error)
	FetchOrderBook(ctx context.Context, externalID string) (*OrderBook, error)
}

// OrderBook represents the current order book for a market.
type OrderBook struct {
	MarketID string
	Bids     []OrderBookEntry
	Asks     []OrderBookEntry
	Spread   float64
	MidPrice float64
}

type OrderBookEntry struct {
	Price    float64
	Quantity float64
}

// TotalBidDepth sums all quantity on the bid side.
func (ob *OrderBook) TotalBidDepth() float64 {
	total := 0.0
	for _, b := range ob.Bids {
		total += b.Quantity
	}
	return total
}

// TotalAskDepth sums all quantity on the ask side.
func (ob *OrderBook) TotalAskDepth() float64 {
	total := 0.0
	for _, a := range ob.Asks {
		total += a.Quantity
	}
	return total
}

// Collector periodically fetches market data and stores enriched snapshots.
type Collector struct {
	cfg      config.MarketAnalysisConfig
	db       *pgxpool.Pool
	provider Provider
}

func NewCollector(cfg config.MarketAnalysisConfig, db *pgxpool.Pool, provider Provider) *Collector {
	return &Collector{cfg: cfg, db: db, provider: provider}
}

// Run starts the market data collection loop.
func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(c.cfg.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()

	slog.Info("market collector started", "interval_seconds", c.cfg.PollIntervalSeconds)

	c.collect(ctx) // run immediately on start

	for {
		select {
		case <-ctx.Done():
			slog.Info("market collector stopped")
			return
		case <-ticker.C:
			c.collect(ctx)
		}
	}
}

func (c *Collector) collect(ctx context.Context) {
	markets, err := c.provider.FetchActiveMarkets(ctx)
	if err != nil {
		slog.Error("failed to fetch active markets", "error", err)
		return
	}

	slog.Debug("fetched active markets", "count", len(markets))

	for _, m := range markets {
		// Upsert market — get the internal UUID back
		marketID, err := c.upsertMarket(ctx, &m)
		if err != nil {
			slog.Error("failed to upsert market", "external_id", m.ExternalID, "error", err)
			continue
		}

		// Fetch raw snapshot (outcome prices, cumulative volume, liquidity)
		snapshot, err := c.provider.FetchMarketSnapshot(ctx, m.ExternalID)
		if err != nil {
			slog.Error("failed to fetch snapshot", "external_id", m.ExternalID, "error", err)
			continue
		}

		// Fix P0: link snapshot to internal market UUID
		snapshot.MarketID = marketID

		// Fix P0: compute per-period volume (delta from last cumulative volume)
		snapshot.VolumePerPeriod = c.computeVolumeDelta(ctx, marketID, snapshot.Volume)

		// Fix P1: fetch order book and enrich snapshot with depth + spread
		ob, err := c.provider.FetchOrderBook(ctx, m.ExternalID)
		if err != nil {
			slog.Warn("failed to fetch order book, snapshot will have zero depth",
				"external_id", m.ExternalID, "error", err)
		} else {
			snapshot.BidDepth = decimal.NewFromFloat(ob.TotalBidDepth())
			snapshot.AskDepth = decimal.NewFromFloat(ob.TotalAskDepth())
			snapshot.Spread = decimal.NewFromFloat(ob.Spread)
		}

		if err := c.storeSnapshot(ctx, snapshot); err != nil {
			slog.Error("failed to store snapshot", "external_id", m.ExternalID, "error", err)
		}
	}
}

// upsertMarket inserts or updates a market and returns its internal UUID.
func (c *Collector) upsertMarket(ctx context.Context, m *models.Market) (uuid.UUID, error) {
	newID := uuid.New()
	var marketID uuid.UUID

	err := c.db.QueryRow(ctx, `
		INSERT INTO markets (id, external_id, question, outcomes, end_date, status)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (external_id) DO UPDATE SET
			question   = EXCLUDED.question,
			outcomes   = EXCLUDED.outcomes,
			end_date   = EXCLUDED.end_date,
			status     = EXCLUDED.status,
			updated_at = NOW()
		RETURNING id
	`, newID, m.ExternalID, m.Question, m.Outcomes, m.EndDate, m.Status).Scan(&marketID)
	if err != nil {
		return uuid.Nil, err
	}
	return marketID, nil
}

// computeVolumeDelta returns the difference between the current cumulative volume
// and the last stored cumulative volume for this market. Polymarket's volume field
// is cumulative (never decreases), so the delta gives actual volume traded this period.
func (c *Collector) computeVolumeDelta(ctx context.Context, marketID uuid.UUID, currentVolume decimal.Decimal) decimal.Decimal {
	var lastVolume decimal.Decimal
	err := c.db.QueryRow(ctx, `
		SELECT volume FROM market_snapshots
		WHERE market_id = $1
		ORDER BY captured_at DESC
		LIMIT 1
	`, marketID).Scan(&lastVolume)
	if err != nil {
		// No previous snapshot — first data point, delta = 0
		return decimal.Zero
	}

	delta := currentVolume.Sub(lastVolume)
	if delta.IsNegative() {
		// Shouldn't happen with cumulative volume, but guard against API anomalies
		return decimal.Zero
	}
	return delta
}

func (c *Collector) storeSnapshot(ctx context.Context, s *models.MarketSnapshot) error {
	_, err := c.db.Exec(ctx, `
		INSERT INTO market_snapshots
			(id, market_id, outcome_prices, volume, volume_per_period, liquidity, bid_depth, ask_depth, spread, captured_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, uuid.New(), s.MarketID, s.OutcomePrices,
		s.Volume, s.VolumePerPeriod, s.Liquidity,
		s.BidDepth, s.AskDepth, s.Spread,
		time.Now())
	return err
}
