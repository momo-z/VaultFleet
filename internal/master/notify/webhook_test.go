package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebhookNotifierSendPostsNotifyMessage(t *testing.T) {
	var received map[string]any
	var headers http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		headers = r.Header.Clone()

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &received))

		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	wh := NewWebhookNotifier(WebhookConfig{
		URL: server.URL,
		Headers: map[string]string{
			"Authorization": "Bearer secret",
			"X-Custom":      "value",
		},
	})
	ts := time.Date(2026, 5, 18, 3, 0, 15, 0, time.UTC)

	err := wh.Send(context.Background(), NotifyMessage{
		Title:     "Backup Failed",
		Body:      "repository locked",
		Level:     LevelError,
		AgentName: "Tokyo-1",
		Timestamp: ts,
	})

	require.NoError(t, err)
	assert.Equal(t, "application/json", headers.Get("Content-Type"))
	assert.Equal(t, "Bearer secret", headers.Get("Authorization"))
	assert.Equal(t, "value", headers.Get("X-Custom"))
	assert.Equal(t, "Backup Failed", received["title"])
	assert.Equal(t, "repository locked", received["body"])
	assert.Equal(t, "error", received["level"])
	assert.Equal(t, "Tokyo-1", received["agent_name"])
	assert.Equal(t, ts.Format(time.RFC3339Nano), received["timestamp"])
}

func TestWebhookNotifierSendReturnsNon2xxErrorWithResponseText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("cannot brew notification"))
	}))
	defer server.Close()

	wh := NewWebhookNotifier(WebhookConfig{URL: server.URL})

	err := wh.Send(context.Background(), NotifyMessage{Title: "Test", Timestamp: time.Now()})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "webhook returned status 418")
	assert.Contains(t, err.Error(), "cannot brew notification")
}

func TestWebhookNotifierSendReturnsContextError(t *testing.T) {
	wh := NewWebhookNotifier(WebhookConfig{URL: "https://hooks.example.test/secret-token?sig=abc123"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := wh.Send(ctx, NotifyMessage{Title: "Test", Timestamp: time.Now()})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, err.Error(), "send webhook message")
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "sig=abc123")
	assert.NotContains(t, err.Error(), "hooks.example.test")
}

func TestWebhookNotifierSendErrorDoesNotLeakSecretURL(t *testing.T) {
	wh := NewWebhookNotifier(WebhookConfig{URL: "http://127.0.0.1:1/secret-path?token=abc123"})

	err := wh.Send(context.Background(), NotifyMessage{Title: "Test", Timestamp: time.Now()})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "send webhook message")
	assert.NotContains(t, err.Error(), "secret-path")
	assert.NotContains(t, err.Error(), "token=abc123")
	assert.NotContains(t, err.Error(), "127.0.0.1:1")
}

func TestWebhookNotifierType(t *testing.T) {
	wh := NewWebhookNotifier(WebhookConfig{URL: "https://example.com"})

	assert.Equal(t, "webhook", wh.Type())
}

func TestWebhookNotifierHasBoundedHTTPClientTimeout(t *testing.T) {
	wh := NewWebhookNotifier(WebhookConfig{URL: "https://example.com"})

	require.NotNil(t, wh.client)
	assert.Equal(t, defaultHTTPTimeout, wh.client.Timeout)
}
