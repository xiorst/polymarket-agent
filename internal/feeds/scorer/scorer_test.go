package scorer

import (
	"testing"
)

func TestScore_CryptoRally(t *testing.T) {
	sig := ScoreText("Bitcoin rally continues as BTC breaks all time high above $100K", "marketfeed")
	if sig == nil {
		t.Fatal("expected signal, got nil")
	}
	if sig.Category != CategoryCrypto {
		t.Errorf("expected crypto, got %s", sig.Category)
	}
	if sig.Sentiment != SentimentBullish {
		t.Errorf("expected bullish, got %v", sig.Sentiment)
	}
	if sig.Confidence < 0.6 {
		t.Errorf("expected confidence >= 0.6, got %f", sig.Confidence)
	}
	t.Logf("✅ category=%s sentiment=%.1f confidence=%.2f keywords=%v", sig.Category, sig.Sentiment, sig.Confidence, sig.Keywords)
}

func TestScore_GeopoliticsBearish(t *testing.T) {
	sig := ScoreText("Breaking: Airstrike reported near capital, casualties confirmed as war escalation continues", "marketfeed")
	if sig == nil {
		t.Fatal("expected signal, got nil")
	}
	if sig.Category != CategoryGeopolitics {
		t.Errorf("expected geopolitics, got %s", sig.Category)
	}
	if sig.Sentiment != SentimentBearish {
		t.Errorf("expected bearish, got %v", sig.Sentiment)
	}
	t.Logf("✅ category=%s sentiment=%.1f confidence=%.2f keywords=%v", sig.Category, sig.Sentiment, sig.Confidence, sig.Keywords)
}

func TestScore_EconomyRateCut(t *testing.T) {
	sig := ScoreText("FOMC: Fed announces rate cut of 25bps, market rally expected", "marketfeed")
	if sig == nil {
		t.Fatal("expected signal, got nil")
	}
	if sig.Category != CategoryEconomy {
		t.Errorf("expected economy, got %s", sig.Category)
	}
	t.Logf("✅ category=%s sentiment=%.1f confidence=%.2f keywords=%v", sig.Category, sig.Sentiment, sig.Confidence, sig.Keywords)
}

func TestScore_Politics(t *testing.T) {
	sig := ScoreText("Trump wins election with landslide victory in swing states", "marketfeed")
	if sig == nil {
		t.Fatal("expected signal, got nil")
	}
	if sig.Category != CategoryPolitics {
		t.Errorf("expected politics, got %s", sig.Category)
	}
	t.Logf("✅ category=%s sentiment=%.1f confidence=%.2f keywords=%v", sig.Category, sig.Sentiment, sig.Confidence, sig.Keywords)
}

func TestScore_Sports(t *testing.T) {
	sig := ScoreText("Real Madrid wins UEFA Champions League championship title", "marketfeed")
	if sig == nil {
		t.Fatal("expected signal, got nil")
	}
	if sig.Category != CategorySports {
		t.Errorf("expected sports, got %s", sig.Category)
	}
	t.Logf("✅ category=%s sentiment=%.1f confidence=%.2f keywords=%v", sig.Category, sig.Sentiment, sig.Confidence, sig.Keywords)
}

func TestScore_NoMatch(t *testing.T) {
	sig := ScoreText("Good morning everyone! Have a great day.", "marketfeed")
	if sig != nil {
		t.Errorf("expected nil for irrelevant message, got %+v", sig)
	}
	t.Log("✅ irrelevant message correctly returns nil")
}

func TestScore_MultiKeywordBoost(t *testing.T) {
	sig := ScoreText("Recession fears grow as unemployment rises, inflation surge pushes Fed toward rate hike", "marketfeed")
	if sig == nil {
		t.Fatal("expected signal, got nil")
	}
	if sig.Confidence < 0.70 {
		t.Errorf("expected confidence boost >= 0.70 from multiple keywords, got %f", sig.Confidence)
	}
	t.Logf("✅ category=%s sentiment=%.1f confidence=%.2f keywords=%v", sig.Category, sig.Sentiment, sig.Confidence, sig.Keywords)
}
