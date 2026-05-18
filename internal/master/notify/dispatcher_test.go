package notify

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vaultfleet/internal/master/db"
	"vaultfleet/internal/master/events"
	"vaultfleet/pkg/protocol"
)

func TestNewNotifierFromConfigBuildsTelegramAndWebhook(t *testing.T) {
	tgConfig := json.RawMessage(`{"bot_token":"token","chat_id":"chat","base_url":"https://example.test"}`)
	tg, err := NewNotifierFromConfig("telegram", tgConfig)
	require.NoError(t, err)
	assert.Equal(t, "telegram", tg.Type())

	whConfig := json.RawMessage(`{"url":"https://hooks.example.test","headers":{"Authorization":"Bearer secret"}}`)
	wh, err := NewNotifierFromConfig("webhook", whConfig)
	require.NoError(t, err)
	assert.Equal(t, "webhook", wh.Type())
}

func TestNewNotifierFromConfigRejectsUnknownOrInvalidConfig(t *testing.T) {
	_, err := NewNotifierFromConfig("email", json.RawMessage(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown notification type")

	_, err = NewNotifierFromConfig("telegram", json.RawMessage(`{"bot_token":"token"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chat_id")

	_, err = NewNotifierFromConfig("webhook", json.RawMessage(`{"headers":{"X-Test":"value"}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "url")
}

func TestNewNotifierFromConfigRejectsUnknownFieldsInvalidHeadersAndWebhookURL(t *testing.T) {
	tests := []struct {
		name             string
		notificationType string
		config           json.RawMessage
		want             string
	}{
		{
			name:             "telegram unknown field",
			notificationType: "telegram",
			config:           json.RawMessage(`{"bot_token":"token","chat_id":"chat","extra":true}`),
			want:             "unknown field",
		},
		{
			name:             "webhook unknown field",
			notificationType: "webhook",
			config:           json.RawMessage(`{"url":"https://hooks.example.test","extra":true}`),
			want:             "unknown field",
		},
		{
			name:             "webhook non-string header",
			notificationType: "webhook",
			config:           json.RawMessage(`{"url":"https://hooks.example.test","headers":{"X-Count":3}}`),
			want:             "header",
		},
		{
			name:             "webhook nested header",
			notificationType: "webhook",
			config:           json.RawMessage(`{"url":"https://hooks.example.test","headers":{"X-Nested":{"value":"bad"}}}`),
			want:             "header",
		},
		{
			name:             "webhook invalid scheme",
			notificationType: "webhook",
			config:           json.RawMessage(`{"url":"ftp://hooks.example.test"}`),
			want:             "http",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewNotifierFromConfig(tt.notificationType, tt.config)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestDispatcherSendsAgentOfflineNotificationsForMatchingConfigs(t *testing.T) {
	database := setupNotifyTestDB(t)
	bus := events.NewBus()
	notifier := &recordingNotifier{}
	dispatcher := NewDispatcher(database, bus, WithNotifierFactory(func(notificationType string, raw json.RawMessage) (Notifier, error) {
		assert.Equal(t, "webhook", notificationType)
		assert.JSONEq(t, `{"url":"https://example.test"}`, string(raw))
		return notifier, nil
	}))
	createNotifyConfig(t, database, "webhook", `{"url":"https://example.test"}`, []string{"agent_offline"})

	dispatcher.Start()
	bus.Publish(events.Event{Type: events.AgentOffline, Payload: "agent-1"})

	require.Len(t, notifier.sent, 1)
	msg := notifier.sent[0]
	assert.Equal(t, "Agent Offline", msg.Title)
	assert.Equal(t, LevelWarning, msg.Level)
	assert.Equal(t, "agent-1", msg.AgentName)
	assert.Contains(t, msg.Body, "agent-1")
	assert.False(t, msg.Timestamp.IsZero())
}

func TestDispatcherDerivesBackupFailedFromFailedBackupTaskResult(t *testing.T) {
	database := setupNotifyTestDB(t)
	bus := events.NewBus()
	notifier := &recordingNotifier{}
	dispatcher := NewDispatcher(database, bus, WithNotifierFactory(func(string, json.RawMessage) (Notifier, error) {
		return notifier, nil
	}))
	createNotifyConfig(t, database, "webhook", `{"url":"https://example.test"}`, []string{"backup_failed"})
	payload, err := json.Marshal(protocol.TaskResultPayload{
		AgentID:  "agent-from-result",
		TaskType: "backup",
		Status:   "failed",
		ErrorLog: "repository locked",
	})
	require.NoError(t, err)

	dispatcher.Start()
	bus.Publish(events.Event{
		Type: events.TaskResult,
		Payload: map[string]any{
			"agent_id": "agent-from-event",
			"payload":  json.RawMessage(payload),
		},
	})

	require.Len(t, notifier.sent, 1)
	msg := notifier.sent[0]
	assert.Equal(t, "Backup Failed", msg.Title)
	assert.Equal(t, LevelError, msg.Level)
	assert.Equal(t, "agent-from-result", msg.AgentName)
	assert.Contains(t, msg.Body, "repository locked")
}

func TestDispatcherSendsDirectBackupFailedEventOnlyToMatchingConfigs(t *testing.T) {
	database := setupNotifyTestDB(t)
	bus := events.NewBus()
	notifier := &recordingNotifier{}
	dispatcher := NewDispatcher(database, bus, WithNotifierFactory(func(string, json.RawMessage) (Notifier, error) {
		return notifier, nil
	}))
	createNotifyConfig(t, database, "webhook", `{"url":"https://backup.example.test"}`, []string{"backup_failed"})
	createNotifyConfig(t, database, "webhook", `{"url":"https://offline.example.test"}`, []string{"agent_offline"})

	dispatcher.Start()
	bus.Publish(events.Event{
		Type: events.EventType(EventBackupFailed),
		Payload: map[string]any{
			"agent_name": "Tokyo-1",
			"error_log":  "repository locked",
		},
	})

	require.Len(t, notifier.sent, 1)
	msg := notifier.sent[0]
	assert.Equal(t, "Backup Failed", msg.Title)
	assert.Equal(t, LevelError, msg.Level)
	assert.Equal(t, "Tokyo-1", msg.AgentName)
	assert.Equal(t, "repository locked", msg.Body)
	assert.False(t, msg.Timestamp.IsZero())
}

func TestDispatcherIgnoresSuccessfulOrNonBackupTaskResults(t *testing.T) {
	database := setupNotifyTestDB(t)
	bus := events.NewBus()
	notifier := &recordingNotifier{}
	dispatcher := NewDispatcher(database, bus, WithNotifierFactory(func(string, json.RawMessage) (Notifier, error) {
		return notifier, nil
	}))
	createNotifyConfig(t, database, "webhook", `{"url":"https://example.test"}`, []string{"backup_failed"})

	dispatcher.Start()
	publishTaskResult(t, bus, protocol.TaskResultPayload{AgentID: "agent-1", TaskType: "backup", Status: "success"})
	publishTaskResult(t, bus, protocol.TaskResultPayload{AgentID: "agent-1", TaskType: "restore", Status: "failed", ErrorLog: "restore failed"})

	assert.Empty(t, notifier.sent)
}

func TestDispatcherSkipsNonMatchingConfigsAndContinuesAfterSendError(t *testing.T) {
	database := setupNotifyTestDB(t)
	bus := events.NewBus()
	failing := &recordingNotifier{err: errors.New("send failed")}
	successful := &recordingNotifier{}
	calls := 0
	dispatcher := NewDispatcher(database, bus, WithNotifierFactory(func(string, json.RawMessage) (Notifier, error) {
		calls++
		if calls == 1 {
			return failing, nil
		}
		return successful, nil
	}))
	createNotifyConfig(t, database, "webhook", `{"url":"https://first.example.test"}`, []string{"agent_offline"})
	createNotifyConfig(t, database, "webhook", `{"url":"https://ignored.example.test"}`, []string{"backup_failed"})
	createNotifyConfig(t, database, "webhook", `{"url":"https://second.example.test"}`, []string{"agent_offline"})

	dispatcher.Start()
	bus.Publish(events.Event{Type: events.AgentOffline, Payload: "agent-1"})

	assert.Len(t, failing.sent, 1)
	assert.Len(t, successful.sent, 1)
	assert.Equal(t, 2, calls)
}

func TestDispatcherDecryptsStoredNotificationConfigAndUsesTimeoutContext(t *testing.T) {
	database := setupNotifyTestDB(t)
	bus := events.NewBus()
	notifier := &contextRecordingNotifier{}
	dispatcher := NewDispatcher(database, bus, WithNotifierFactory(func(notificationType string, raw json.RawMessage) (Notifier, error) {
		assert.Equal(t, "webhook", notificationType)
		assert.JSONEq(t, `{"url":"https://hooks.example.test","headers":{"Authorization":"Bearer secret"}}`, string(raw))
		return notifier, nil
	}))

	encryptedConfig, err := db.Encrypt(`{"url":"https://hooks.example.test","headers":{"Authorization":"Bearer secret"}}`, database.MasterKey)
	require.NoError(t, err)
	createNotifyConfig(t, database, "webhook", encryptedConfig, []string{"agent_offline"})

	dispatcher.Start()
	bus.Publish(events.Event{Type: events.AgentOffline, Payload: "agent-1"})

	require.Len(t, notifier.sent, 1)
	assert.True(t, notifier.hadDeadline, "dispatcher should bound external notification sends")
	assert.LessOrEqual(t, time.Until(notifier.deadline), defaultSendTimeout)
}

func publishTaskResult(t *testing.T, bus *events.Bus, result protocol.TaskResultPayload) {
	t.Helper()

	payload, err := json.Marshal(result)
	require.NoError(t, err)
	bus.Publish(events.Event{
		Type: events.TaskResult,
		Payload: map[string]any{
			"agent_id": result.AgentID,
			"payload":  json.RawMessage(payload),
		},
	})
}

func setupNotifyTestDB(t *testing.T) *db.Database {
	t.Helper()

	database, err := db.New(t.TempDir())
	require.NoError(t, err)
	return database
}

func createNotifyConfig(t *testing.T, database *db.Database, notificationType, config string, eventNames []string) db.NotificationConfig {
	t.Helper()

	eventsJSON, err := json.Marshal(eventNames)
	require.NoError(t, err)

	notificationConfig := db.NotificationConfig{
		Type:   notificationType,
		Config: config,
		Events: string(eventsJSON),
	}
	require.NoError(t, database.DB.Create(&notificationConfig).Error)
	return notificationConfig
}

type recordingNotifier struct {
	sent []NotifyMessage
	err  error
}

func (n *recordingNotifier) Send(_ context.Context, msg NotifyMessage) error {
	n.sent = append(n.sent, msg)
	return n.err
}

func (n *recordingNotifier) Type() string {
	return "recording"
}

type contextRecordingNotifier struct {
	sent        []NotifyMessage
	hadDeadline bool
	deadline    time.Time
}

func (n *contextRecordingNotifier) Send(ctx context.Context, msg NotifyMessage) error {
	n.sent = append(n.sent, msg)
	n.deadline, n.hadDeadline = ctx.Deadline()
	return nil
}

func (n *contextRecordingNotifier) Type() string {
	return "recording"
}
