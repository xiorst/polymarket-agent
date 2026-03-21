// cmd/tg-test/main.go — test Telegram feed read-only.
// Connects using saved session, fetches last 5 messages from each channel, scores them.
// No database required.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/x10rst/ai-agent-autonom/internal/feeds/scorer"
)

func main() {
	apiIDStr := os.Getenv("AGENT_TELEGRAM_FEED_API_ID")
	apiHash := os.Getenv("AGENT_TELEGRAM_FEED_API_HASH")
	sessionFile := os.Getenv("AGENT_TELEGRAM_FEED_SESSION_FILE")

	if sessionFile == "" {
		sessionFile = "./data/tg_session.json"
	}
	if apiIDStr == "" || apiHash == "" {
		fmt.Println("ERROR: Set AGENT_TELEGRAM_FEED_API_ID and AGENT_TELEGRAM_FEED_API_HASH")
		os.Exit(1)
	}

	apiID, err := strconv.Atoi(apiIDStr)
	if err != nil {
		fmt.Printf("ERROR: invalid API_ID: %v\n", err)
		os.Exit(1)
	}

	// Channels to test — default: marketfeed
	channels := []string{"marketfeed"}
	if len(os.Args) > 1 {
		channels = os.Args[1:]
	}

	fmt.Println("🔍 Telegram Feed Test")
	fmt.Printf("   Session  : %s\n", sessionFile)
	fmt.Printf("   Channels : %v\n\n", channels)

	client := telegram.NewClient(apiID, apiHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: sessionFile},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err = client.Run(ctx, func(ctx context.Context) error {
		api := client.API()

		// Verify auth
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}
		if !status.Authorized {
			return fmt.Errorf("not authorized — run tg-auth first")
		}
		fmt.Println("✅ Auth OK\n")

		for _, username := range channels {
			fmt.Printf("📡 Fetching: t.me/%s\n", username)
			fmt.Println(repeat("-", 60))

			// Resolve channel
			resolved, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{
				Username: username,
			})
			if err != nil {
				fmt.Printf("  ❌ Cannot resolve @%s: %v\n\n", username, err)
				continue
			}

			if len(resolved.Chats) == 0 {
				fmt.Printf("  ❌ Channel @%s not found or not joined\n\n", username)
				continue
			}

			channel, ok := resolved.Chats[0].(*tg.Channel)
			if !ok {
				fmt.Printf("  ❌ @%s is not a channel\n\n", username)
				continue
			}

			fmt.Printf("  Title  : %s\n", channel.Title)
			fmt.Printf("  ID     : %d\n\n", channel.ID)

			// Fetch last 5 messages
			history, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
				Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
				Limit: 5,
			})
			if err != nil {
				fmt.Printf("  ❌ Cannot fetch messages: %v\n\n", username, err)
				continue
			}

			messages, ok := history.(*tg.MessagesChannelMessages)
			if !ok {
				fmt.Printf("  ❌ Unexpected message type\n\n")
				continue
			}

			fmt.Printf("  Last %d messages:\n\n", len(messages.Messages))

			for i, raw := range messages.Messages {
				msg, ok := raw.(*tg.Message)
				if !ok || msg.Message == "" {
					continue
				}

				preview := msg.Message
				if len(preview) > 120 {
					preview = preview[:120] + "..."
				}

				fmt.Printf("  [%d] ID:%d\n", i+1, msg.ID)
				fmt.Printf("      %s\n", preview)

				// Score the message
				sig := scorer.ScoreText(msg.Message, username)
				if sig != nil {
					sentiment := "🟡 neutral"
					if sig.Sentiment > 0 {
						sentiment = "🟢 bullish"
					} else if sig.Sentiment < 0 {
						sentiment = "🔴 bearish"
					}
					fmt.Printf("      📊 %s | category=%-12s | confidence=%.2f | keywords=%v\n",
						sentiment, sig.Category, sig.Confidence, sig.Keywords)
				} else {
					fmt.Printf("      ⬜ no signal (irrelevant to Polymarket categories)\n")
				}
				fmt.Println()
			}
		}

		return nil
	})

	if err != nil && err != context.Canceled {
		fmt.Printf("ERROR: %v\n", err)
		os.Exit(1)
	}
}

func repeat(s string, n int) string {
	result := ""
	for i := 0; i < n; i++ {
		result += s
	}
	return result
}
