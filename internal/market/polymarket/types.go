package polymarket

import "time"

// API response types for Polymarket/CLOB API

type APIMarket struct {
	ConditionID   string       `json:"condition_id"`
	QuestionID    string       `json:"question_id"`
	Question      string       `json:"question"`
	Description   string       `json:"description"`
	EndDateISO    string       `json:"end_date_iso"`
	Active        bool         `json:"active"`
	Closed        bool         `json:"closed"`
	Tokens        []APIToken   `json:"tokens"`
	Volume        string       `json:"volume"`
	Liquidity     string       `json:"liquidity"`
}

type APIToken struct {
	TokenID  string `json:"token_id"`
	Outcome  string `json:"outcome"`
	Price    string `json:"price"`
	Winner   bool   `json:"winner"`
}

type APIOrderBook struct {
	MarketID string          `json:"market"`
	AssetID  string          `json:"asset_id"`
	Bids     []APIBookEntry  `json:"bids"`
	Asks     []APIBookEntry  `json:"asks"`
	Hash     string          `json:"hash"`
}

type APIBookEntry struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

type APIOrderResponse struct {
	OrderID   string `json:"orderID"`
	Status    string `json:"status"`
	TxHash    string `json:"transactionsHashes"`
}

// Internal normalized types

type NormalizedMarket struct {
	ExternalID string
	Question   string
	Outcomes   []NormalizedOutcome
	EndDate    time.Time
	Active     bool
	Volume     float64
	Liquidity  float64
}

type NormalizedOutcome struct {
	Name    string
	TokenID string
	Price   float64
}
