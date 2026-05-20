package commands

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vaultfleet/internal/master/db"
	"vaultfleet/pkg/protocol"
)

func TestCreateCommandEncryptsPayloadAndSetsDeadline(t *testing.T) {
	database := setupCommandTestDB(t)
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	service := NewService(database, nil)
	service.Now = func() time.Time { return now }

	msg, err := protocol.NewMessage(protocol.TypeBackupNow, protocol.BackupNowPayload{AgentID: "agent-1"})
	require.NoError(t, err)

	command, err := service.CreateCommand(context.Background(), CreateCommandInput{
		AgentID:   "agent-1",
		Type:      protocol.TypeBackupNow,
		Message:   *msg,
		TaskType:  "backup",
		TaskState: TaskStatusPending,
	})
	require.NoError(t, err)

	assert.Equal(t, CommandStatusPending, command.Status)
	assert.Equal(t, msg.ID, command.MessageID)
	assert.NotNil(t, command.DeadlineAt)
	assert.Equal(t, now.Add(6*time.Hour), command.DeadlineAt.UTC())
	assert.NotContains(t, command.Payload, "agent-1")

	var history db.TaskHistory
	require.NoError(t, database.DB.First(&history, "command_id = ?", command.ID).Error)
	assert.Equal(t, "backup", history.Type)
	assert.Equal(t, TaskStatusPending, history.Status)
	assert.Equal(t, msg.ID, history.MessageID)
}

func TestDispatchPendingForAgentSendsOldestPendingCommand(t *testing.T) {
	database := setupCommandTestDB(t)
	hub := &recordingHub{online: map[string]bool{"agent-1": true}}
	service := NewService(database, hub)
	service.Now = func() time.Time { return time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC) }

	first := createCommandForTest(t, service, "agent-1", protocol.TypeBackupNow)
	second := createCommandForTest(t, service, "agent-1", protocol.TypeRestoreReq)

	require.NoError(t, service.DispatchPendingForAgent(context.Background(), "agent-1", 10))

	require.Len(t, hub.sent, 2)
	assert.Equal(t, first.MessageID, hub.sent[0].ID)
	assert.Equal(t, second.MessageID, hub.sent[1].ID)

	var updated db.AgentCommand
	require.NoError(t, database.DB.First(&updated, "id = ?", first.ID).Error)
	assert.Equal(t, CommandStatusRunning, updated.Status)
	assert.Equal(t, 1, updated.Attempts)
	assert.NotNil(t, updated.DispatchedAt)
}

func TestDispatchPendingForAgentRedispatchesUnackedShortCommand(t *testing.T) {
	database := setupCommandTestDB(t)
	hub := &recordingHub{online: map[string]bool{"agent-1": true}}
	service := NewService(database, hub)

	command := createPolicyPushCommandForTest(t, service, "agent-1")

	require.NoError(t, service.DispatchPendingForAgent(context.Background(), "agent-1", 10))
	require.Len(t, hub.sent, 1)

	var dispatched db.AgentCommand
	require.NoError(t, database.DB.First(&dispatched, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusDispatched, dispatched.Status)
	assert.Equal(t, 1, dispatched.Attempts)

	require.NoError(t, service.DispatchPendingForAgent(context.Background(), "agent-1", 10))

	require.Len(t, hub.sent, 2)
	assert.Equal(t, command.MessageID, hub.sent[1].ID)
	var afterSecondDispatch db.AgentCommand
	require.NoError(t, database.DB.First(&afterSecondDispatch, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusDispatched, afterSecondDispatch.Status)
	assert.Equal(t, 2, afterSecondDispatch.Attempts)
}

func TestDispatchPendingForAgentConcurrentDispatchSendsLongCommandAtMostOnce(t *testing.T) {
	database := setupCommandTestDB(t)
	hub := newBlockingHub("agent-1")
	service := NewService(database, hub)
	command := createCommandForTest(t, service, "agent-1", protocol.TypeBackupNow)

	assertConcurrentDispatchSendsLongCommandAtMostOnce(t, database, hub, command, service, service)
}

func TestDispatchPendingForAgentConcurrentServicesSendLongCommandAtMostOnce(t *testing.T) {
	database := setupCommandTestDB(t)
	hub := newBlockingHub("agent-1")
	firstService := NewService(database, hub)
	secondService := NewService(database, hub)
	command := createCommandForTest(t, firstService, "agent-1", protocol.TypeBackupNow)

	assertConcurrentDispatchSendsLongCommandAtMostOnce(t, database, hub, command, firstService, secondService)
}

func assertConcurrentDispatchSendsLongCommandAtMostOnce(
	t *testing.T,
	database *db.Database,
	hub *blockingHub,
	command db.AgentCommand,
	firstService *Service,
	secondService *Service,
) {
	t.Helper()

	firstErr := make(chan error, 1)
	go func() {
		firstErr <- firstService.DispatchPendingForAgent(context.Background(), "agent-1", 10)
	}()

	hub.waitForSend(t)

	var duringSend db.AgentCommand
	if assert.NoError(t, database.DB.First(&duringSend, "id = ?", command.ID).Error) {
		assert.Equal(t, CommandStatusPending, duringSend.Status)
		assert.Equal(t, 0, duringSend.Attempts)
		assert.Nil(t, duringSend.DispatchedAt)
	}
	var historyDuringSend db.TaskHistory
	if assert.NoError(t, database.DB.First(&historyDuringSend, "command_id = ?", command.ID).Error) {
		assert.Equal(t, TaskStatusPending, historyDuringSend.Status)
	}

	secondErr := make(chan error, 1)
	go func() {
		secondErr <- secondService.DispatchPendingForAgent(context.Background(), "agent-1", 10)
	}()

	secondReturnedEarly := false
	select {
	case <-hub.entered:
		t.Error("second dispatch attempted to send while first dispatch was still in progress")
	case err := <-secondErr:
		require.NoError(t, err)
		secondReturnedEarly = true
	case <-time.After(50 * time.Millisecond):
	}

	hub.releaseSends()
	require.NoError(t, <-firstErr)
	if !secondReturnedEarly {
		select {
		case err := <-secondErr:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for second dispatch")
		}
	}

	assert.Equal(t, 1, hub.sendCount())

	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusRunning, found.Status)
	assert.Equal(t, 1, found.Attempts)
}

func TestDispatchPendingForOfflineAgentLeavesCommandPending(t *testing.T) {
	database := setupCommandTestDB(t)
	hub := &recordingHub{online: map[string]bool{"agent-1": false}}
	service := NewService(database, hub)
	command := createCommandForTest(t, service, "agent-1", protocol.TypeBackupNow)

	require.NoError(t, service.DispatchPendingForAgent(context.Background(), "agent-1", 10))

	assert.Empty(t, hub.sent)
	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusPending, found.Status)
	assert.Equal(t, 0, found.Attempts)
}

func TestDispatchPendingRecordsSendFailure(t *testing.T) {
	database := setupCommandTestDB(t)
	hub := &recordingHub{online: map[string]bool{"agent-1": true}, err: errors.New("write failed")}
	service := NewService(database, hub)
	command := createCommandForTest(t, service, "agent-1", protocol.TypeBackupNow)

	require.NoError(t, service.DispatchPendingForAgent(context.Background(), "agent-1", 10))

	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusPending, found.Status)
	assert.Equal(t, 1, found.Attempts)
	assert.Nil(t, found.DispatchedAt)
	assert.Contains(t, found.ErrorMessage, "write failed")
}

func TestCompletePolicyAckMarksCommandSucceeded(t *testing.T) {
	database := setupCommandTestDB(t)
	service := NewService(database, nil)
	msg, err := protocol.NewMessage(protocol.TypePolicyPush, protocol.PolicyPushPayload{AgentID: "agent-1"})
	require.NoError(t, err)
	command, err := service.CreateCommand(context.Background(), CreateCommandInput{
		AgentID:  "agent-1",
		Type:     protocol.TypePolicyPush,
		Message:  *msg,
		PolicyID: "policy-1",
	})
	require.NoError(t, err)

	require.NoError(t, service.CompletePolicyAck(context.Background(), "agent-1", msg.ID, true, ""))

	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusSucceeded, found.Status)
	assert.NotNil(t, found.CompletedAt)
	assert.Empty(t, found.ErrorMessage)
}

func TestCompletePolicyAckMarksCommandFailed(t *testing.T) {
	database := setupCommandTestDB(t)
	service := NewService(database, nil)
	msg, err := protocol.NewMessage(protocol.TypePolicyPush, protocol.PolicyPushPayload{AgentID: "agent-1"})
	require.NoError(t, err)
	command, err := service.CreateCommand(context.Background(), CreateCommandInput{
		AgentID:  "agent-1",
		Type:     protocol.TypePolicyPush,
		Message:  *msg,
		PolicyID: "policy-1",
	})
	require.NoError(t, err)

	require.NoError(t, service.CompletePolicyAck(context.Background(), "agent-1", msg.ID, false, "invalid schedule"))

	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusFailed, found.Status)
	assert.Equal(t, "invalid schedule", found.ErrorMessage)
	assert.NotNil(t, found.CompletedAt)
}

func TestCompletePolicyAckReturnsErrorForMissingCommand(t *testing.T) {
	database := setupCommandTestDB(t)
	service := NewService(database, nil)

	err := service.CompletePolicyAck(context.Background(), "agent-1", "missing-message", true, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "policy command not found")
}

func TestCompletePolicyAckDoesNotRewriteTerminalCommand(t *testing.T) {
	database := setupCommandTestDB(t)
	service := NewService(database, nil)
	msg, err := protocol.NewMessage(protocol.TypePolicyPush, protocol.PolicyPushPayload{AgentID: "agent-1"})
	require.NoError(t, err)
	command, err := service.CreateCommand(context.Background(), CreateCommandInput{
		AgentID:  "agent-1",
		Type:     protocol.TypePolicyPush,
		Message:  *msg,
		PolicyID: "policy-1",
	})
	require.NoError(t, err)
	require.NoError(t, service.CompletePolicyAck(context.Background(), "agent-1", msg.ID, true, ""))

	require.NoError(t, service.CompletePolicyAck(context.Background(), "agent-1", msg.ID, false, "late failure"))

	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusSucceeded, found.Status)
	assert.Empty(t, found.ErrorMessage)
}

func TestCompleteTaskResultUpdatesCommandAndTaskHistory(t *testing.T) {
	database := setupCommandTestDB(t)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	service := NewService(database, nil)
	service.Now = func() time.Time { return now }
	startedAt := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(2 * time.Minute)
	msg, err := protocol.NewMessage(protocol.TypeBackupNow, protocol.BackupNowPayload{AgentID: "agent-1"})
	require.NoError(t, err)
	command, err := service.CreateCommand(context.Background(), CreateCommandInput{
		AgentID:   "agent-1",
		Type:      protocol.TypeBackupNow,
		Message:   *msg,
		TaskType:  "backup",
		TaskState: TaskStatusRunning,
	})
	require.NoError(t, err)
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).Where("id = ?", command.ID).Update("status", CommandStatusRunning).Error)

	require.NoError(t, service.CompleteTaskResult(context.Background(), "agent-1", msg.ID, protocol.TaskResultPayload{
		AgentID:    "agent-1",
		TaskType:   "backup",
		Status:     "success",
		SnapshotID: "snap-1",
		DurationMs: 120000,
		RepoSize:   2048,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	}))

	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusSucceeded, found.Status)
	require.NotNil(t, found.CompletedAt)
	assert.True(t, found.CompletedAt.Equal(now))
	assert.Empty(t, found.ErrorMessage)
	assert.Contains(t, found.Result, `"snapshot_id":"snap-1"`)

	var history db.TaskHistory
	require.NoError(t, database.DB.First(&history, "command_id = ?", command.ID).Error)
	assert.Equal(t, TaskStatusSuccess, history.Status)
	assert.Equal(t, "snap-1", history.SnapshotID)
	assert.Equal(t, int64(120000), history.DurationMs)
	assert.Equal(t, int64(2048), history.RepoSize)
	require.NotNil(t, history.StartedAt)
	assert.True(t, history.StartedAt.Equal(startedAt))
	require.NotNil(t, history.FinishedAt)
	assert.True(t, history.FinishedAt.Equal(finishedAt))
}

func TestCompleteTaskResultUpdatesOnlyMatchingCommandHistory(t *testing.T) {
	database := setupCommandTestDB(t)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	service := NewService(database, nil)
	service.Now = func() time.Time { return now }
	msg, err := protocol.NewMessage(protocol.TypeBackupNow, protocol.BackupNowPayload{AgentID: "agent-1"})
	require.NoError(t, err)
	command, err := service.CreateCommand(context.Background(), CreateCommandInput{
		AgentID:   "agent-1",
		Type:      protocol.TypeBackupNow,
		Message:   *msg,
		TaskType:  "backup",
		TaskState: TaskStatusRunning,
	})
	require.NoError(t, err)
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).Where("id = ?", command.ID).Update("status", CommandStatusRunning).Error)
	otherHistory := db.TaskHistory{
		AgentID:   "agent-1",
		Type:      "backup",
		Status:    TaskStatusRunning,
		MessageID: msg.ID,
		CommandID: "other-command",
	}
	require.NoError(t, database.DB.Create(&otherHistory).Error)

	require.NoError(t, service.CompleteTaskResult(context.Background(), "agent-1", msg.ID, protocol.TaskResultPayload{
		AgentID:    "agent-1",
		TaskType:   "backup",
		Status:     TaskStatusSuccess,
		SnapshotID: "snap-1",
		DurationMs: 120000,
		RepoSize:   2048,
		FinishedAt: now,
	}))

	var commandHistory db.TaskHistory
	require.NoError(t, database.DB.First(&commandHistory, "command_id = ?", command.ID).Error)
	assert.Equal(t, TaskStatusSuccess, commandHistory.Status)
	assert.Equal(t, "snap-1", commandHistory.SnapshotID)

	var otherAfter db.TaskHistory
	require.NoError(t, database.DB.First(&otherAfter, "id = ?", otherHistory.ID).Error)
	assert.Equal(t, TaskStatusRunning, otherAfter.Status)
	assert.Empty(t, otherAfter.SnapshotID)
}

func TestCompleteTaskResultPreservesExistingStartedAtWhenResultOmitsIt(t *testing.T) {
	database := setupCommandTestDB(t)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	existingStartedAt := now.Add(-10 * time.Minute)
	service := NewService(database, nil)
	service.Now = func() time.Time { return now }
	msg, err := protocol.NewMessage(protocol.TypeBackupNow, protocol.BackupNowPayload{AgentID: "agent-1"})
	require.NoError(t, err)
	command, err := service.CreateCommand(context.Background(), CreateCommandInput{
		AgentID:   "agent-1",
		Type:      protocol.TypeBackupNow,
		Message:   *msg,
		TaskType:  "backup",
		TaskState: TaskStatusRunning,
	})
	require.NoError(t, err)
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).Where("id = ?", command.ID).Update("status", CommandStatusRunning).Error)
	require.NoError(t, database.DB.Model(&db.TaskHistory{}).
		Where("command_id = ?", command.ID).
		Update("started_at", &existingStartedAt).Error)

	require.NoError(t, service.CompleteTaskResult(context.Background(), "agent-1", msg.ID, protocol.TaskResultPayload{
		AgentID:    "agent-1",
		TaskType:   "backup",
		Status:     TaskStatusSuccess,
		SnapshotID: "snap-1",
		FinishedAt: now,
	}))

	var history db.TaskHistory
	require.NoError(t, database.DB.First(&history, "command_id = ?", command.ID).Error)
	require.NotNil(t, history.StartedAt)
	assert.True(t, history.StartedAt.Equal(existingStartedAt))
}

func TestCompleteTaskResultMarksCommandFailed(t *testing.T) {
	database := setupCommandTestDB(t)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	service := NewService(database, nil)
	service.Now = func() time.Time { return now }
	startedAt := now.Add(-time.Minute)
	msg, err := protocol.NewMessage(protocol.TypeBackupNow, protocol.BackupNowPayload{AgentID: "agent-1"})
	require.NoError(t, err)
	command, err := service.CreateCommand(context.Background(), CreateCommandInput{
		AgentID:   "agent-1",
		Type:      protocol.TypeBackupNow,
		Message:   *msg,
		TaskType:  "backup",
		TaskState: TaskStatusRunning,
	})
	require.NoError(t, err)
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).Where("id = ?", command.ID).Update("status", CommandStatusRunning).Error)

	require.NoError(t, service.CompleteTaskResult(context.Background(), "agent-1", msg.ID, protocol.TaskResultPayload{
		AgentID:   "agent-1",
		TaskType:  "backup",
		Status:    "failed",
		ErrorLog:  "restic failed",
		StartedAt: startedAt,
	}))

	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusFailed, found.Status)
	assert.Equal(t, "restic failed", found.ErrorMessage)
	require.NotNil(t, found.CompletedAt)
	assert.True(t, found.CompletedAt.Equal(now))

	var history db.TaskHistory
	require.NoError(t, database.DB.First(&history, "command_id = ?", command.ID).Error)
	assert.Equal(t, TaskStatusFailed, history.Status)
	assert.Equal(t, "restic failed", history.ErrorLog)
	require.NotNil(t, history.FinishedAt)
	assert.True(t, history.FinishedAt.Equal(now))
}

func TestTimeoutExpiredDoesNotRewriteTerminalTaskHistory(t *testing.T) {
	for _, terminalStatus := range []string{TaskStatusSuccess, TaskStatusFailed} {
		t.Run(terminalStatus, func(t *testing.T) {
			database := setupCommandTestDB(t)
			now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
			service := NewService(database, nil)
			service.Now = func() time.Time { return now }
			command := createCommandForTest(t, service, "agent-1", protocol.TypeBackupNow)
			pastDeadline := now.Add(-time.Second)
			finishedAt := now.Add(-time.Minute)
			require.NoError(t, database.DB.Model(&db.AgentCommand{}).
				Where("id = ?", command.ID).
				Updates(map[string]any{
					"status":      CommandStatusRunning,
					"deadline_at": &pastDeadline,
				}).Error)
			require.NoError(t, database.DB.Model(&db.TaskHistory{}).
				Where("command_id = ?", command.ID).
				Updates(map[string]any{
					"status":      terminalStatus,
					"finished_at": &finishedAt,
				}).Error)

			count, err := service.TimeoutExpired(context.Background())

			require.NoError(t, err)
			assert.Equal(t, int64(1), count)
			var found db.AgentCommand
			require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
			assert.Equal(t, CommandStatusTimeout, found.Status)

			var history db.TaskHistory
			require.NoError(t, database.DB.First(&history, "command_id = ?", command.ID).Error)
			assert.Equal(t, terminalStatus, history.Status)
			require.NotNil(t, history.FinishedAt)
			assert.True(t, history.FinishedAt.Equal(finishedAt))
		})
	}
}

func TestTimeoutExpiredCommandsMarksCommandAndTask(t *testing.T) {
	database := setupCommandTestDB(t)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	service := NewService(database, nil)
	service.Now = func() time.Time { return now }
	command := createCommandForTest(t, service, "agent-1", protocol.TypeBackupNow)
	pastDeadline := now.Add(-time.Second)
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).
		Where("id = ?", command.ID).
		Updates(map[string]any{
			"status":      CommandStatusRunning,
			"deadline_at": &pastDeadline,
		}).Error)
	require.NoError(t, database.DB.Model(&db.TaskHistory{}).
		Where("command_id = ?", command.ID).
		Update("status", TaskStatusRunning).Error)

	count, err := service.TimeoutExpired(context.Background())

	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusTimeout, found.Status)
	assert.Equal(t, "command timeout", found.ErrorMessage)
	require.NotNil(t, found.CompletedAt)
	assert.True(t, found.CompletedAt.Equal(now))

	var history db.TaskHistory
	require.NoError(t, database.DB.First(&history, "command_id = ?", command.ID).Error)
	assert.Equal(t, TaskStatusTimeout, history.Status)
	assert.Equal(t, "command timeout", history.ErrorLog)
	require.NotNil(t, history.FinishedAt)
	assert.True(t, history.FinishedAt.Equal(now))
}

func TestDispatchDoesNotOverwritePolicyAckTerminalState(t *testing.T) {
	database := setupCommandTestDB(t)
	hub := newAckingHub("agent-1")
	service := NewService(database, hub)
	hub.onSend = func(message protocol.Message) {
		require.NoError(t, service.CompletePolicyAck(context.Background(), "agent-1", message.ID, true, ""))
	}
	command := createPolicyPushCommandForTest(t, service, "agent-1")

	require.NoError(t, service.DispatchPendingForAgent(context.Background(), "agent-1", 10))

	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusSucceeded, found.Status)
	assert.NotNil(t, found.CompletedAt)
}

func TestDispatchFailureDoesNotOverwritePolicyAckTerminalState(t *testing.T) {
	database := setupCommandTestDB(t)
	hub := newAckingHub("agent-1")
	service := NewService(database, hub)
	hub.onSend = func(message protocol.Message) {
		require.NoError(t, service.CompletePolicyAck(context.Background(), "agent-1", message.ID, true, ""))
	}
	hub.err = errors.New("late write failure")
	command := createPolicyPushCommandForTest(t, service, "agent-1")

	require.NoError(t, service.DispatchPendingForAgent(context.Background(), "agent-1", 10))

	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusSucceeded, found.Status)
	assert.NotNil(t, found.CompletedAt)
	assert.Empty(t, found.ErrorMessage)
}

func setupCommandTestDB(t *testing.T) *db.Database {
	t.Helper()
	database, err := db.New(t.TempDir())
	require.NoError(t, err)
	return database
}

type ackingHub struct {
	online map[string]bool
	onSend func(protocol.Message)
	err    error
}

func newAckingHub(agentID string) *ackingHub {
	return &ackingHub{online: map[string]bool{agentID: true}}
}

func (h *ackingHub) IsOnline(agentID string) bool {
	return h.online[agentID]
}

func (h *ackingHub) Send(agentID string, msg interface{}) error {
	message, ok := msg.(protocol.Message)
	if !ok {
		return errors.New("message is not protocol.Message")
	}
	if h.onSend != nil {
		h.onSend(message)
	}
	return h.err
}

type recordingHub struct {
	online map[string]bool
	err    error
	sent   []protocol.Message
}

func (h *recordingHub) IsOnline(agentID string) bool {
	return h.online[agentID]
}

func (h *recordingHub) Send(agentID string, msg interface{}) error {
	if h.err != nil {
		return h.err
	}
	message, ok := msg.(protocol.Message)
	if !ok {
		return errors.New("message is not protocol.Message")
	}
	h.sent = append(h.sent, message)
	return nil
}

type blockingHub struct {
	online  map[string]bool
	entered chan struct{}
	release chan struct{}
	sent    chan protocol.Message
}

func newBlockingHub(agentID string) *blockingHub {
	return &blockingHub{
		online:  map[string]bool{agentID: true},
		entered: make(chan struct{}, 2),
		release: make(chan struct{}),
		sent:    make(chan protocol.Message, 2),
	}
}

func (h *blockingHub) IsOnline(agentID string) bool {
	return h.online[agentID]
}

func (h *blockingHub) Send(agentID string, msg interface{}) error {
	message, ok := msg.(protocol.Message)
	if !ok {
		return errors.New("message is not protocol.Message")
	}
	h.entered <- struct{}{}
	<-h.release
	h.sent <- message
	return nil
}

func (h *blockingHub) waitForSend(t *testing.T) {
	t.Helper()
	select {
	case <-h.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for send")
	}
}

func (h *blockingHub) releaseSends() {
	close(h.release)
}

func (h *blockingHub) sendCount() int {
	return len(h.sent)
}

func createCommandForTest(t *testing.T, service *Service, agentID string, msgType string) db.AgentCommand {
	t.Helper()
	var payload any
	taskType := "backup"
	switch msgType {
	case protocol.TypeRestoreReq:
		payload = protocol.RestoreReqPayload{SnapshotID: "snap-1", Target: "/restore"}
		taskType = "restore"
	default:
		payload = protocol.BackupNowPayload{AgentID: agentID}
	}
	msg, err := protocol.NewMessage(msgType, payload)
	require.NoError(t, err)
	command, err := service.CreateCommand(context.Background(), CreateCommandInput{
		AgentID:   agentID,
		Type:      msgType,
		Message:   *msg,
		TaskType:  taskType,
		TaskState: TaskStatusPending,
	})
	require.NoError(t, err)
	return command
}

func createPolicyPushCommandForTest(t *testing.T, service *Service, agentID string) db.AgentCommand {
	t.Helper()
	msg, err := protocol.NewMessage(protocol.TypePolicyPush, protocol.PolicyPushPayload{AgentID: agentID})
	require.NoError(t, err)
	command, err := service.CreateCommand(context.Background(), CreateCommandInput{
		AgentID: agentID,
		Type:    protocol.TypePolicyPush,
		Message: *msg,
	})
	require.NoError(t, err)
	return command
}

func payloadJSON(t *testing.T, msg protocol.Message) map[string]any {
	t.Helper()
	var result map[string]any
	require.NoError(t, json.Unmarshal(msg.Payload, &result))
	return result
}
