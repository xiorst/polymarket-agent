package ml

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/x10rst/ai-agent-autonom/internal/config"
	"github.com/x10rst/ai-agent-autonom/internal/models"
)

// MinSnapshotsRequired is the minimum number of snapshots needed for a reliable prediction.
// At 30s poll interval, 20 snapshots = 10 minutes of history.
// Enough for meaningful momentum, slope, and volume acceleration calculations.
const MinSnapshotsRequired = 20

// Predictor is the interface for ML model implementations.
// Can be backed by a Go-native statistical model or a Python sidecar via gRPC/HTTP.
type Predictor interface {
	Predict(ctx context.Context, marketID string, snapshots []models.MarketSnapshot) (*Prediction, error)
}

// PredictorWithExpiry extends Predictor with market end date for expiry penalty.
type PredictorWithExpiry interface {
	Predictor
	PredictWithEndDate(ctx context.Context, marketID string, snapshots []models.MarketSnapshot, endDate time.Time) (*Prediction, error)
}

type Prediction struct {
	MarketID         string
	PredictedOutcome string
	Confidence       float64
}

// Pipeline orchestrates ML prediction and signal generation for all active markets.
type Pipeline struct {
	cfg       config.MarketAnalysisConfig
	db        *pgxpool.Pool
	predictor Predictor
}

func NewPipeline(cfg config.MarketAnalysisConfig, db *pgxpool.Pool, predictor Predictor) *Pipeline {
	return &Pipeline{cfg: cfg, db: db, predictor: predictor}
}

// GenerateSignals runs ML predictions on all active markets and returns
// actionable trading signals where confidence exceeds threshold and
// the market price is sufficiently mispriced relative to the prediction.
func (p *Pipeline) GenerateSignals(ctx context.Context) ([]models.Signal, error) {
	rows, err := p.db.Query(ctx, `
		SELECT m.id, m.external_id, m.outcomes, m.end_date
		FROM markets m
		WHERE m.status = 'active'
	`)
	if err != nil {
		return nil, fmt.Errorf("query active markets: %w", err)
	}
	defer rows.Close()

	var signals []models.Signal

	for rows.Next() {
		var marketID uuid.UUID
		var externalID string
		var outcomes []models.OutcomePrice
		var endDate time.Time

		if err := rows.Scan(&marketID, &externalID, &outcomes, &endDate); err != nil {
			slog.Error("scan market row", "error", err)
			continue
		}

		snapshots, err := p.getRecentSnapshots(ctx, marketID)
		if err != nil {
			slog.Error("get snapshots for prediction", "market", externalID, "error", err)
			continue
		}

		if len(snapshots) < MinSnapshotsRequired {
			slog.Debug("not enough snapshots for prediction",
				"market", externalID,
				"count", len(snapshots),
				"required", MinSnapshotsRequired,
			)
			continue
		}

		// Use PredictWithEndDate if the predictor supports it (for expiry penalty)
		var prediction *Prediction
		if ep, ok := p.predictor.(PredictorWithExpiry); ok {
			prediction, err = ep.PredictWithEndDate(ctx, externalID, snapshots, endDate)
		} else {
			prediction, err = p.predictor.Predict(ctx, externalID, snapshots)
		}
		if err != nil {
			slog.Error("ml prediction failed", "market", externalID, "error", err)
			continue
		}
		if prediction == nil {
			continue
		}

		// Gate 1: confidence threshold
		if prediction.Confidence < p.cfg.ConfidenceThreshold {
			slog.Debug("prediction below confidence threshold",
				"market", externalID,
				"confidence", prediction.Confidence,
				"threshold", p.cfg.ConfidenceThreshold,
			)
			continue
		}

		// Get current market price for the predicted outcome from latest snapshot outcomes
		var currentPrice decimal.Decimal
		for _, o := range outcomes {
			if o.Name == prediction.PredictedOutcome {
				currentPrice = o.Price
				break
			}
		}

		// Gate 2: mispricing threshold — only trade if there's real edge
		predictedProb := decimal.NewFromFloat(prediction.Confidence)
		diff := predictedProb.Sub(currentPrice).Abs()
		mispricingThreshold := decimal.NewFromFloat(p.cfg.MispricingThreshold)

		if diff.LessThan(mispricingThreshold) {
			slog.Debug("no significant mispricing",
				"market", externalID,
				"predicted", prediction.Confidence,
				"market_price", currentPrice,
				"diff", diff,
				"threshold", mispricingThreshold,
			)
			continue
		}

		signal := models.Signal{
			ID:               uuid.New(),
			MarketID:         marketID,
			PredictedOutcome: prediction.PredictedOutcome,
			Confidence:       decimal.NewFromFloat(prediction.Confidence),
			MarketPrice:      currentPrice,
			CreatedAt:        time.Now(),
		}

		if err := p.storeSignal(ctx, &signal); err != nil {
			slog.Error("failed to store signal", "market", externalID, "error", err)
			continue
		}

		signals = append(signals, signal)
		slog.Info("trading signal generated",
			"market", externalID,
			"outcome", prediction.PredictedOutcome,
			"confidence", prediction.Confidence,
			"market_price", currentPrice,
			"edge", diff,
			"hours_to_expiry", time.Until(endDate).Hours(),
		)
	}

	return signals, nil
}

// GenerateIdempotencyKey creates a unique key for a signal to prevent duplicate orders.
func GenerateIdempotencyKey(marketID uuid.UUID, outcome string, signalTime time.Time) string {
	data := fmt.Sprintf("%s:%s:%d", marketID, outcome, signalTime.UnixNano())
	hash := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", hash)
}

func (p *Pipeline) getRecentSnapshots(ctx context.Context, marketID uuid.UUID) ([]models.MarketSnapshot, error) {
	rows, err := p.db.Query(ctx, `
		SELECT id, market_id, outcome_prices, volume, volume_per_period, liquidity,
		       bid_depth, ask_depth, spread, captured_at
		FROM market_snapshots
		WHERE market_id = $1
		ORDER BY captured_at DESC
		LIMIT 100
	`, marketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var snapshots []models.MarketSnapshot
	for rows.Next() {
		var s models.MarketSnapshot
		if err := rows.Scan(
			&s.ID, &s.MarketID, &s.OutcomePrices,
			&s.Volume, &s.VolumePerPeriod, &s.Liquidity,
			&s.BidDepth, &s.AskDepth, &s.Spread,
			&s.CapturedAt,
		); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, s)
	}
	return snapshots, nil
}

func (p *Pipeline) storeSignal(ctx context.Context, s *models.Signal) error {
	_, err := p.db.Exec(ctx, `
		INSERT INTO signals (id, market_id, predicted_outcome, confidence, market_price, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, s.ID, s.MarketID, s.PredictedOutcome, s.Confidence, s.MarketPrice, s.CreatedAt)
	return err
}
