package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/x10rst/ai-agent-autonom/internal/config"
	"github.com/x10rst/ai-agent-autonom/internal/models"
)

type Notifier struct {
	cfg           config.NotificationConfig
	httpClient    *http.Client
	lastAlertTime map[string]time.Time
	mu            sync.Mutex
}

func New(cfg config.NotificationConfig) *Notifier {
	return &Notifier{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		lastAlertTime: make(map[string]time.Time),
	}
}

// Send delivers a notification based on severity.
func (n *Notifier) Send(ctx context.Context, severity models.AlertSeverity, eventType, message string) error {
	// Cooldown check
	n.mu.Lock()
	key := fmt.Sprintf("%s:%s", severity, eventType)
	if lastTime, ok := n.lastAlertTime[key]; ok {
		if time.Since(lastTime) < time.Duration(n.cfg.AlertCooldownSeconds)*time.Second {
			n.mu.Unlock()
			slog.Debug("alert cooldown active, skipping", "event", eventType)
			return nil
		}
	}
	n.lastAlertTime[key] = time.Now()
	n.mu.Unlock()

	// Format message
	icon := severityIcon(severity)
	text := fmt.Sprintf("%s *[%s] %s*\n\n%s", icon, severity, eventType, message)

	// Send to Telegram
	if n.cfg.TelegramBotToken != "" && n.cfg.TelegramChatID != "" {
		if err := n.sendTelegram(ctx, text); err != nil {
			slog.Error("failed to send telegram notification", "error", err)
			return err
		}
	}

	slog.Info("notification sent", "severity", severity, "event", eventType)
	return nil
}

func (n *Notifier) sendTelegram(ctx context.Context, text string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", n.cfg.TelegramBotToken)

	payload := map[string]string{
		"chat_id":    n.cfg.TelegramChatID,
		"text":       text,
		"parse_mode": "Markdown",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal telegram payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send telegram request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram API returned status %d", resp.StatusCode)
	}

	return nil
}

func severityIcon(s models.AlertSeverity) string {
	switch s {
	case models.AlertCritical:
		return "🚨"
	case models.AlertHigh:
		return "⚠️"
	case models.AlertMedium:
		return "📊"
	case models.AlertLow:
		return "✅"
	default:
		return "ℹ️"
	}
}
