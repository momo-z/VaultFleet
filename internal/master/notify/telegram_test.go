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

func TestTelegramNotifierSendPostsMessageToBotAPI(t *testing.T) {
	var receivedBody map[string]any
	var receivedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.Header.Get("Content-Type"), "application/json")

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &receivedBody))

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	tg := NewTelegramNotifier(TelegramConfig{
		BotToken: "123456:ABC-DEF",
		ChatID:   "-100999888",
		BaseURL:  server.URL,
	})
	msg := NotifyMessage{
		Title:     "Backup Failed",
		Body:      "restic exit code 1 - repository locked",
		Level:     LevelError,
		AgentName: "Tokyo-1",
		Timestamp: time.Date(2026, 5, 18, 3, 0, 15, 0, time.UTC),
	}

	err := tg.Send(context.Background(), msg)

	require.NoError(t, err)
	assert.Equal(t, "/bot123456:ABC-DEF/sendMessage", receivedPath)
	assert.Equal(t, "-100999888", receivedBody["chat_id"])
	assert.Equal(t, "HTML", receivedBody["parse_mode"])
	text, ok := receivedBody["text"].(string)
	require.True(t, ok)
	assert.Contains(t, text, "Backup Failed")
	assert.Contains(t, text, "Tokyo-1")
	assert.Contains(t, text, "2026-05-18")
	assert.Contains(t, text, "repository locked")
}

func TestTelegramNotifierEscapesHTMLDynamicFields(t *testing.T) {
	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &receivedBody))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tg := NewTelegramNotifier(TelegramConfig{
		BotToken: "token",
		ChatID:   "chat",
		BaseURL:  server.URL,
	})

	err := tg.Send(context.Background(), NotifyMessage{
		Title:     `<bad & title>`,
		Body:      `body with <tag> & "quotes"`,
		Level:     LevelWarning,
		AgentName: `A&B`,
		Timestamp: time.Date(2026, 5, 18, 3, 0, 0, 0, time.UTC),
	})

	require.NoError(t, err)
	text := receivedBody["text"].(string)
	assert.Contains(t, text, "&lt;bad &amp; title&gt;")
	assert.Contains(t, text, "A&amp;B")
	assert.Contains(t, text, "body with &lt;tag&gt; &amp; &#34;quotes&#34;")
	assert.NotContains(t, text, "<bad & title>")
}

func TestTelegramNotifierSendReturnsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
	}))
	defer server.Close()

	tg := NewTelegramNotifier(TelegramConfig{
		BotToken: "123456:ABC-DEF",
		ChatID:   "-100invalid",
		BaseURL:  server.URL,
	})

	err := tg.Send(context.Background(), NotifyMessage{Title: "Test", Timestamp: time.Now()})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "telegram API error")
	assert.Contains(t, err.Error(), "chat not found")
}

func TestTelegramNotifierSendReturnsNetworkOrContextError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tg := NewTelegramNotifier(TelegramConfig{
		BotToken: "token",
		ChatID:   "chat",
		BaseURL:  server.URL,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := tg.Send(ctx, NotifyMessage{Title: "Test", Timestamp: time.Now()})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestTelegramNotifierSendErrorDoesNotLeakBotTokenOrURL(t *testing.T) {
	tg := NewTelegramNotifier(TelegramConfig{
		BotToken: "secret-telegram-token",
		ChatID:   "chat",
		BaseURL:  "http://127.0.0.1:1",
	})

	err := tg.Send(context.Background(), NotifyMessage{Title: "Test", Timestamp: time.Now()})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "send telegram message")
	assert.NotContains(t, err.Error(), "secret-telegram-token")
	assert.NotContains(t, err.Error(), "/botsecret-telegram-token/sendMessage")
}

func TestTelegramNotifierTypeAndDefaultBaseURL(t *testing.T) {
	tg := NewTelegramNotifier(TelegramConfig{
		BotToken: "token",
		ChatID:   "chat",
	})

	assert.Equal(t, "telegram", tg.Type())
	assert.Equal(t, "https://api.telegram.org", tg.baseURL)
}

func TestTelegramNotifierHasBoundedHTTPClientTimeout(t *testing.T) {
	tg := NewTelegramNotifier(TelegramConfig{
		BotToken: "token",
		ChatID:   "chat",
	})

	require.NotNil(t, tg.client)
	assert.Equal(t, defaultHTTPTimeout, tg.client.Timeout)
}
