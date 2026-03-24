package scalper

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"golang.org/x/net/proxy"

	"log/slog"
)

const clobBase = "https://clob.polymarket.com"

// CLOBExecutor executes orders via the Polymarket CLOB REST API.
type CLOBExecutor struct {
	cfg         *Config
	client      *http.Client // via proxy (untuk order)
	directClient *http.Client // direct (untuk non-order requests)
}

// OrderResult holds the result of a placed order.
type OrderResult struct {
	OrderID   string
	Status    string
	FilledAmt float64
	Price     float64
}

// NewCLOBExecutor creates a new CLOBExecutor.
// Order requests diroute via SOCKS5 proxy (bypass geoblock).
// Balance/status requests pakai direct connection (lebih cepat).
func NewCLOBExecutor(cfg *Config) *CLOBExecutor {
	directClient := &http.Client{Timeout: 8 * time.Second}

	orderClient := &http.Client{Timeout: 10 * time.Second}

	// Route order via SOCKS5 proxy hanya jika SOCKS5_PROXY di-set
	proxyAddr := os.Getenv("SOCKS5_PROXY")
	if proxyAddr != "" {
		if u, err := url.Parse(proxyAddr); err == nil {
			if dialer, err := proxy.FromURL(u, proxy.Direct); err == nil {
				orderClient.Transport = &http.Transport{
					DialContext: dialer.(proxy.ContextDialer).DialContext,
				}
			}
		}
	}

	return &CLOBExecutor{
		cfg:          cfg,
		client:       orderClient,
		directClient: directClient,
	}
}

// sign produces the HMAC-SHA256 signature for L2 auth.
// signature = base64url(HMAC-SHA256(base64url-decoded(secret), timestamp+METHOD+path+body))
func (e *CLOBExecutor) sign(timestamp, method, path, body string) (string, error) {
	secret := e.cfg.APISecret

	// Decode base64url secret — pad if needed
	padded := secret
	switch len(padded) % 4 {
	case 2:
		padded += "=="
	case 3:
		padded += "="
	}
	key, err := base64.URLEncoding.DecodeString(padded)
	if err != nil {
		// Try raw base64
		key, err = base64.StdEncoding.DecodeString(padded)
		if err != nil {
			return "", fmt.Errorf("decode api secret: %w", err)
		}
	}

	msg := timestamp + method + path + body
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	sig := base64.URLEncoding.EncodeToString(mac.Sum(nil))
	return sig, nil
}

// authHeaders returns L2 auth headers for a request.
func (e *CLOBExecutor) authHeaders(method, path, body string) (map[string]string, error) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig, err := e.sign(ts, strings.ToUpper(method), path, body)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"POLY_ADDRESS":    e.cfg.SignerAddress, // L2 = signer/EOA address
		"POLY_SIGNATURE":  sig,
		"POLY_TIMESTAMP":  ts,
		"POLY_API_KEY":    e.cfg.APIKey,
		"POLY_PASSPHRASE": e.cfg.APIPassphrase,
		"Content-Type":    "application/json",
	}, nil
}

func (e *CLOBExecutor) doRequest(ctx context.Context, method, path, body string) ([]byte, int, error) {
	return e.doRequestWithClient(ctx, method, path, body, e.client)
}

func (e *CLOBExecutor) doDirectRequest(ctx context.Context, method, path, body string) ([]byte, int, error) {
	return e.doRequestWithClient(ctx, method, path, body, e.directClient)
}

func (e *CLOBExecutor) doRequestWithClient(ctx context.Context, method, path, body string, httpClient *http.Client) ([]byte, int, error) {
	headers, err := e.authHeaders(method, path, body)
	if err != nil {
		return nil, 0, err
	}

	reqURL := clobBase + path
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// GetBalance returns the USDC balance from the CLOB.
// Endpoint: GET /balance-allowance?asset_type=USDC
// Fallback ke TotalCapital jika endpoint tidak tersedia.
func (e *CLOBExecutor) GetBalance(ctx context.Context) (float64, error) {
	path := "/balance-allowance?asset_type=USDC"

	// Gunakan timeout pendek untuk balance check — non-blocking
	balCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	data, status, err := e.doDirectRequest(balCtx, http.MethodGet, path, "")
	if err != nil || status != http.StatusOK {
		// Fallback: pakai TotalCapital dari config supaya scalper tidak blocked
		return e.cfg.TotalCapital, nil
	}

	// Response: {"asset_type":"USDC","balance":"3.00","allowance":"999999"}
	var result struct {
		Balance string `json:"balance"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return e.cfg.TotalCapital, nil
	}

	bal, err := strconv.ParseFloat(result.Balance, 64)
	if err != nil {
		return e.cfg.TotalCapital, nil
	}
	return bal, nil
}

// PlaceMarketBuy places a FOK market buy order for the given USDC amount.
// BUY: makerAmount = USDC spent (6 dec), takerAmount = shares received = makerAmount/price (6 dec)
func (e *CLOBExecutor) PlaceMarketBuy(ctx context.Context, tokenID string, usdcAmount float64, price float64) (*OrderResult, error) {
	saltVal := big.NewInt(rand.Int63()) //nolint:gosec

	tokenIDBig, ok := new(big.Int).SetString(tokenID, 10)
	if !ok {
		return nil, fmt.Errorf("invalid tokenID: %s", tokenID)
	}

	if price <= 0 || price >= 1 {
		return nil, fmt.Errorf("invalid price: %f (must be 0 < price < 1)", price)
	}

	// Round price to 2 decimal places (tick size 0.01)
	price = math.Round(price*100) / 100

	// makerAmount = USDC (6 decimals)
	makerAmtInt := int64(usdcAmount * 1e6)
	// takerAmount = shares = USDC / price (6 decimals), rounded down
	takerAmtRaw := (usdcAmount / price) * 1e6
	takerAmtInt := int64(math.Floor(takerAmtRaw))

	o := &OrderStruct{
		Salt:          saltVal,
		Maker:         common.HexToAddress(e.cfg.BuilderAddress),
		Signer:        common.HexToAddress(e.cfg.SignerAddress),
		Taker:         common.HexToAddress("0x0000000000000000000000000000000000000000"),
		TokenID:       tokenIDBig,
		MakerAmount:   big.NewInt(makerAmtInt),
		TakerAmount:   big.NewInt(takerAmtInt),
		Expiration:    big.NewInt(0),
		Nonce:         big.NewInt(0),
		FeeRateBps:    big.NewInt(0),
		Side:          0, // BUY = 0
		SignatureType: 1, // POLY_PROXY = 1 (OAuth/Proxy wallet)
	}

	sig, err := signOrder(o, e.cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("sign order: %w", err)
	}

	order := map[string]interface{}{
		"salt":          saltVal.Int64(), // integer per Polymarket spec
		"maker":         strings.ToLower(e.cfg.BuilderAddress),
		"signer":        strings.ToLower(e.cfg.SignerAddress),
		"taker":         "0x0000000000000000000000000000000000000000",
		"tokenId":       tokenID,
		"makerAmount":   strconv.FormatInt(makerAmtInt, 10),
		"takerAmount":   strconv.FormatInt(takerAmtInt, 10),
		"expiration":    "0",
		"nonce":         "0",
		"feeRateBps":    "0",
		"side":          "BUY", // string per Polymarket JSON spec
		"signatureType": 1,     // POLY_PROXY
		"signature":     sig,
	}

	payload := map[string]interface{}{
		"order":     order,
		"owner":     e.cfg.APIKey,
		"orderType": "FOK",
		"postOnly":  false,
	}

	bodyBytes, _ := json.Marshal(payload)
	body := string(bodyBytes)

	slog.Debug("placing order payload", "body", body)

	data, status, err := e.doRequest(ctx, http.MethodPost, "/order", body)
	if err != nil {
		return nil, fmt.Errorf("place market buy: %w", err)
	}
	if status != http.StatusOK && status != http.StatusCreated {
		slog.Debug("order response", "status", status, "body", string(data))
		return nil, fmt.Errorf("place market buy status %d: %s", status, string(data))
	}

	return parseOrderResult(data)
}

// PlaceLimitSell places a GTC limit sell order.
func (e *CLOBExecutor) PlaceLimitSell(ctx context.Context, tokenID string, shares float64, price float64) (*OrderResult, error) {
	makerAmt := strconv.FormatInt(int64(shares*1e6), 10)
	takerAmt := strconv.FormatInt(int64(price*shares*1e6), 10)
	salt := rand.Int63() //nolint:gosec

	order := map[string]interface{}{
		"salt":          strconv.FormatInt(salt, 10),
		"maker":         e.cfg.BuilderAddress,
		"signer":        e.cfg.SignerAddress,
		"taker":         "0x0000000000000000000000000000000000000000",
		"tokenId":       tokenID,
		"makerAmount":   makerAmt,
		"takerAmount":   takerAmt,
		"expiration":    "0",
		"nonce":         "0",
		"feeRateBps":    "0",
		"side":          "SELL",
		"signatureType": 2,
		"signature":     "0x",
	}

	payload := map[string]interface{}{
		"order":     order,
		"owner":     e.cfg.APIKey,
		"orderType": "GTC",
	}

	bodyBytes, _ := json.Marshal(payload)
	body := string(bodyBytes)

	data, status, err := e.doRequest(ctx, http.MethodPost, "/order", body)
	if err != nil {
		return nil, fmt.Errorf("place limit sell: %w", err)
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return nil, fmt.Errorf("place limit sell status %d: %s", status, string(data))
	}

	return parseOrderResult(data)
}

// CancelOrder cancels an order by ID.
func (e *CLOBExecutor) CancelOrder(ctx context.Context, orderID string) error {
	path := "/orders/" + orderID
	data, status, err := e.doRequest(ctx, http.MethodDelete, path, "")
	if err != nil {
		return fmt.Errorf("cancel order: %w", err)
	}
	if status != http.StatusOK && status != http.StatusNoContent {
		return fmt.Errorf("cancel order status %d: %s", status, string(data))
	}
	return nil
}

// GetOrderStatus returns the status of an order.
func (e *CLOBExecutor) GetOrderStatus(ctx context.Context, orderID string) (string, error) {
	path := "/orders/" + orderID
	data, status, err := e.doRequest(ctx, http.MethodGet, path, "")
	if err != nil {
		return "", fmt.Errorf("get order status: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("get order status %d: %s", status, string(data))
	}

	var result struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("parse order status: %w", err)
	}
	return result.Status, nil
}

func parseOrderResult(data []byte) (*OrderResult, error) {
	// Response may be wrapped: {"orderID":"...", "status":"...", ...}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse order result: %w", err)
	}

	result := &OrderResult{}

	if v, ok := raw["orderID"].(string); ok {
		result.OrderID = v
	} else if v, ok := raw["order_id"].(string); ok {
		result.OrderID = v
	}

	if v, ok := raw["status"].(string); ok {
		result.Status = v
	}

	if v, ok := raw["size_matched"].(string); ok {
		result.FilledAmt, _ = strconv.ParseFloat(v, 64)
	}

	_ = bytes.NewReader(data) // suppress unused import
	return result, nil
}
