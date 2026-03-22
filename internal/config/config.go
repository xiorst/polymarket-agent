package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server         ServerConfig         `yaml:"server"`
	Database       DatabaseConfig       `yaml:"database"`
	Blockchain     BlockchainConfig     `yaml:"blockchain"`
	Trading        TradingConfig        `yaml:"trading"`
	Risk           RiskConfig           `yaml:"risk"`
	MarketAnalysis MarketAnalysisConfig `yaml:"market_analysis"`
	Liquidity      LiquidityConfig      `yaml:"liquidity"`
	AutoWithdraw   AutoWithdrawConfig   `yaml:"auto_withdraw"`
	Resolution     ResolutionConfig     `yaml:"resolution"`
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
	Nonce          NonceConfig          `yaml:"nonce"`
	Reconciliation ReconciliationConfig `yaml:"reconciliation"`
	Notification   NotificationConfig   `yaml:"notification"`
	MultiVenue     MultiVenueConfig     `yaml:"multi_venue"`
	TelegramFeed   TelegramFeedConfig   `yaml:"telegram_feed"`
}

type ServerConfig struct {
	Host   string `yaml:"host"`
	Port   int    `yaml:"port"`
	APIKey string `yaml:"api_key"` // Use env: AGENT_SERVER_API_KEY
}

type DatabaseConfig struct {
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	User         string `yaml:"user"`
	Password     string `yaml:"password"`
	Name         string `yaml:"name"`
	SSLMode      string `yaml:"sslmode"`
	MaxOpenConns int    `yaml:"max_open_conns"`
	MaxIdleConns int    `yaml:"max_idle_conns"`
}

func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode,
	)
}

type BlockchainConfig struct {
	RPCURL               string  `yaml:"rpc_url"`
	ChainID              int64   `yaml:"chain_id"`
	PrivateKey           string  `yaml:"private_key"`
	USDCContract         string  `yaml:"usdc_contract"`
	CTFExchangeAddress   string  `yaml:"ctf_exchange_address"`
	PolymarketAPIKey     string  `yaml:"polymarket_api_key"`
	GasEstimationMethod  string  `yaml:"gas_estimation_method"`
	MaxGasPriceGwei      float64 `yaml:"max_gas_price_gwei"`
	GasPriceBuffer       float64 `yaml:"gas_price_buffer"`
}

type TradingConfig struct {
	Mode              string  `yaml:"mode"`
	InitialBalance    float64 `yaml:"initial_balance"`
	DailyProfitTarget float64 `yaml:"daily_profit_target"`
	DefaultOrderType  string  `yaml:"default_order_type"`
}

type RiskConfig struct {
	MaxSlippageTolerance    float64 `yaml:"max_slippage_tolerance"`
	StopLoss                float64 `yaml:"stop_loss"`
	MaxPositionPerMarket    float64 `yaml:"max_position_per_market"`
	MaxTotalExposure        float64 `yaml:"max_total_exposure"`
	DailyLossLimit          float64 `yaml:"daily_loss_limit"`
	MaxPriceImpactThreshold float64 `yaml:"max_price_impact_threshold"`
	PriceImpactAutoSplit    bool    `yaml:"price_impact_auto_split"`
	OrderSplitThreshold     float64 `yaml:"order_split_threshold"`
	OrderSplitMaxChunks     int     `yaml:"order_split_max_chunks"`
	OrderSplitDelayMs       int     `yaml:"order_split_delay_ms"`
}

type MarketAnalysisConfig struct {
	PollIntervalSeconds int     `yaml:"poll_interval_seconds"`
	ConfidenceThreshold float64 `yaml:"confidence_threshold"`
	MispricingThreshold float64 `yaml:"mispricing_threshold"`
}

type LiquidityConfig struct {
	SpreadCheckInterval       int     `yaml:"spread_check_interval"`
	SpreadWarningMultiplier   float64 `yaml:"spread_warning_multiplier"`
	SpreadHaltMultiplier      float64 `yaml:"spread_halt_multiplier"`
	SpreadNormalizationWindow int     `yaml:"spread_normalization_window"`
	VolumeAwareExecution      bool    `yaml:"volume_aware_execution"`
	VolumePercentileMin       int     `yaml:"volume_percentile_min"`
	VolumeQueueTimeout        int     `yaml:"volume_queue_timeout"`
	VolumeProfileWindow       int     `yaml:"volume_profile_window"`
}

type AutoWithdrawConfig struct {
	Enabled              bool    `yaml:"enabled"`
	SafeWalletAddress    string  `yaml:"safe_wallet_address"`
	MinHotWalletBalance  float64 `yaml:"min_hot_wallet_balance"`
	CheckIntervalSeconds int     `yaml:"check_interval_seconds"`
}

type ResolutionConfig struct {
	CheckIntervalSeconds int `yaml:"check_interval_seconds"`
	ClaimMaxRetries      int `yaml:"claim_max_retries"`
}

type CircuitBreakerConfig struct {
	Enabled                 bool    `yaml:"enabled"`
	CooldownSeconds         int     `yaml:"cooldown_seconds"`
	MaxConsecutiveFailures  int     `yaml:"max_consecutive_failures"`
	RapidDropThreshold      float64 `yaml:"rapid_drop_threshold"`
	RapidDropWindowSeconds  int     `yaml:"rapid_drop_window_seconds"`
}

type NonceConfig struct {
	StuckTimeoutSeconds        int     `yaml:"stuck_timeout_seconds"`
	ReplacementGasMultiplier   float64 `yaml:"replacement_gas_multiplier"`
	MaxPendingTransactions     int     `yaml:"max_pending_transactions"`
}

type ReconciliationConfig struct {
	OnStartup       bool    `yaml:"on_startup"`
	IntervalSeconds int     `yaml:"interval_seconds"`
	Tolerance       float64 `yaml:"tolerance"`
}

type NotificationConfig struct {
	TelegramBotToken     string `yaml:"telegram_bot_token"`
	TelegramChatID       string `yaml:"telegram_chat_id"`
	EmailEnabled         bool   `yaml:"email_enabled"`
	AlertCooldownSeconds int    `yaml:"alert_cooldown_seconds"`
}

type MultiVenueConfig struct {
	Enabled           bool `yaml:"enabled"`
	MinVenuesForSplit int  `yaml:"min_venues_for_split"`
}

type TelegramFeedConfig struct {
	Enabled             bool     `yaml:"enabled"`
	APIID               int      `yaml:"api_id"`
	APIHash             string   `yaml:"api_hash"`
	Phone               string   `yaml:"phone"`
	SessionFile         string   `yaml:"session_file"`
	PollIntervalSeconds int      `yaml:"poll_interval_seconds"`
	Channels            []string `yaml:"channels"`
}

// Load reads config from YAML file and overrides with environment variables.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Override with environment variables
	cfg.applyEnvOverrides()

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return cfg, nil
}

func (c *Config) applyEnvOverrides() {
	if v := os.Getenv("AGENT_DATABASE_PASSWORD"); v != "" {
		c.Database.Password = v
	}
	if v := os.Getenv("AGENT_DATABASE_HOST"); v != "" {
		c.Database.Host = v
	}
	if v := os.Getenv("AGENT_DATABASE_PORT"); v != "" {
		fmt.Sscanf(v, "%d", &c.Database.Port)
	}
	if v := os.Getenv("AGENT_BLOCKCHAIN_PRIVATE_KEY"); v != "" {
		c.Blockchain.PrivateKey = v
	}
	if v := os.Getenv("AGENT_BLOCKCHAIN_RPC_URL"); v != "" {
		c.Blockchain.RPCURL = v
	}
	if v := os.Getenv("AGENT_AUTO_WITHDRAW_SAFE_WALLET_ADDRESS"); v != "" {
		c.AutoWithdraw.SafeWalletAddress = v
	}
	if v := os.Getenv("AGENT_NOTIFICATION_TELEGRAM_BOT_TOKEN"); v != "" {
		c.Notification.TelegramBotToken = v
	}
	if v := os.Getenv("AGENT_NOTIFICATION_TELEGRAM_CHAT_ID"); v != "" {
		c.Notification.TelegramChatID = v
	}
	if v := os.Getenv("AGENT_TRADING_MODE"); v != "" {
		c.Trading.Mode = v
	}
	if v := os.Getenv("AGENT_TELEGRAM_FEED_API_HASH"); v != "" {
		c.TelegramFeed.APIHash = v
	}
	if v := os.Getenv("AGENT_TELEGRAM_FEED_PHONE"); v != "" {
		c.TelegramFeed.Phone = v
	}
	if v := os.Getenv("AGENT_TELEGRAM_FEED_API_ID"); v != "" {
		fmt.Sscanf(v, "%d", &c.TelegramFeed.APIID)
	}
	if v := os.Getenv("AGENT_TELEGRAM_FEED_ENABLED"); v == "true" {
		c.TelegramFeed.Enabled = true
	}
	if v := os.Getenv("AGENT_SERVER_API_KEY"); v != "" {
		c.Server.APIKey = v
	}
	if v := os.Getenv("AGENT_BLOCKCHAIN_POLYMARKET_API_KEY"); v != "" {
		c.Blockchain.PolymarketAPIKey = v
	}
	if v := os.Getenv("AGENT_BLOCKCHAIN_CTF_EXCHANGE_ADDRESS"); v != "" {
		c.Blockchain.CTFExchangeAddress = v
	}
}

func (c *Config) validate() error {
	validModes := map[string]bool{"live": true, "paper": true, "backtest": true}
	if !validModes[c.Trading.Mode] {
		return fmt.Errorf("invalid trading mode: %q (must be live, paper, or backtest)", c.Trading.Mode)
	}

	if c.Trading.Mode == "live" {
		if c.Blockchain.PrivateKey == "" {
			return fmt.Errorf("blockchain.private_key is required in live mode")
		}
		if c.AutoWithdraw.Enabled && c.AutoWithdraw.SafeWalletAddress == "" {
			return fmt.Errorf("auto_withdraw.safe_wallet_address is required when auto_withdraw is enabled")
		}
		// Validate private key format (must be hex, 64 chars without 0x prefix)
		pk := strings.TrimPrefix(c.Blockchain.PrivateKey, "0x")
		if len(pk) != 64 {
			return fmt.Errorf("blockchain.private_key must be a 64-character hex string")
		}
	}

	if c.Risk.MaxSlippageTolerance <= 0 || c.Risk.MaxSlippageTolerance > 1 {
		return fmt.Errorf("risk.max_slippage_tolerance must be between 0 and 1")
	}

	if c.Risk.DailyLossLimit <= 0 || c.Risk.DailyLossLimit > 1 {
		return fmt.Errorf("risk.daily_loss_limit must be between 0 and 1")
	}

	return nil
}
