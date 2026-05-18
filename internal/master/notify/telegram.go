package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultTelegramBaseURL = "https://api.telegram.org"

type TelegramConfig struct {
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
	BaseURL  string `json:"base_url,omitempty"`
}

type TelegramNotifier struct {
	botToken string
	chatID   string
	baseURL  string
	client   *http.Client
}

func NewTelegramNotifier(config TelegramConfig) *TelegramNotifier {
	baseURL := strings.TrimRight(config.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultTelegramBaseURL
	}

	return &TelegramNotifier{
		botToken: config.BotToken,
		chatID:   config.ChatID,
		baseURL:  baseURL,
		client:   &http.Client{Timeout: defaultHTTPTimeout},
	}
}

func (n *TelegramNotifier) Send(ctx context.Context, msg NotifyMessage) error {
	payload := map[string]string{
		"chat_id":    n.chatID,
		"text":       n.formatMessage(msg),
		"parse_mode": "HTML",
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal telegram message: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", n.baseURL, n.botToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return sanitizedSendError{op: "send telegram message", err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("telegram API error: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return nil
}

func (n *TelegramNotifier) Type() string {
	return "telegram"
}

func (n *TelegramNotifier) formatMessage(msg NotifyMessage) string {
	timestamp := msg.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}

	return fmt.Sprintf(
		"<b>%s</b>\nLevel: %s\nAgent: %s\nTime: %s\n\n%s",
		html.EscapeString(msg.Title),
		html.EscapeString(string(msg.Level)),
		html.EscapeString(msg.AgentName),
		html.EscapeString(timestamp.UTC().Format(time.RFC3339)),
		html.EscapeString(msg.Body),
	)
}
