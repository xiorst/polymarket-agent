package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/x10rst/ai-agent-autonom/internal/api"
	"github.com/x10rst/ai-agent-autonom/internal/api/handler"
	"github.com/x10rst/ai-agent-autonom/internal/blockchain"
	"github.com/x10rst/ai-agent-autonom/internal/config"
	"github.com/x10rst/ai-agent-autonom/internal/database"
	"github.com/x10rst/ai-agent-autonom/internal/feeds/scorer"
	"github.com/x10rst/ai-agent-autonom/internal/feeds/telegram"
	"github.com/x10rst/ai-agent-autonom/internal/logger"
	"github.com/x10rst/ai-agent-autonom/internal/market"
	"github.com/x10rst/ai-agent-autonom/internal/market/polymarket"
	"github.com/x10rst/ai-agent-autonom/internal/ml"
	"github.com/x10rst/ai-agent-autonom/internal/models"
	"github.com/x10rst/ai-agent-autonom/internal/notification"
	"github.com/x10rst/ai-agent-autonom/internal/reliability"
	"github.com/x10rst/ai-agent-autonom/internal/resolution"
	"github.com/x10rst/ai-agent-autonom/internal/risk"
	"github.com/x10rst/ai-agent-autonom/internal/scalper"
	"github.com/x10rst/ai-agent-autonom/internal/trading"
	"github.com/x10rst/ai-agent-autonom/internal/withdraw"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	logLevel := flag.String("log-level", "info", "log level (debug, info, warn, error)")
	flag.Parse()

	// Setup logger
	logger.Setup(*logLevel)

	// Load config
	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	slog.Info("config loaded",
		"trading_mode", cfg.Trading.Mode,
		"initial_balance", cfg.Trading.InitialBalance,
	)

	// Context with graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Connect to database
	db, err := database.Connect(ctx, cfg.Database)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	slog.Info("database connected")

	// Run migrations
	if err := database.Migrate(ctx, db); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}
	slog.Info("migrations applied")

	// Initialize notification system
	notifier := notification.New(cfg.Notification)

	// Initialize circuit breaker
	cb := reliability.NewCircuitBreaker(cfg.CircuitBreaker, notifier)

	// Initialize Polymarket provider (used in all modes for market data)
	marketProvider := polymarket.NewProvider(polymarket.ProviderConfig{
		APIKey: cfg.Blockchain.PolymarketAPIKey,
	})

	// Initialize market data collector
	collector := market.NewCollector(cfg.MarketAnalysis, db, marketProvider)

	// Initialize liquidity monitor
	liqMonitor := market.NewLiquidityMonitor(cfg.Liquidity, marketProvider, notifier)

	// Initialize ML predictor & pipeline
	predictor := ml.NewDefaultPredictor()
	mlPipeline := ml.NewPipeline(cfg.MarketAnalysis, db, predictor)

	// Initialize risk manager
	riskMgr := risk.NewManager(cfg.Risk, db, notifier)

	// Initialize executor, blockchain client, reconciler — mode-dependent
	var (
		executor     trading.Executor
		autoWithdraw *withdraw.AutoWithdrawer
		reconciler   *reliability.Reconciler
		chain        *blockchain.Client
	)

	switch cfg.Trading.Mode {
	case "paper":
		executor = trading.NewPaperExecutor()
		slog.Info("running in PAPER trading mode — no blockchain connection required")

	case "live":
		var err error
		chain, err = blockchain.NewClient(ctx, cfg.Blockchain)
		if err != nil {
			slog.Error("failed to initialize blockchain client", "error", err)
			os.Exit(1)
		}
		defer chain.Close()

		// Build contract provider with EIP-712 signer using the hot wallet private key
		contractProvider, err := polymarket.NewContractProviderWithSigner(
			cfg.Blockchain.CTFExchangeAddress, chain.PrivateKey(),
		)
		if err != nil {
			slog.Error("failed to initialize contract provider", "error", err)
			os.Exit(1)
		}
		executor = trading.NewLiveExecutor(chain, db, cfg.Blockchain, cfg.Nonce, notifier, contractProvider)

		reconciler = reliability.NewReconciler(cfg.Reconciliation, db, chain, notifier)
		autoWithdraw = withdraw.NewAutoWithdrawer(cfg.AutoWithdraw, cfg.Trading, db, chain, notifier)

		if err := reconciler.RunOnStartup(ctx); err != nil {
			slog.Error("startup reconciliation failed", "error", err)
		}
		slog.Info("running in LIVE trading mode", "address", chain.Address())

	default:
		slog.Error("unsupported trading mode", "mode", cfg.Trading.Mode)
		os.Exit(1)
	}

	// Initialize trading engine
	engine := trading.NewEngine(
		cfg.Trading, cfg.Risk, db, executor, riskMgr, cb, liqMonitor, notifier, mlPipeline, marketProvider,
	)

	// Initialize resolution handler — only meaningful in live mode with a resolver
	resolutionHandler := resolution.NewHandler(cfg.Resolution, db, nil, notifier)
	_ = resolutionHandler

	// Agent state for API
	agentState := &handler.AgentState{
		Status:    models.AgentRunning,
		StartedAt: time.Now(),
	}

	// Setup HTTP API
	h := handler.New(db, cfg, cb, agentState)
	router := api.NewRouter(h, cfg.Server.APIKey)

	server := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start background services
	go func() {
		slog.Info("HTTP server starting", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server error", "error", err)
		}
	}()

	// Start market data collector
	go collector.Run(ctx)

	// Start liquidity monitor — queries active external market IDs from DB each tick
	go liqMonitor.Run(ctx, func() []string {
		rows, err := db.Query(ctx,
			"SELECT external_id FROM markets WHERE status = 'active' LIMIT 200")
		if err != nil {
			slog.Warn("liquidity monitor: failed to query active markets", "error", err)
			return nil
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				ids = append(ids, id)
			}
		}
		return ids
	})

	// Start live-only background services
	if cfg.Trading.Mode == "live" {
		if reconciler != nil {
			go reconciler.Run(ctx)
		}
		if autoWithdraw != nil {
			go autoWithdraw.Run(ctx)
		}
	}

	// Start trading loop
	go func() {
		tradingInterval := time.Duration(cfg.MarketAnalysis.PollIntervalSeconds) * time.Second
		ticker := time.NewTicker(tradingInterval)
		defer ticker.Stop()

		slog.Info("trading loop started", "interval", tradingInterval)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if agentState.Status != models.AgentRunning {
					continue
				}
				if err := engine.RunCycle(ctx); err != nil {
					slog.Error("trading cycle error", "error", err)
				}
			}
		}
	}()

	// Start scalper engine (BTC 5m prediction market scalper)
	if cfg.Scalper.Enabled {
		scalperCfg := &scalper.Config{
			Enabled:           cfg.Scalper.Enabled,
			SeriesSlug:        cfg.Scalper.SeriesSlug,
			TradeSize:         cfg.Scalper.TradeSize,
			TotalCapital:      cfg.Scalper.TotalCapital,
			TakeProfitMin:     cfg.Scalper.TakeProfitMin,
			TakeProfitMax:     cfg.Scalper.TakeProfitMax,
			StopLoss:          cfg.Scalper.StopLoss,
			MomentumThreshold: cfg.Scalper.MomentumThreshold,
			APIKey:            cfg.Scalper.APIKey,
			APISecret:         cfg.Scalper.APISecret,
			APIPassphrase:     cfg.Scalper.APIPassphrase,
			BuilderAddress:    cfg.Scalper.BuilderAddress,
			SignerAddress:     cfg.Scalper.SignerAddress,
		}
		scalperEngine := scalper.NewEngine(scalperCfg)
		go func() {
			if err := scalperEngine.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("scalper engine stopped", "error", err)
			}
		}()
		slog.Info("scalper engine started", "series", scalperCfg.SeriesSlug)
	}

	// Start Telegram feed (external news context for ML predictor)
	if cfg.TelegramFeed.Enabled {
		feedCfg := telegram.FeedConfig{
			APIID:        cfg.TelegramFeed.APIID,
			APIHash:      cfg.TelegramFeed.APIHash,
			Phone:        cfg.TelegramFeed.Phone,
			SessionFile:  cfg.TelegramFeed.SessionFile,
			Channels:     cfg.TelegramFeed.Channels,
			PollInterval: time.Duration(cfg.TelegramFeed.PollIntervalSeconds) * time.Second,
		}

		feed := telegram.NewFeed(feedCfg, func(msg telegram.Message) *telegram.ExternalSignal {
			sig := scorer.ScoreText(msg.Text, msg.ChannelUsername)
			if sig == nil {
				return nil
			}
			return &telegram.ExternalSignal{
				Category:   telegram.Category(sig.Category),
				Sentiment:  telegram.Sentiment(sig.Sentiment),
				Confidence: sig.Confidence,
				Keywords:   sig.Keywords,
				Source:     sig.Source,
				RawText:    sig.RawText,
				CreatedAt:  sig.CreatedAt,
			}
		})

		// Run feed in background
		go func() {
			if err := feed.Run(ctx); err != nil && err != context.Canceled {
				slog.Error("telegram feed error", "error", err)
			}
		}()

		// Ingest signals into ML pipeline
		go func() {
			sigCh := feed.Signals(ctx)
			for {
				select {
				case <-ctx.Done():
					return
				case sig, ok := <-sigCh:
					if !ok {
						return
					}
					mlPipeline.IngestExternalSignal(scorer.ExternalSignal{
						Category:   scorer.Category(sig.Category),
						Sentiment:  scorer.Sentiment(sig.Sentiment),
						Confidence: sig.Confidence,
						Keywords:   sig.Keywords,
						Source:     sig.Source,
						RawText:    sig.RawText,
						CreatedAt:  sig.CreatedAt,
					})
				}
			}
		}()

		slog.Info("telegram feed started",
			"channels", cfg.TelegramFeed.Channels,
			"poll_interval", cfg.TelegramFeed.PollIntervalSeconds,
		)
	} else {
		slog.Info("telegram feed disabled — set telegram_feed.enabled=true to activate")
	}

	// Send startup notification
	notifier.Send(ctx, models.AlertInfo, "agent_started",
		fmt.Sprintf("Agent started in %s mode. Balance: $%.2f",
			cfg.Trading.Mode, cfg.Trading.InitialBalance))

	slog.Info("agent is running",
		"mode", cfg.Trading.Mode,
		"api_addr", server.Addr,
	)

	// Wait for shutdown signal
	sig := <-sigCh
	slog.Info("shutdown signal received", "signal", sig)

	// Graceful shutdown
	agentState.Status = models.AgentStopped

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("HTTP server shutdown error", "error", err)
	}

	cancel() // cancel all background goroutines

	notifier.Send(shutdownCtx, models.AlertInfo, "agent_stopped", "Agent stopped gracefully")

	slog.Info("agent stopped")
}
