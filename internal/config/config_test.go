package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

const validConfig = `
server:
  host: "0.0.0.0"
  port: 8080
database:
  host: "localhost"
  port: 5432
  user: "agent"
  password: "test"
  name: "trading_agent"
  sslmode: "disable"
  max_open_conns: 10
  max_idle_conns: 2
blockchain:
  rpc_url: "https://polygon-rpc.com"
  chain_id: 137
  private_key: ""
  usdc_contract: "0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174"
  gas_estimation_method: "eip1559"
  max_gas_price_gwei: 100.0
  gas_price_buffer: 1.2
trading:
  mode: "paper"
  initial_balance: 10.00
  daily_profit_target: 0.25
  default_order_type: "limit"
risk:
  max_slippage_tolerance: 0.05
  stop_loss: 0.15
  max_position_per_market: 0.10
  max_total_exposure: 0.70
  daily_loss_limit: 0.05
  max_price_impact_threshold: 0.03
  price_impact_auto_split: true
  order_split_threshold: 100
  order_split_max_chunks: 5
  order_split_delay_ms: 2000
market_analysis:
  poll_interval_seconds: 30
  confidence_threshold: 0.65
  mispricing_threshold: 0.05
liquidity:
  spread_check_interval: 15
  spread_warning_multiplier: 2.0
  spread_halt_multiplier: 4.0
  spread_normalization_window: 3600
  volume_aware_execution: true
  volume_percentile_min: 50
  volume_queue_timeout: 1800
  volume_profile_window: 168
auto_withdraw:
  enabled: false
  safe_wallet_address: ""
  min_hot_wallet_balance: 10.00
  check_interval_seconds: 300
resolution:
  check_interval_seconds: 300
  claim_max_retries: 5
circuit_breaker:
  enabled: true
  cooldown_seconds: 600
  max_consecutive_failures: 3
  rapid_drop_threshold: 0.10
  rapid_drop_window_seconds: 300
nonce:
  stuck_timeout_seconds: 180
  replacement_gas_multiplier: 1.3
  max_pending_transactions: 3
reconciliation:
  on_startup: true
  interval_seconds: 900
  tolerance: 0.01
notification:
  telegram_bot_token: ""
  telegram_chat_id: ""
  email_enabled: false
  alert_cooldown_seconds: 60
multi_venue:
  enabled: false
  min_venues_for_split: 2
`

func TestLoad_ValidConfig(t *testing.T) {
	path := writeTestConfig(t, validConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Trading.Mode != "paper" {
		t.Errorf("expected mode paper, got %s", cfg.Trading.Mode)
	}
	if cfg.Trading.InitialBalance != 10.0 {
		t.Errorf("expected initial balance 10.0, got %f", cfg.Trading.InitialBalance)
	}
	if cfg.Risk.StopLoss != 0.15 {
		t.Errorf("expected stop loss 0.15, got %f", cfg.Risk.StopLoss)
	}
	if cfg.Blockchain.ChainID != 137 {
		t.Errorf("expected chain ID 137 (Polygon), got %d", cfg.Blockchain.ChainID)
	}
}

func TestLoad_InvalidTradingMode(t *testing.T) {
	config := `
trading:
  mode: "invalid_mode"
risk:
  max_slippage_tolerance: 0.05
  daily_loss_limit: 0.05
`
	path := writeTestConfig(t, config)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for invalid trading mode")
	}
}

func TestLoad_InvalidSlippageTolerance(t *testing.T) {
	config := `
trading:
  mode: "paper"
risk:
  max_slippage_tolerance: 1.5
  daily_loss_limit: 0.05
`
	path := writeTestConfig(t, config)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for slippage tolerance > 1")
	}
}

func TestLoad_InvalidDailyLossLimit(t *testing.T) {
	config := `
trading:
  mode: "paper"
risk:
  max_slippage_tolerance: 0.05
  daily_loss_limit: 0
`
	path := writeTestConfig(t, config)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for daily loss limit = 0")
	}
}

func TestLoad_LiveModeRequiresPrivateKey(t *testing.T) {
	config := `
trading:
  mode: "live"
blockchain:
  private_key: ""
risk:
  max_slippage_tolerance: 0.05
  daily_loss_limit: 0.05
`
	path := writeTestConfig(t, config)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for live mode without private key")
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	path := writeTestConfig(t, validConfig)

	t.Setenv("AGENT_TRADING_MODE", "backtest")
	t.Setenv("AGENT_DATABASE_PASSWORD", "secret123")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Trading.Mode != "backtest" {
		t.Errorf("expected mode backtest from env, got %s", cfg.Trading.Mode)
	}
	if cfg.Database.Password != "secret123" {
		t.Errorf("expected password from env, got %s", cfg.Database.Password)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Error("expected error for nonexistent config file")
	}
}

func TestDSN(t *testing.T) {
	cfg := DatabaseConfig{
		Host:     "localhost",
		Port:     5432,
		User:     "agent",
		Password: "pass",
		Name:     "testdb",
		SSLMode:  "disable",
	}

	dsn := cfg.DSN()
	expected := "host=localhost port=5432 user=agent password=pass dbname=testdb sslmode=disable"
	if dsn != expected {
		t.Errorf("DSN mismatch:\ngot:  %s\nwant: %s", dsn, expected)
	}
}
