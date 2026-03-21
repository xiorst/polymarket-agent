package telegram

import (
	"testing"
	"time"
)

func TestScoreMessage_CryptoRally(t *testing.T) {
	msg := Message{
		ChannelUsername: "marketfeed",
		Text:            "Bitcoin rally continues as BTC breaks all time high above $100K",
		ReceivedAt:      time.Now(),
	}

	sig := ScoreMessage(msg)
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
	t.Logf("signal: category=%s sentiment=%.1f confidence=%.2f keywords=%v", sig.Category, sig.Sentiment, sig.Confidence, sig.Keywords)
}

func TestScoreMessage_GeopoliticsBearish(t *testing.T) {
	msg := Message{
		ChannelUsername: "marketfeed",
		Text:            "Breaking: Airstrike reported near capital, casualties confirmed as war escalation continues",
		ReceivedAt:      time.Now(),
	}

	sig := ScoreMessage(msg)
	if sig == nil {
		t.Fatal("expected signal, got nil")
	}
	if sig.Category != CategoryGeopolitics {
		t.Errorf("expected geopolitics, got %s", sig.Category)
	}
	if sig.Sentiment != SentimentBearish {
		t.Errorf("expected bearish, got %v", sig.Sentiment)
	}
	t.Logf("signal: category=%s sentiment=%.1f confidence=%.2f keywords=%v", sig.Category, sig.Sentiment, sig.Confidence, sig.Keywords)
}

func TestScoreMessage_EconomyRateCut(t *testing.T) {
	msg := Message{
		ChannelUsername: "marketfeed",
		Text:            "FOMC: Fed announces rate cut of 25bps, market rally expected",
		ReceivedAt:      time.Now(),
	}

	sig := ScoreMessage(msg)
	if sig == nil {
		t.Fatal("expected signal, got nil")
	}
	if sig.Category != CategoryEconomy {
		t.Errorf("expected economy, got %s", sig.Category)
	}
	t.Logf("signal: category=%s sentiment=%.1f confidence=%.2f keywords=%v", sig.Category, sig.Sentiment, sig.Confidence, sig.Keywords)
}

func TestScoreMessage_Politics(t *testing.T) {
	msg := Message{
		ChannelUsername: "marketfeed",
		Text:            "Trump wins election with landslide victory in swing states",
		ReceivedAt:      time.Now(),
	}

	sig := ScoreMessage(msg)
	if sig == nil {
		t.Fatal("expected signal, got nil")
	}
	if sig.Category != CategoryPolitics {
		t.Errorf("expected politics, got %s", sig.Category)
	}
	t.Logf("signal: category=%s sentiment=%.1f confidence=%.2f keywords=%v", sig.Category, sig.Sentiment, sig.Confidence, sig.Keywords)
}

func TestScoreMessage_Sports(t *testing.T) {
	msg := Message{
		ChannelUsername: "marketfeed",
		Text:            "Real Madrid wins UEFA Champions League championship title",
		ReceivedAt:      time.Now(),
	}

	sig := ScoreMessage(msg)
	if sig == nil {
		t.Fatal("expected signal, got nil")
	}
	if sig.Category != CategorySports {
		t.Errorf("expected sports, got %s", sig.Category)
	}
	t.Logf("signal: category=%s sentiment=%.1f confidence=%.2f keywords=%v", sig.Category, sig.Sentiment, sig.Confidence, sig.Keywords)
}

func TestScoreMessage_NoMatch(t *testing.T) {
	msg := Message{
		ChannelUsername: "marketfeed",
		Text:            "Good morning everyone! Have a great day.",
		ReceivedAt:      time.Now(),
	}

	sig := ScoreMessage(msg)
	if sig != nil {
		t.Errorf("expected nil signal for irrelevant message, got %+v", sig)
	}
}

func TestScoreMessage_MultiKeywordBoost(t *testing.T) {
	msg := Message{
		ChannelUsername: "marketfeed",
		Text:            "Recession fears grow as unemployment rises, inflation surge pushes Fed toward rate hike",
		ReceivedAt:      time.Now(),
	}

	sig := ScoreMessage(msg)
	if sig == nil {
		t.Fatal("expected signal, got nil")
	}
	// Multiple economy keywords should boost confidence
	if sig.Confidence < 0.70 {
		t.Errorf("expected confidence boost from multiple keywords, got %f", sig.Confidence)
	}
	t.Logf("signal: category=%s sentiment=%.1f confidence=%.2f keywords=%v", sig.Category, sig.Sentiment, sig.Confidence, sig.Keywords)
}
