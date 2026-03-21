// cmd/tg-auth/main.go — standalone Telegram auth helper.
// Supports OTP + 2FA cloud password.
// Run once to authenticate and save session file.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"golang.org/x/term"
)

// interactiveAuth implements auth.UserAuthenticator with OTP + 2FA support.
type interactiveAuth struct {
	phone string
}

func (a *interactiveAuth) Phone(_ context.Context) (string, error) {
	return a.phone, nil
}

func (a *interactiveAuth) Password(_ context.Context) (string, error) {
	fmt.Print("Enter 2FA cloud password: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		var pwd string
		_, err = fmt.Scanln(&pwd)
		return strings.TrimSpace(pwd), err
	}
	return strings.TrimSpace(string(b)), nil
}

func (a *interactiveAuth) Code(_ context.Context, _ *tg.AuthSentCode) (string, error) {
	fmt.Print("Enter OTP code: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		var code string
		_, err = fmt.Scanln(&code)
		return strings.TrimSpace(code), err
	}
	return strings.TrimSpace(string(b)), nil
}

func (a *interactiveAuth) AcceptTermsOfService(_ context.Context, tos tg.HelpTermsOfService) error {
	fmt.Printf("Terms of Service: %s\nAccepted automatically.\n", tos.Text)
	return nil
}

func (a *interactiveAuth) SignUp(_ context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, fmt.Errorf("sign up not supported — use existing account")
}

func main() {
	apiIDStr := os.Getenv("AGENT_TELEGRAM_FEED_API_ID")
	apiHash := os.Getenv("AGENT_TELEGRAM_FEED_API_HASH")
	phone := os.Getenv("AGENT_TELEGRAM_FEED_PHONE")
	sessionFile := os.Getenv("AGENT_TELEGRAM_FEED_SESSION_FILE")

	if sessionFile == "" {
		sessionFile = "./data/tg_session.json"
	}

	if apiIDStr == "" || apiHash == "" || phone == "" {
		fmt.Println("ERROR: Set AGENT_TELEGRAM_FEED_API_ID, AGENT_TELEGRAM_FEED_API_HASH, AGENT_TELEGRAM_FEED_PHONE")
		os.Exit(1)
	}

	apiID, err := strconv.Atoi(apiIDStr)
	if err != nil {
		fmt.Printf("ERROR: invalid API_ID: %v\n", err)
		os.Exit(1)
	}

	// Ensure session directory exists
	lastSlash := strings.LastIndex(sessionFile, "/")
	if lastSlash > 0 {
		dir := sessionFile[:lastSlash]
		if err := os.MkdirAll(dir, 0700); err != nil {
			fmt.Printf("ERROR: create session dir: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("🔐 Telegram Auth\n")
	fmt.Printf("   Phone   : %s\n", phone)
	fmt.Printf("   Session : %s\n", sessionFile)
	fmt.Println()

	client := telegram.NewClient(apiID, apiHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: sessionFile},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	err = client.Run(ctx, func(ctx context.Context) error {
		authClient := client.Auth()

		status, err := authClient.Status(ctx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}

		if status.Authorized {
			fmt.Println("✅ Already authorized! Session valid.")
			return nil
		}

		fmt.Println("📱 Sending OTP to", phone, "...")

		flow := auth.NewFlow(&interactiveAuth{phone: phone}, auth.SendCodeOptions{})
		if err := authClient.IfNecessary(ctx, flow); err != nil {
			return fmt.Errorf("auth failed: %w", err)
		}

		fmt.Println("✅ Auth successful! Session saved to:", sessionFile)
		return nil
	})

	if err != nil && err != context.Canceled {
		fmt.Printf("ERROR: %v\n", err)
		os.Exit(1)
	}
}
