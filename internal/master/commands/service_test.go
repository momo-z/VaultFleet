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

func TestDeadlineForTypeMaintenance(t *testing.T) {
	now := time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC)
	assert.Equal(t, now.Add(2*time.Hour), DeadlineForType(protocol.TypeMaintenance, now))
}

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

func TestCreateCommandUsesCustomTimeoutHours(t *testing.T) {
	database := setupCommandTestDB(t)
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	service := NewService(database, nil)
	service.Now = func() time.Time { return now }

	msg, err := protocol.NewMessage(protocol.TypeBackupNow, protocol.BackupNowPayload{AgentID: "agent-1"})
	require.NoError(t, err)

	command, err := service.CreateCommand(context.Background(), CreateCommandInput{
		AgentID:      "agent-1",
		Type:         protocol.TypeBackupNow,
		Message:      *msg,
		TaskType:     "backup",
		TimeoutHours: 2,
	})
	require.NoError(t, err)

	require.NotNil(t, command.DeadlineAt)
	assert.Equal(t, now.Add(2*time.Hour), command.DeadlineAt.UTC())
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

func TestDispatchPendingForAgentFailsLegacyRestoreIncludePathsWithoutCapability(t *testing.T) {
	database := setupCommandTestDB(t)
	require.NoError(t, database.DB.Create(&db.Agent{
		ID:         "agent-1",
		Name:       "Agent 1",
		Status:     "online",
		SystemInfo: `{"capabilities":["snapshot_browse"]}`,
	}).Error)
	hub := &recordingHub{online: map[string]bool{"agent-1": true}}
	service := NewService(database, hub)
	command := createRestoreCommandForTest(t, service, "agent-1", protocol.TypeRestoreReq, []string{"/etc/hosts"})

	require.NoError(t, service.DispatchPendingForAgent(context.Background(), "agent-1", 10))

	assert.Empty(t, hub.sent)
	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusFailed, found.Status)
	assert.Equal(t, 0, found.Attempts)
	assert.Nil(t, found.DispatchedAt)
	assert.Contains(t, found.ErrorMessage, "agent does not support selective restore")
	assert.NotNil(t, found.CompletedAt)

	var history db.TaskHistory
	require.NoError(t, database.DB.First(&history, "command_id = ?", command.ID).Error)
	assert.Equal(t, TaskStatusFailed, history.Status)
	assert.Contains(t, history.ErrorLog, "agent does not support selective restore")
	assert.NotNil(t, history.FinishedAt)
}

func TestDispatchPendingForAgentLeavesLegacyRestoreIncludePathsPendingWhenCapabilityUnknown(t *testing.T) {
	database := setupCommandTestDB(t)
	require.NoError(t, database.DB.Create(&db.Agent{
		ID:         "agent-1",
		Name:       "Agent 1",
		Status:     "online",
		SystemInfo: `{"os":"linux"}`,
	}).Error)
	hub := &recordingHub{online: map[string]bool{"agent-1": true}}
	service := NewService(database, hub)
	command := createRestoreCommandForTest(t, service, "agent-1", protocol.TypeRestoreReq, []string{"/etc/hosts"})

	require.NoError(t, service.DispatchPendingForAgent(context.Background(), "agent-1", 10))

	assert.Empty(t, hub.sent)
	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusPending, found.Status)
	assert.Equal(t, 0, found.Attempts)
	assert.Nil(t, found.DispatchedAt)
	assert.Empty(t, found.ErrorMessage)
	assert.Nil(t, found.CompletedAt)

	var history db.TaskHistory
	require.NoError(t, database.DB.First(&history, "command_id = ?", command.ID).Error)
	assert.Equal(t, TaskStatusPending, history.Status)
	assert.Empty(t, history.ErrorLog)
	assert.Nil(t, history.FinishedAt)
}

func TestDispatchPendingForAgentSendsLegacyRestoreIncludePathsAsSelectiveWhenSupported(t *testing.T) {
	database := setupCommandTestDB(t)
	agent := db.Agent{
		ID:         "agent-1",
		Name:       "Agent 1",
		Status:     "online",
		SystemInfo: `{"capabilities":["restore_include_paths"]}`,
	}
	require.NoError(t, database.DB.Create(&agent).Error)
	hub := &recordingHub{online: map[string]bool{"agent-1": true}}
	service := NewService(database, hub)
	command := createRestoreCommandForTest(t, service, "agent-1", protocol.TypeRestoreReq, []string{"/etc/hosts"})

	require.NoError(t, service.DispatchPendingForAgent(context.Background(), "agent-1", 10))

	require.Len(t, hub.sent, 1)
	assert.Equal(t, protocol.TypeSelectiveRestoreReq, hub.sent[0].Type)
	payload, err := protocol.ParsePayload[protocol.RestoreReqPayload](&hub.sent[0])
	require.NoError(t, err)
	assert.Equal(t, []string{"/etc/hosts"}, payload.IncludePaths)

	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusRunning, found.Status)
	assert.Equal(t, protocol.TypeSelectiveRestoreReq, found.Type)
	assert.Equal(t, 1, found.Attempts)
	assert.NotNil(t, found.DispatchedAt)

	persistedMessage, err := service.messageFromCommand(found)
	require.NoError(t, err)
	assert.Equal(t, protocol.TypeSelectiveRestoreReq, persistedMessage.Type)
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
		assert.Equal(t, CommandStatusRunning, duringSend.Status)
		assert.Equal(t, 1, duringSend.Attempts)
		assert.NotNil(t, duringSend.DispatchedAt)
	}
	var historyDuringSend db.TaskHistory
	if assert.NoError(t, database.DB.First(&historyDuringSend, "command_id = ?", command.ID).Error) {
		assert.Equal(t, TaskStatusRunning, historyDuringSend.Status)
		assert.NotNil(t, historyDuringSend.StartedAt, "started_at should be set when task transitions to running")
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

func TestDispatchPendingRecordsActiveStateBeforeSend(t *testing.T) {
	tests := []struct {
		name           string
		commandType    string
		expectedStatus string
		expectedTask   string
	}{
		{
			name:           "long running",
			commandType:    protocol.TypeBackupNow,
			expectedStatus: CommandStatusRunning,
			expectedTask:   TaskStatusRunning,
		},
		{
			name:           "short command",
			commandType:    protocol.TypePolicyPush,
			expectedStatus: CommandStatusDispatched,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database := setupCommandTestDB(t)
			service := NewService(database, nil)
			var command db.AgentCommand
			if tt.commandType == protocol.TypePolicyPush {
				command = createPolicyPushCommandForTest(t, service, "agent-1")
			} else {
				command = createCommandForTest(t, service, "agent-1", tt.commandType)
			}
			hub := &inspectingHub{
				online: map[string]bool{"agent-1": true},
				onSend: func(t *testing.T, message protocol.Message) {
					t.Helper()

					var duringSend db.AgentCommand
					require.NoError(t, database.DB.First(&duringSend, "id = ?", command.ID).Error)
					assert.Equal(t, tt.expectedStatus, duringSend.Status)
					assert.Equal(t, 1, duringSend.Attempts)
					assert.NotNil(t, duringSend.DispatchedAt)
					assert.Empty(t, duringSend.ErrorMessage)

					if tt.expectedTask != "" {
						var history db.TaskHistory
						require.NoError(t, database.DB.First(&history, "command_id = ?", command.ID).Error)
						assert.Equal(t, tt.expectedTask, history.Status)
					}
				},
				t: t,
			}
			service.Hub = hub

			require.NoError(t, service.DispatchPendingForAgent(context.Background(), "agent-1", 10))
			require.Len(t, hub.sent, 1)
		})
	}
}

func TestDispatchPendingSendFailureRollsBackPreDispatchStateWithoutExtraAttempt(t *testing.T) {
	database := setupCommandTestDB(t)
	service := NewService(database, nil)
	command := createCommandForTest(t, service, "agent-1", protocol.TypeBackupNow)
	hub := &inspectingHub{
		online: map[string]bool{"agent-1": true},
		err:    errors.New("write failed"),
		onSend: func(t *testing.T, message protocol.Message) {
			t.Helper()

			var duringSend db.AgentCommand
			require.NoError(t, database.DB.First(&duringSend, "id = ?", command.ID).Error)
			assert.Equal(t, CommandStatusRunning, duringSend.Status)
			assert.Equal(t, 1, duringSend.Attempts)
			assert.NotNil(t, duringSend.DispatchedAt)

			var history db.TaskHistory
			require.NoError(t, database.DB.First(&history, "command_id = ?", command.ID).Error)
			assert.Equal(t, TaskStatusRunning, history.Status)
		},
		t: t,
	}
	service.Hub = hub

	require.NoError(t, service.DispatchPendingForAgent(context.Background(), "agent-1", 10))

	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusPending, found.Status)
	assert.Equal(t, 1, found.Attempts)
	assert.Nil(t, found.DispatchedAt)
	assert.Contains(t, found.ErrorMessage, "write failed")

	var history db.TaskHistory
	require.NoError(t, database.DB.First(&history, "command_id = ?", command.ID).Error)
	assert.Equal(t, TaskStatusPending, history.Status)
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

func TestCompleteTaskResultUpdatesMaintenanceCommandAndTaskHistory(t *testing.T) {
	database := setupCommandTestDB(t)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	service := NewService(database, nil)
	service.Now = func() time.Time { return now }
	startedAt := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(3 * time.Second)
	msg, err := protocol.NewMessage(protocol.TypeMaintenance, protocol.MaintenancePayload{AgentID: "agent-1", Operation: "check"})
	require.NoError(t, err)
	command, err := service.CreateCommand(context.Background(), CreateCommandInput{
		AgentID:   "agent-1",
		Type:      protocol.TypeMaintenance,
		Message:   *msg,
		TaskType:  "maintenance",
		TaskState: TaskStatusPending,
	})
	require.NoError(t, err)
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).Where("id = ?", command.ID).Update("status", CommandStatusDispatched).Error)

	require.NoError(t, service.CompleteTaskResult(context.Background(), "agent-1", msg.ID, protocol.TaskResultPayload{
		AgentID:    "agent-1",
		TaskType:   "maintenance",
		Status:     "success",
		Output:     "no errors were found",
		DurationMs: 3000,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	}))

	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusSucceeded, found.Status)
	assert.Contains(t, found.Result, `"output":"no errors were found"`)

	var history db.TaskHistory
	require.NoError(t, database.DB.First(&history, "command_id = ?", command.ID).Error)
	assert.Equal(t, TaskStatusSuccess, history.Status)
	assert.Equal(t, "no errors were found", history.Output)
	assert.Equal(t, int64(3000), history.DurationMs)
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

func TestCompleteTaskResultCancelledMapsToCancelledCommand(t *testing.T) {
	database := setupCommandTestDB(t)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	service := NewService(database, nil)
	service.Now = func() time.Time { return now }
	command := createCommandForTest(t, service, "agent-1", protocol.TypeBackupNow)
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).Where("id = ?", command.ID).Update("status", CommandStatusRunning).Error)
	require.NoError(t, database.DB.Model(&db.TaskHistory{}).Where("command_id = ?", command.ID).Update("status", TaskStatusRunning).Error)

	completed, err := service.CompleteTaskResultWith(context.Background(), "agent-1", command.MessageID, protocol.TaskResultPayload{
		AgentID:  "agent-1",
		TaskType: "backup",
		Status:   TaskStatusCancelled,
		ErrorLog: "cancelled by user",
	}, nil)

	require.NoError(t, err)
	assert.True(t, completed)
	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusCancelled, found.Status)
	assert.Equal(t, "cancelled by user", found.ErrorMessage)
	require.NotNil(t, found.CompletedAt)
	assert.True(t, found.CompletedAt.Equal(now))

	var history db.TaskHistory
	require.NoError(t, database.DB.First(&history, "command_id = ?", command.ID).Error)
	assert.Equal(t, TaskStatusCancelled, history.Status)
	assert.Equal(t, "cancelled by user", history.ErrorLog)
	require.NotNil(t, history.FinishedAt)
	assert.True(t, history.FinishedAt.Equal(now))
}

func TestCancelCommandPendingMarksCancelledDirectly(t *testing.T) {
	database := setupCommandTestDB(t)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	service := NewService(database, nil)
	service.Now = func() time.Time { return now }
	command := createCommandForTest(t, service, "agent-1", protocol.TypeBackupNow)

	result, err := service.CancelCommand(context.Background(), command.ID)

	require.NoError(t, err)
	assert.False(t, result.NeedsWS)
	assert.Empty(t, result.AgentID)
	assert.Empty(t, result.MessageID)

	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusCancelled, found.Status)
	assert.Equal(t, "cancelled by user", found.ErrorMessage)
	require.NotNil(t, found.CompletedAt)
	assert.True(t, found.CompletedAt.Equal(now))

	var history db.TaskHistory
	require.NoError(t, database.DB.First(&history, "command_id = ?", command.ID).Error)
	assert.Equal(t, TaskStatusCancelled, history.Status)
	assert.Equal(t, "cancelled by user", history.ErrorLog)
	require.NotNil(t, history.FinishedAt)
	assert.True(t, history.FinishedAt.Equal(now))
}

func TestCancelCommandRunningReturnsNeedsWS(t *testing.T) {
	database := setupCommandTestDB(t)
	service := NewService(database, nil)
	command := createCommandForTest(t, service, "agent-1", protocol.TypeBackupNow)
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).Where("id = ?", command.ID).Update("status", CommandStatusRunning).Error)
	require.NoError(t, database.DB.Model(&db.TaskHistory{}).Where("command_id = ?", command.ID).Update("status", TaskStatusRunning).Error)

	result, err := service.CancelCommand(context.Background(), command.ID)

	require.NoError(t, err)
	assert.True(t, result.NeedsWS)
	assert.Equal(t, "agent-1", result.AgentID)
	assert.Equal(t, command.MessageID, result.MessageID)

	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusRunning, found.Status)
}

func TestCancelCommandDispatchedReturnsNeedsWS(t *testing.T) {
	database := setupCommandTestDB(t)
	service := NewService(database, nil)
	command := createCommandForTest(t, service, "agent-1", protocol.TypeBackupNow)
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).Where("id = ?", command.ID).Update("status", CommandStatusDispatched).Error)

	result, err := service.CancelCommand(context.Background(), command.ID)

	require.NoError(t, err)
	assert.True(t, result.NeedsWS)
	assert.Equal(t, "agent-1", result.AgentID)
	assert.Equal(t, command.MessageID, result.MessageID)
}

func TestCancelCommandTerminalReturnsError(t *testing.T) {
	database := setupCommandTestDB(t)
	service := NewService(database, nil)
	command := createCommandForTest(t, service, "agent-1", protocol.TypeBackupNow)
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).Where("id = ?", command.ID).Update("status", CommandStatusSucceeded).Error)

	_, err := service.CancelCommand(context.Background(), command.ID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminal")
}

func TestCancelCommandCancelledTerminalReturnsError(t *testing.T) {
	database := setupCommandTestDB(t)
	service := NewService(database, nil)
	command := createCommandForTest(t, service, "agent-1", protocol.TypeBackupNow)
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).Where("id = ?", command.ID).Update("status", CommandStatusCancelled).Error)

	_, err := service.CancelCommand(context.Background(), command.ID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminal")
}

func TestTimeoutExpiredDoesNotRewriteTerminalTaskHistory(t *testing.T) {
	for _, terminalStatus := range []string{TaskStatusSuccess, TaskStatusFailed, TaskStatusCancelled} {
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

func TestTimeoutExpiredSendsCancelForRunningCommand(t *testing.T) {
	database := setupCommandTestDB(t)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	hub := &recordingHub{online: map[string]bool{"agent-1": true}}
	service := NewService(database, hub)
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
	require.Len(t, hub.sent, 1)
	assert.Equal(t, protocol.TypeCancelTask, hub.sent[0].Type)
	payload, err := protocol.ParsePayload[protocol.CancelTaskPayload](&hub.sent[0])
	require.NoError(t, err)
	assert.Equal(t, "agent-1", payload.AgentID)
	assert.Equal(t, command.MessageID, payload.MessageID)
}

func TestTimeoutExpiredSendsCancelForDispatchedCommand(t *testing.T) {
	database := setupCommandTestDB(t)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	hub := &recordingHub{online: map[string]bool{"agent-1": true}}
	service := NewService(database, hub)
	service.Now = func() time.Time { return now }
	command := createCommandForTest(t, service, "agent-1", protocol.TypeBackupNow)
	pastDeadline := now.Add(-time.Second)
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).
		Where("id = ?", command.ID).
		Updates(map[string]any{
			"status":      CommandStatusDispatched,
			"deadline_at": &pastDeadline,
		}).Error)

	count, err := service.TimeoutExpired(context.Background())

	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
	require.Len(t, hub.sent, 1)
	assert.Equal(t, protocol.TypeCancelTask, hub.sent[0].Type)
	payload, err := protocol.ParsePayload[protocol.CancelTaskPayload](&hub.sent[0])
	require.NoError(t, err)
	assert.Equal(t, command.MessageID, payload.MessageID)
}

func TestTimeoutExpiredSendsCancelForDispatchedSnapshotListCommand(t *testing.T) {
	database := setupCommandTestDB(t)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	hub := &recordingHub{online: map[string]bool{"agent-1": true}}
	service := NewService(database, hub)
	service.Now = func() time.Time { return now }
	msg, err := protocol.NewMessage(protocol.TypeSnapshotListReq, protocol.SnapshotListReqPayload{AgentID: "agent-1"})
	require.NoError(t, err)
	command, err := service.CreateCommand(context.Background(), CreateCommandInput{
		AgentID: "agent-1",
		Type:    protocol.TypeSnapshotListReq,
		Message: *msg,
	})
	require.NoError(t, err)
	pastDeadline := now.Add(-time.Second)
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).
		Where("id = ?", command.ID).
		Updates(map[string]any{
			"status":      CommandStatusDispatched,
			"deadline_at": &pastDeadline,
		}).Error)

	count, err := service.TimeoutExpired(context.Background())

	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
	require.Len(t, hub.sent, 1)
	assert.Equal(t, protocol.TypeCancelTask, hub.sent[0].Type)
	payload, err := protocol.ParsePayload[protocol.CancelTaskPayload](&hub.sent[0])
	require.NoError(t, err)
	assert.Equal(t, command.MessageID, payload.MessageID)
}

func TestTimeoutExpiredDoesNotSendCancelForPendingCommand(t *testing.T) {
	database := setupCommandTestDB(t)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	hub := &recordingHub{online: map[string]bool{"agent-1": true}}
	service := NewService(database, hub)
	service.Now = func() time.Time { return now }
	command := createCommandForTest(t, service, "agent-1", protocol.TypeBackupNow)
	pastDeadline := now.Add(-time.Second)
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).
		Where("id = ?", command.ID).
		Update("deadline_at", &pastDeadline).Error)

	count, err := service.TimeoutExpired(context.Background())

	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
	assert.Empty(t, hub.sent)
	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusTimeout, found.Status)
}

func TestTimeoutExpiredIgnoresCancelSendFailure(t *testing.T) {
	database := setupCommandTestDB(t)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	hub := &recordingHub{online: map[string]bool{"agent-1": true}, err: errors.New("write failed")}
	service := NewService(database, hub)
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
}

func TestRunTimeoutScannerMarksExpiredCommandsAndStopsOnContextCancel(t *testing.T) {
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

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.RunTimeoutScanner(ctx, 5*time.Millisecond)
	}()

	require.Eventually(t, func() bool {
		var found db.AgentCommand
		if err := database.DB.First(&found, "id = ?", command.ID).Error; err != nil {
			return false
		}
		return found.Status == CommandStatusTimeout
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	require.Eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, 2*time.Second, 10*time.Millisecond)

	var history db.TaskHistory
	require.NoError(t, database.DB.First(&history, "command_id = ?", command.ID).Error)
	assert.Equal(t, TaskStatusTimeout, history.Status)
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

type inspectingHub struct {
	online map[string]bool
	err    error
	sent   []protocol.Message
	onSend func(*testing.T, protocol.Message)
	t      *testing.T
}

func (h *inspectingHub) IsOnline(agentID string) bool {
	return h.online[agentID]
}

func (h *inspectingHub) Send(agentID string, msg interface{}) error {
	message, ok := msg.(protocol.Message)
	if !ok {
		return errors.New("message is not protocol.Message")
	}
	if h.onSend != nil {
		h.onSend(h.t, message)
	}
	if h.err != nil {
		return h.err
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

func createRestoreCommandForTest(t *testing.T, service *Service, agentID string, msgType string, includePaths []string) db.AgentCommand {
	t.Helper()
	msg, err := protocol.NewMessage(msgType, protocol.RestoreReqPayload{
		SnapshotID:   "snap-1",
		Target:       "/restore",
		IncludePaths: includePaths,
	})
	require.NoError(t, err)
	command, err := service.CreateCommand(context.Background(), CreateCommandInput{
		AgentID:    agentID,
		Type:       msgType,
		Message:    *msg,
		TaskType:   "restore",
		TaskState:  TaskStatusPending,
		SnapshotID: "snap-1",
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
