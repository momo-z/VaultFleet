package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type WebhookConfig struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

type WebhookNotifier struct {
	url     string
	headers map[string]string
	client  *http.Client
}

func NewWebhookNotifier(config WebhookConfig) *WebhookNotifier {
	headers := make(map[string]string, len(config.Headers))
	for key, value := range config.Headers {
		headers[key] = value
	}

	return &WebhookNotifier{
		url:     config.URL,
		headers: headers,
		client:  &http.Client{Timeout: defaultHTTPTimeout},
	}
}

func (n *WebhookNotifier) Send(ctx context.Context, msg NotifyMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal webhook message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range n.headers {
		req.Header.Set(key, value)
	}

	resp, err := n.client.Do(req)
	if err != nil {
		return sanitizedSendError{op: "send webhook message", err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("webhook returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return nil
}

func (n *WebhookNotifier) Type() string {
	return "webhook"
}

func validateWebhookURL(rawURL string) error {
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return fmt.Errorf("invalid webhook url")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("webhook url must use http or https")
	}
	if parsed.Host == "" {
		return fmt.Errorf("webhook url host is required")
	}
	return nil
}

func validateWebhookHeaders(headers map[string]string) error {
	for key, value := range headers {
		if !isValidHTTPHeaderName(key) {
			return fmt.Errorf("invalid webhook header name %q", key)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("invalid webhook header value for %q", key)
		}
	}
	return nil
}

func isValidHTTPHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, ch := range name {
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= 'A' && ch <= 'Z':
		case ch >= '0' && ch <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", ch):
		default:
			return false
		}
	}
	return true
}
