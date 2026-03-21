package telegram

import (
	"strings"
	"time"
	"unicode"
)

// Category represents a Polymarket market category.
type Category string

const (
	CategoryCrypto      Category = "crypto"
	CategoryPolitics    Category = "politics"
	CategorySports      Category = "sports"
	CategoryGeopolitics Category = "geopolitics"
	CategoryEconomy     Category = "economy"
	CategoryOther       Category = "other"
)

// Sentiment represents the directional bias of a signal.
type Sentiment float64

const (
	SentimentBullish  Sentiment = 1.0
	SentimentNeutral  Sentiment = 0.0
	SentimentBearish  Sentiment = -1.0
)

// ExternalSignal is the output of the scorer — injected into the ML pipeline.
type ExternalSignal struct {
	Category   Category
	Sentiment  Sentiment  // -1.0 (bearish) to +1.0 (bullish)
	Confidence float64    // 0.0 to 1.0 — how confident is this signal
	Keywords   []string   // matched keywords
	Source     string     // channel username
	RawText    string     // original message snippet
	CreatedAt  time.Time
}

// keywordRule defines a set of keywords mapped to a category and sentiment.
type keywordRule struct {
	keywords   []string
	category   Category
	sentiment  Sentiment
	confidence float64
}

// rules is the rule-based keyword matcher.
// Ordered by specificity — more specific rules first.
var rules = []keywordRule{
	// === CRYPTO ===
	{
		keywords:   []string{"bitcoin", "btc", "eth", "ethereum", "solana", "sol", "crypto", "blockchain", "defi", "nft", "altcoin", "memecoin"},
		category:   CategoryCrypto,
		sentiment:  SentimentNeutral,
		confidence: 0.5,
	},
	{
		keywords:   []string{"bitcoin rally", "btc pump", "eth pump", "crypto bull", "all time high", "ath", "moon", "breakout"},
		category:   CategoryCrypto,
		sentiment:  SentimentBullish,
		confidence: 0.7,
	},
	{
		keywords:   []string{"bitcoin crash", "btc dump", "crypto bear", "sell off", "liquidation", "capitulation", "rug pull"},
		category:   CategoryCrypto,
		sentiment:  SentimentBearish,
		confidence: 0.7,
	},

	// === POLITICS ===
	{
		keywords:   []string{"election", "vote", "president", "congress", "senate", "parliament", "democrat", "republican", "poll", "ballot"},
		category:   CategoryPolitics,
		sentiment:  SentimentNeutral,
		confidence: 0.6,
	},
	{
		keywords:   []string{"trump", "biden", "harris", "musk", "zelensky", "putin", "xi jinping", "modi"},
		category:   CategoryPolitics,
		sentiment:  SentimentNeutral,
		confidence: 0.65,
	},
	{
		keywords:   []string{"wins election", "elected president", "victory", "landslide"},
		category:   CategoryPolitics,
		sentiment:  SentimentBullish,
		confidence: 0.75,
	},

	// === GEOPOLITICS ===
	{
		keywords:   []string{"war", "conflict", "missile", "attack", "invasion", "troops", "military", "nato", "sanction", "ceasefire", "peace deal"},
		category:   CategoryGeopolitics,
		sentiment:  SentimentNeutral,
		confidence: 0.65,
	},
	{
		keywords:   []string{"ceasefire agreed", "peace deal", "negotiations successful", "troops withdrawal"},
		category:   CategoryGeopolitics,
		sentiment:  SentimentBullish,
		confidence: 0.75,
	},
	{
		keywords:   []string{"war escalation", "nuclear", "airstrike", "explosion", "casualties"},
		category:   CategoryGeopolitics,
		sentiment:  SentimentBearish,
		confidence: 0.75,
	},

	// === ECONOMY ===
	{
		keywords:   []string{"fed", "federal reserve", "interest rate", "inflation", "cpi", "gdp", "recession", "rate hike", "rate cut", "fomc"},
		category:   CategoryEconomy,
		sentiment:  SentimentNeutral,
		confidence: 0.6,
	},
	{
		keywords:   []string{"rate cut", "stimulus", "gdp growth", "employment rises", "market rally"},
		category:   CategoryEconomy,
		sentiment:  SentimentBullish,
		confidence: 0.7,
	},
	{
		keywords:   []string{"rate hike", "recession", "unemployment rises", "inflation surge", "market crash"},
		category:   CategoryEconomy,
		sentiment:  SentimentBearish,
		confidence: 0.7,
	},

	// === SPORTS ===
	{
		keywords:   []string{"match", "game", "championship", "world cup", "nba", "nfl", "soccer", "football", "tennis", "formula 1", "f1", "ufc", "mma"},
		category:   CategorySports,
		sentiment:  SentimentNeutral,
		confidence: 0.5,
	},
	{
		keywords:   []string{"wins", "champion", "title", "gold medal", "victory"},
		category:   CategorySports,
		sentiment:  SentimentBullish,
		confidence: 0.65,
	},
	{
		keywords:   []string{"injured", "suspended", "disqualified", "eliminated"},
		category:   CategorySports,
		sentiment:  SentimentBearish,
		confidence: 0.65,
	},
}

// DefaultScorer returns a rule-based scorer function suitable for use with Feed.
func DefaultScorer() func(Message) *ExternalSignal {
	return func(msg Message) *ExternalSignal {
		return ScoreMessage(msg)
	}
}

// ScoreMessage applies rule-based keyword matching to a message.
// Returns nil if no relevant signal is found.
func ScoreMessage(msg Message) *ExternalSignal {
	text := normalize(msg.Text)

	bestRule := (*keywordRule)(nil)
	matchedKeywords := []string{}

	// Find the best matching rule (highest confidence with most keyword hits)
	for i := range rules {
		r := &rules[i]
		hits := matchKeywords(text, r.keywords)
		if len(hits) == 0 {
			continue
		}
		if bestRule == nil || r.confidence > bestRule.confidence || len(hits) > len(matchedKeywords) {
			bestRule = r
			matchedKeywords = hits
		}
	}

	if bestRule == nil {
		return nil
	}

	// Boost confidence slightly for multiple keyword hits
	confidence := bestRule.confidence
	if len(matchedKeywords) >= 3 {
		confidence = min64(confidence+0.10, 1.0)
	} else if len(matchedKeywords) >= 2 {
		confidence = min64(confidence+0.05, 1.0)
	}

	return &ExternalSignal{
		Category:   bestRule.category,
		Sentiment:  bestRule.sentiment,
		Confidence: confidence,
		Keywords:   matchedKeywords,
		Source:     msg.ChannelUsername,
		RawText:    truncate(msg.Text, 200),
		CreatedAt:  msg.ReceivedAt,
	}
}

// normalize lowercases and strips punctuation for keyword matching.
func normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return b.String()
}

// matchKeywords returns the list of keywords found in text.
func matchKeywords(text string, keywords []string) []string {
	var found []string
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			found = append(found, kw)
		}
	}
	return found
}

func min64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
