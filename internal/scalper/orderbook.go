package scalper

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wsEndpoint     = "wss://ws-subscriptions-clob.polymarket.com/ws/market"
	wsReconnectWait = 5 * time.Second
)

// OrderBookSnapshot holds the best bid/ask and depth for a single token.
type OrderBookSnapshot struct {
	TokenID   string
	BestBid   float64
	BestAsk   float64
	BidDepth  float64 // total size of top-3 bids
	AskDepth  float64 // total size of top-3 asks
	UpdatedAt time.Time
}

// OrderBook maintains live order book state via WebSocket.
type OrderBook struct {
	mu        sync.RWMutex
	snapshots map[string]*OrderBookSnapshot
	cfg       *Config
}

// NewOrderBook creates an empty OrderBook.
func NewOrderBook(cfg *Config) *OrderBook {
	return &OrderBook{
		snapshots: make(map[string]*OrderBookSnapshot),
		cfg:       cfg,
	}
}

// GetSnapshot returns the latest snapshot for a token.
func (ob *OrderBook) GetSnapshot(tokenID string) (OrderBookSnapshot, bool) {
	ob.mu.RLock()
	defer ob.mu.RUnlock()
	s, ok := ob.snapshots[tokenID]
	if !ok {
		return OrderBookSnapshot{}, false
	}
	return *s, true
}

// Subscribe starts a WebSocket goroutine for the given token IDs.
// It reconnects automatically on disconnect.
func (ob *OrderBook) Subscribe(ctx context.Context, tokenIDs ...string) error {
	if len(tokenIDs) == 0 {
		return fmt.Errorf("no token IDs provided")
	}
	go ob.runWS(ctx, tokenIDs)
	return nil
}

// ----- internal WS logic -----

type wsBookMsg struct {
	Type    string    `json:"type"`
	AssetID string    `json:"asset_id"`
	Bids    []wsLevel `json:"bids"`
	Asks    []wsLevel `json:"asks"`
	// price_change fields
	Side    string `json:"side"` // "BUY" or "SELL"
	Price   string `json:"price"`
	Size    string `json:"size"`
}

type wsLevel struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

func (ob *OrderBook) runWS(ctx context.Context, tokenIDs []string) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := ob.connectAndListen(ctx, tokenIDs); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("orderbook ws disconnected, reconnecting", "error", err, "wait", wsReconnectWait)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wsReconnectWait):
			}
		}
	}
}

func (ob *OrderBook) connectAndListen(ctx context.Context, tokenIDs []string) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsEndpoint, nil)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	defer conn.Close()

	// Subscribe for each token
	subMsg := map[string]interface{}{
		"assets_ids": tokenIDs,
		"type":       "market",
	}
	if err := conn.WriteJSON(subMsg); err != nil {
		return fmt.Errorf("ws subscribe: %w", err)
	}

	slog.Info("orderbook ws connected", "tokens", tokenIDs)

	// Local full book state: map tokenID → side → price → size
	bids := make(map[string]map[string]float64)
	asks := make(map[string]map[string]float64)
	for _, id := range tokenIDs {
		bids[id] = make(map[string]float64)
		asks[id] = make(map[string]float64)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		_, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("ws read: %w", err)
		}

		// Messages may be arrays or single objects
		// Try array first
		var msgs []wsBookMsg
		if json.Unmarshal(raw, &msgs) != nil {
			// Try single object
			var single wsBookMsg
			if err := json.Unmarshal(raw, &single); err != nil {
				continue
			}
			msgs = []wsBookMsg{single}
		}

		for _, msg := range msgs {
			switch msg.Type {
			case "book":
				ob.applySnapshot(msg, bids, asks)
			case "price_change":
				ob.applyDelta(msg, bids, asks)
			}
		}
	}
}

func (ob *OrderBook) applySnapshot(msg wsBookMsg,
	bids, asks map[string]map[string]float64) {

	tokenID := msg.AssetID
	if _, ok := bids[tokenID]; !ok {
		bids[tokenID] = make(map[string]float64)
		asks[tokenID] = make(map[string]float64)
	}

	// Reset
	bids[tokenID] = make(map[string]float64)
	asks[tokenID] = make(map[string]float64)

	for _, l := range msg.Bids {
		p, _ := strconv.ParseFloat(l.Price, 64)
		s, _ := strconv.ParseFloat(l.Size, 64)
		bids[tokenID][l.Price] = p
		_ = s
		if sz, err := strconv.ParseFloat(l.Size, 64); err == nil {
			bids[tokenID][l.Price] = sz // size keyed by price string
		}
	}
	for _, l := range msg.Asks {
		if sz, err := strconv.ParseFloat(l.Size, 64); err == nil {
			asks[tokenID][l.Price] = sz
		}
	}

	// Store bid prices separately for best-bid calculation
	ob.rebuildSnapshot(tokenID, msg.Bids, msg.Asks)
}

func (ob *OrderBook) applyDelta(msg wsBookMsg,
	bids, asks map[string]map[string]float64) {

	tokenID := msg.AssetID
	if _, ok := bids[tokenID]; !ok {
		return
	}

	sz, _ := strconv.ParseFloat(msg.Size, 64)
	if msg.Side == "BUY" {
		if sz == 0 {
			delete(bids[tokenID], msg.Price)
		} else {
			bids[tokenID][msg.Price] = sz
		}
	} else {
		if sz == 0 {
			delete(asks[tokenID], msg.Price)
		} else {
			asks[tokenID][msg.Price] = sz
		}
	}

	// Rebuild snapshot from current bid/ask maps
	bidLevels := mapToLevels(bids[tokenID])
	askLevels := mapToLevels(asks[tokenID])
	ob.rebuildSnapshot(tokenID, bidLevels, askLevels)
}

// mapToLevels converts a price→size map to wsLevel slice.
func mapToLevels(m map[string]float64) []wsLevel {
	levels := make([]wsLevel, 0, len(m))
	for price, size := range m {
		levels = append(levels, wsLevel{Price: price, Size: strconv.FormatFloat(size, 'f', -1, 64)})
	}
	return levels
}

// rebuildSnapshot recalculates and stores the snapshot for tokenID.
func (ob *OrderBook) rebuildSnapshot(tokenID string, bidLevels, askLevels []wsLevel) {
	// Sort bids descending by price
	sort.Slice(bidLevels, func(i, j int) bool {
		pi, _ := strconv.ParseFloat(bidLevels[i].Price, 64)
		pj, _ := strconv.ParseFloat(bidLevels[j].Price, 64)
		return pi > pj
	})
	// Sort asks ascending by price
	sort.Slice(askLevels, func(i, j int) bool {
		pi, _ := strconv.ParseFloat(askLevels[i].Price, 64)
		pj, _ := strconv.ParseFloat(askLevels[j].Price, 64)
		return pi < pj
	})

	var bestBid, bestAsk, bidDepth, askDepth float64

	if len(bidLevels) > 0 {
		bestBid, _ = strconv.ParseFloat(bidLevels[0].Price, 64)
		for i := 0; i < 3 && i < len(bidLevels); i++ {
			sz, _ := strconv.ParseFloat(bidLevels[i].Size, 64)
			bidDepth += sz
		}
	}
	if len(askLevels) > 0 {
		bestAsk, _ = strconv.ParseFloat(askLevels[0].Price, 64)
		for i := 0; i < 3 && i < len(askLevels); i++ {
			sz, _ := strconv.ParseFloat(askLevels[i].Size, 64)
			askDepth += sz
		}
	}

	ob.mu.Lock()
	ob.snapshots[tokenID] = &OrderBookSnapshot{
		TokenID:   tokenID,
		BestBid:   bestBid,
		BestAsk:   bestAsk,
		BidDepth:  bidDepth,
		AskDepth:  askDepth,
		UpdatedAt: time.Now(),
	}
	ob.mu.Unlock()
}
