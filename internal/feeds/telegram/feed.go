package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

// Message represents a parsed message from a Telegram channel.
type Message struct {
	ChannelUsername string
	ChannelTitle    string
	Text            string
	ReceivedAt      time.Time
	MessageID       int
}

// FeedConfig holds configuration for the Telegram feed.
type FeedConfig struct {
	APIID        int
	APIHash      string
	Phone        string
	SessionFile  string
	Channels     []string // channel usernames to monitor (without @)
	PollInterval time.Duration
}

// Feed monitors Telegram channels and emits messages for processing.
type Feed struct {
	cfg      FeedConfig
	client   *telegram.Client
	msgCh    chan Message
	scorerFn func(Message) *ExternalSignal
}

// NewFeed creates a new Telegram feed monitor.
func NewFeed(cfg FeedConfig, scorerFn func(Message) *ExternalSignal) *Feed {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 90 * time.Second
	}
	if cfg.SessionFile == "" {
		cfg.SessionFile = "./data/tg_session.json"
	}

	return &Feed{
		cfg:      cfg,
		msgCh:    make(chan Message, 100),
		scorerFn: scorerFn,
	}
}

// Run starts the Telegram feed — blocks until ctx is cancelled.
// On first run, will prompt for OTP via stdin.
func (f *Feed) Run(ctx context.Context) error {
	// Ensure session directory exists
	sessionDir := sessionDir(f.cfg.SessionFile)
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}

	sessionStorage := &session.FileStorage{Path: f.cfg.SessionFile}

	f.client = telegram.NewClient(f.cfg.APIID, f.cfg.APIHash, telegram.Options{
		SessionStorage: sessionStorage,
	})

	return f.client.Run(ctx, func(ctx context.Context) error {
		// Auth flow
		authClient := f.client.Auth()
		status, err := authClient.Status(ctx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}

		if !status.Authorized {
			slog.Info("telegram: not authorized, starting auth flow")
			if err := f.authenticate(ctx, authClient); err != nil {
				return fmt.Errorf("authenticate: %w", err)
			}
		}

		slog.Info("telegram: authorized, starting channel monitor",
			"channels", f.cfg.Channels,
			"poll_interval", f.cfg.PollInterval,
		)

		return f.pollLoop(ctx)
	})
}

// Signals returns the channel where ExternalSignals are emitted.
func (f *Feed) Signals(ctx context.Context) <-chan ExternalSignal {
	sigCh := make(chan ExternalSignal, 50)

	go func() {
		defer close(sigCh)
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-f.msgCh:
				if !ok {
					return
				}
				if f.scorerFn == nil {
					continue
				}
				signal := f.scorerFn(msg)
				if signal != nil {
					select {
					case sigCh <- *signal:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()

	return sigCh
}

// authenticate handles the Telegram login flow via phone + OTP.
func (f *Feed) authenticate(ctx context.Context, authClient *telegram.Client) error {
	flow := auth.NewFlow(
		auth.CodeOnly(f.cfg.Phone, auth.CodeAuthenticatorFunc(func(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
			fmt.Print("Enter Telegram OTP code: ")
			var code string
			_, err := fmt.Scanln(&code)
			return strings.TrimSpace(code), err
		})),
		auth.SendCodeOptions{},
	)
	return authClient.IfNecessary(ctx, flow)
}

// pollLoop fetches recent messages from each monitored channel periodically.
func (f *Feed) pollLoop(ctx context.Context) error {
	ticker := time.NewTicker(f.cfg.PollInterval)
	defer ticker.Stop()

	// Track last seen message ID per channel to avoid duplicates
	lastSeen := make(map[string]int)

	// Initial fetch on startup
	f.fetchAll(ctx, lastSeen)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			f.fetchAll(ctx, lastSeen)
		}
	}
}

// fetchAll fetches recent messages from all configured channels.
func (f *Feed) fetchAll(ctx context.Context, lastSeen map[string]int) {
	api := f.client.API()

	for _, username := range f.cfg.Channels {
		if err := f.fetchChannel(ctx, api, username, lastSeen); err != nil {
			slog.Warn("telegram: failed to fetch channel",
				"channel", username,
				"error", err,
			)
		}
	}
}

// fetchChannel fetches recent messages from a single channel.
func (f *Feed) fetchChannel(ctx context.Context, api *tg.Client, username string, lastSeen map[string]int) error {
	// Resolve channel
	resolved, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{
		Username: username,
	})
	if err != nil {
		return fmt.Errorf("resolve username %s: %w", username, err)
	}

	if len(resolved.Chats) == 0 {
		return fmt.Errorf("channel %s not found or not joined", username)
	}

	chat := resolved.Chats[0]
	channel, ok := chat.(*tg.Channel)
	if !ok {
		return fmt.Errorf("%s is not a channel", username)
	}

	inputChannel := &tg.InputChannel{
		ChannelID:  channel.ID,
		AccessHash: channel.AccessHash,
	}

	// Fetch last 20 messages
	history, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit: 20,
	})
	if err != nil {
		return fmt.Errorf("get history %s: %w", username, err)
	}
	_ = inputChannel

	messages, ok := history.(*tg.MessagesChannelMessages)
	if !ok {
		return nil
	}

	minLastSeen := lastSeen[username]

	for _, raw := range messages.Messages {
		msg, ok := raw.(*tg.Message)
		if !ok || msg.Message == "" {
			continue
		}

		// Skip already-seen messages
		if msg.ID <= minLastSeen {
			continue
		}

		slog.Debug("telegram: new message",
			"channel", username,
			"msg_id", msg.ID,
			"text_preview", truncate(msg.Message, 80),
		)

		select {
		case f.msgCh <- Message{
			ChannelUsername: username,
			ChannelTitle:    channel.Title,
			Text:            msg.Message,
			ReceivedAt:      time.Now(),
			MessageID:       msg.ID,
		}:
		default:
			slog.Warn("telegram: message channel buffer full, dropping message")
		}

		if msg.ID > lastSeen[username] {
			lastSeen[username] = msg.ID
		}
	}

	return nil
}

func sessionDir(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) <= 1 {
		return "."
	}
	return strings.Join(parts[:len(parts)-1], "/")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
