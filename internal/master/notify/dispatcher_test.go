package notify

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
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

	msg := requireRecordedMessage(t, notifier)
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

	msg := requireRecordedMessage(t, notifier)
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

	msg := requireRecordedMessage(t, notifier)
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

	assert.Empty(t, notifier.snapshot())
}

func TestDispatcherSkipsNonMatchingConfigsAndContinuesAfterSendError(t *testing.T) {
	database := setupNotifyTestDB(t)
	bus := events.NewBus()
	failing := &recordingNotifier{err: errors.New("send failed")}
	successful := &recordingNotifier{}
	var calls atomic.Int64
	dispatcher := NewDispatcher(database, bus, WithNotifierFactory(func(string, json.RawMessage) (Notifier, error) {
		if calls.Add(1) == 1 {
			return failing, nil
		}
		return successful, nil
	}))
	createNotifyConfig(t, database, "webhook", `{"url":"https://first.example.test"}`, []string{"agent_offline"})
	createNotifyConfig(t, database, "webhook", `{"url":"https://ignored.example.test"}`, []string{"backup_failed"})
	createNotifyConfig(t, database, "webhook", `{"url":"https://second.example.test"}`, []string{"agent_offline"})

	dispatcher.Start()
	bus.Publish(events.Event{Type: events.AgentOffline, Payload: "agent-1"})

	require.Eventually(t, func() bool {
		return len(failing.snapshot()) == 1 && len(successful.snapshot()) == 1
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, int64(2), calls.Load())
}

func TestDispatcherDoesNotBlockEventPublishWhenNotifierBlocks(t *testing.T) {
	database := setupNotifyTestDB(t)
	bus := events.NewBus()
	blocking := &blockingNotifier{started: make(chan struct{}), release: make(chan struct{})}
	dispatcher := NewDispatcher(database, bus, WithNotifierFactory(func(string, json.RawMessage) (Notifier, error) {
		return blocking, nil
	}))
	createNotifyConfig(t, database, "webhook", `{"url":"https://blocking.example.test"}`, []string{"agent_offline"})

	dispatcher.Start()
	started := time.Now()
	bus.Publish(events.Event{Type: events.AgentOffline, Payload: "agent-1"})
	elapsed := time.Since(started)
	defer close(blocking.release)

	assert.Less(t, elapsed, 50*time.Millisecond)
	select {
	case <-blocking.started:
	case <-time.After(time.Second):
		t.Fatal("notification send did not start")
	}
}

func TestDispatcherSlowNotifierDoesNotPreventLaterMatchingConfig(t *testing.T) {
	database := setupNotifyTestDB(t)
	bus := events.NewBus()
	blocking := &blockingNotifier{started: make(chan struct{}), release: make(chan struct{})}
	successful := &recordingNotifier{}
	var calls atomic.Int64
	dispatcher := NewDispatcher(database, bus, WithNotifierFactory(func(string, json.RawMessage) (Notifier, error) {
		if calls.Add(1) == 1 {
			return blocking, nil
		}
		return successful, nil
	}))
	createNotifyConfig(t, database, "webhook", `{"url":"https://blocking.example.test"}`, []string{"agent_offline"})
	createNotifyConfig(t, database, "webhook", `{"url":"https://successful.example.test"}`, []string{"agent_offline"})

	dispatcher.Start()
	bus.Publish(events.Event{Type: events.AgentOffline, Payload: "agent-1"})
	defer close(blocking.release)

	select {
	case <-blocking.started:
	case <-time.After(time.Second):
		t.Fatal("blocking notification send did not start")
	}
	requireRecordedMessage(t, successful)
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

	require.Eventually(t, func() bool {
		return len(notifier.snapshot()) == 1
	}, time.Second, 10*time.Millisecond)
	hadDeadline, deadline := notifier.deadlineSnapshot()
	assert.True(t, hadDeadline, "dispatcher should bound external notification sends")
	assert.LessOrEqual(t, time.Until(deadline), defaultSendTimeout)
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
	mu   sync.Mutex
	sent []NotifyMessage
	err  error
}

func (n *recordingNotifier) Send(_ context.Context, msg NotifyMessage) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, msg)
	return n.err
}

func (n *recordingNotifier) Type() string {
	return "recording"
}

func (n *recordingNotifier) snapshot() []NotifyMessage {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]NotifyMessage(nil), n.sent...)
}

func requireRecordedMessage(t *testing.T, notifier *recordingNotifier) NotifyMessage {
	t.Helper()

	var messages []NotifyMessage
	require.Eventually(t, func() bool {
		messages = notifier.snapshot()
		return len(messages) == 1
	}, time.Second, 10*time.Millisecond)
	return messages[0]
}

type contextRecordingNotifier struct {
	mu          sync.Mutex
	sent        []NotifyMessage
	hadDeadline bool
	deadline    time.Time
}

func (n *contextRecordingNotifier) Send(ctx context.Context, msg NotifyMessage) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, msg)
	n.deadline, n.hadDeadline = ctx.Deadline()
	return nil
}

func (n *contextRecordingNotifier) Type() string {
	return "recording"
}

func (n *contextRecordingNotifier) snapshot() []NotifyMessage {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]NotifyMessage(nil), n.sent...)
}

func (n *contextRecordingNotifier) deadlineSnapshot() (bool, time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.hadDeadline, n.deadline
}

type blockingNotifier struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (n *blockingNotifier) Send(ctx context.Context, msg NotifyMessage) error {
	n.once.Do(func() {
		close(n.started)
	})
	select {
	case <-n.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (n *blockingNotifier) Type() string {
	return "blocking"
}
