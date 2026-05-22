package main

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vaultfleet/internal/master/commands"
	"vaultfleet/internal/master/db"
	"vaultfleet/internal/master/events"
	"vaultfleet/pkg/protocol"
)

func TestBuildRuntimeWiresDurableCommandService(t *testing.T) {
	database, err := db.New(t.TempDir())
	require.NoError(t, err)
	agent := db.Agent{Name: "Runtime Agent", AgentToken: "runtime-token", Status: "online"}
	require.NoError(t, database.DB.Create(&agent).Error)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runtime := buildRuntime(ctx, database, nil)

	require.NotNil(t, runtime.commandService)
	require.NotNil(t, runtime.wsHandler.PendingCommandDispatcher)
	require.NotNil(t, runtime.wsHandler.PolicyAckProcessor)
	require.NotNil(t, runtime.policyPusher.Commands)
	assert.Same(t, runtime.commandService, runtime.policyPusher.Commands)

	queued := createMasterTestPolicyPushCommand(t, runtime.commandService, agent.ID)
	server := httptest.NewServer(runtime.router)
	t.Cleanup(server.Close)

	conn, _, err := websocket.DefaultDialer.Dial(masterWebSocketURL(server.URL, "runtime-token"), nil)
	require.NoError(t, err)
	defer conn.Close()

	var queuedPush protocol.Message
	require.NoError(t, conn.ReadJSON(&queuedPush))
	assert.Equal(t, queued.MessageID, queuedPush.ID)

	require.Eventually(t, func() bool {
		var dispatched db.AgentCommand
		require.NoError(t, database.DB.First(&dispatched, "id = ?", queued.ID).Error)
		return dispatched.Status == commands.CommandStatusDispatched
	}, time.Second, 10*time.Millisecond)

	storage := db.StorageConfig{
		Name:         "Runtime Storage",
		RcloneType:   "s3",
		RcloneConfig: encryptMasterTestMap(t, database, `{"provider":"Cloudflare","access_key_id":"AKID","secret_access_key":"SECRET"}`),
	}
	require.NoError(t, database.DB.Create(&storage).Error)
	policy := createMasterTestPolicy(t, database, agent.ID, storage.ID)
	runtime.policyPusher.Handle(events.Event{
		Type: events.PolicyChanged,
		Payload: map[string]interface{}{
			"agent_id": agent.ID,
			"action":   "updated",
		},
	})

	var pushedMessage protocol.Message
	require.NoError(t, conn.ReadJSON(&pushedMessage))
	var pushed db.AgentCommand
	require.NoError(t, database.DB.First(&pushed, "agent_id = ? AND type = ? AND policy_id = ?", agent.ID, protocol.TypePolicyPush, policy.ID).Error)
	assert.Equal(t, commands.CommandStatusDispatched, pushed.Status)
	assert.Equal(t, storage.ID, pushed.StorageID)
	assert.Equal(t, pushed.MessageID, pushedMessage.ID)

	ack := masterPolicyAckMessage(t, pushed.MessageID, agent.ID)
	require.NoError(t, conn.WriteJSON(ack))
	require.Eventually(t, func() bool {
		var completed db.AgentCommand
		require.NoError(t, database.DB.First(&completed, "id = ?", pushed.ID).Error)
		return completed.Status == commands.CommandStatusSucceeded && completed.CompletedAt != nil
	}, time.Second, 10*time.Millisecond)
}

func TestRuntimeReconnectPolicyPushIsDurableAndNotDuplicated(t *testing.T) {
	database, err := db.New(t.TempDir())
	require.NoError(t, err)
	agent := db.Agent{Name: "Reconnect Agent", AgentToken: "reconnect-token", Status: "offline"}
	require.NoError(t, database.DB.Create(&agent).Error)
	storage := db.StorageConfig{
		Name:         "Reconnect Storage",
		RcloneType:   "s3",
		RcloneConfig: encryptMasterTestMap(t, database, `{"provider":"Cloudflare","access_key_id":"AKID","secret_access_key":"SECRET"}`),
	}
	require.NoError(t, database.DB.Create(&storage).Error)
	policy := createMasterTestPolicy(t, database, agent.ID, storage.ID)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runtime := buildRuntime(ctx, database, nil)
	server := httptest.NewServer(runtime.router)
	t.Cleanup(server.Close)

	conn, _, err := websocket.DefaultDialer.Dial(masterWebSocketURL(server.URL, "reconnect-token"), nil)
	require.NoError(t, err)
	defer conn.Close()

	var pushed protocol.Message
	require.NoError(t, conn.ReadJSON(&pushed))
	require.Equal(t, protocol.TypePolicyPush, pushed.Type)

	var command db.AgentCommand
	require.NoError(t, database.DB.First(&command, "agent_id = ? AND message_id = ?", agent.ID, pushed.ID).Error)
	assert.Equal(t, protocol.TypePolicyPush, command.Type)
	assert.Equal(t, policy.ID, command.PolicyID)
	assert.Equal(t, storage.ID, command.StorageID)
	require.Eventually(t, func() bool {
		var updated db.AgentCommand
		require.NoError(t, database.DB.First(&updated, "id = ?", command.ID).Error)
		return updated.Status == commands.CommandStatusDispatched
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(100*time.Millisecond)))
	var duplicate protocol.Message
	err = conn.ReadJSON(&duplicate)
	require.Error(t, err)
	var netErr net.Error
	require.True(t, errors.As(err, &netErr) && netErr.Timeout(), "expected read timeout without duplicate policy_push, got %v", err)
}

func TestRuntimePolicyAckAfterTrackerRestartMarksPolicySynced(t *testing.T) {
	database, err := db.New(t.TempDir())
	require.NoError(t, err)
	agent := db.Agent{Name: "Restart Agent", AgentToken: "restart-token", Status: "offline"}
	require.NoError(t, database.DB.Create(&agent).Error)
	storage := db.StorageConfig{
		Name:         "Restart Storage",
		RcloneType:   "s3",
		RcloneConfig: encryptMasterTestMap(t, database, `{"provider":"Cloudflare","access_key_id":"AKID","secret_access_key":"SECRET"}`),
	}
	require.NoError(t, database.DB.Create(&storage).Error)
	policy := createMasterTestPolicy(t, database, agent.ID, storage.ID)

	firstCtx, firstCancel := context.WithCancel(context.Background())
	t.Cleanup(firstCancel)
	firstRuntime := buildRuntime(firstCtx, database, nil)
	require.True(t, firstRuntime.policyPusher.EnsureDurableCommand(context.Background(), agent.ID))

	var pending db.AgentCommand
	require.NoError(t, database.DB.First(&pending, "agent_id = ? AND type = ? AND policy_id = ?", agent.ID, protocol.TypePolicyPush, policy.ID).Error)
	require.Equal(t, commands.CommandStatusPending, pending.Status)

	restartedCtx, restartedCancel := context.WithCancel(context.Background())
	t.Cleanup(restartedCancel)
	restartedRuntime := buildRuntime(restartedCtx, database, nil)
	server := httptest.NewServer(restartedRuntime.router)
	t.Cleanup(server.Close)

	conn, _, err := websocket.DefaultDialer.Dial(masterWebSocketURL(server.URL, "restart-token"), nil)
	require.NoError(t, err)
	defer conn.Close()

	var pushed protocol.Message
	require.NoError(t, conn.ReadJSON(&pushed))
	require.Equal(t, pending.MessageID, pushed.ID)
	require.NoError(t, conn.WriteJSON(masterPolicyAckMessage(t, pushed.ID, agent.ID)))

	require.Eventually(t, func() bool {
		var command db.AgentCommand
		require.NoError(t, database.DB.First(&command, "id = ?", pending.ID).Error)
		var storedPolicy db.BackupPolicy
		require.NoError(t, database.DB.First(&storedPolicy, "id = ?", policy.ID).Error)
		return command.Status == commands.CommandStatusSucceeded && storedPolicy.Synced
	}, time.Second, 10*time.Millisecond)
}

func TestRuntimeTaskResultCompletesDurableBackupCommand(t *testing.T) {
	database, err := db.New(t.TempDir())
	require.NoError(t, err)
	agent := db.Agent{Name: "Task Result Agent", AgentToken: "task-result-token", Status: "offline"}
	require.NoError(t, database.DB.Create(&agent).Error)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runtime := buildRuntime(ctx, database, nil)
	command := createMasterTestBackupCommand(t, runtime.commandService, agent.ID)
	server := httptest.NewServer(runtime.router)
	t.Cleanup(server.Close)

	conn, _, err := websocket.DefaultDialer.Dial(masterWebSocketURL(server.URL, agent.AgentToken), nil)
	require.NoError(t, err)
	defer conn.Close()

	var dispatched protocol.Message
	require.NoError(t, conn.ReadJSON(&dispatched))
	require.Equal(t, command.MessageID, dispatched.ID)
	require.Equal(t, protocol.TypeBackupNow, dispatched.Type)

	finishedAt := time.Now().UTC()
	result, err := protocol.NewMessage(protocol.TypeTaskResult, protocol.TaskResultPayload{
		AgentID:    agent.ID,
		TaskType:   "backup",
		Status:     commands.TaskStatusSuccess,
		SnapshotID: "runtime-backup-snap",
		DurationMs: 1234,
		RepoSize:   4096,
		StartedAt:  finishedAt.Add(-time.Minute),
		FinishedAt: finishedAt,
		Snapshots: []protocol.SnapshotInfo{
			{ID: "runtime-backup-snap", Time: finishedAt, Paths: []string{"/etc"}, Size: 4096},
		},
	})
	require.NoError(t, err)
	result.ID = dispatched.ID
	require.NoError(t, conn.WriteJSON(result))

	require.Eventually(t, func() bool {
		var completed db.AgentCommand
		require.NoError(t, database.DB.First(&completed, "id = ?", command.ID).Error)
		return completed.Status == commands.CommandStatusSucceeded && completed.CompletedAt != nil && completed.Result != ""
	}, time.Second, 10*time.Millisecond)

	var histories []db.TaskHistory
	require.NoError(t, database.DB.Find(&histories, "agent_id = ? AND message_id = ?", agent.ID, dispatched.ID).Error)
	require.Len(t, histories, 1)
	assert.Equal(t, command.ID, histories[0].CommandID)
	assert.Equal(t, commands.TaskStatusSuccess, histories[0].Status)
	assert.Equal(t, "runtime-backup-snap", histories[0].SnapshotID)

	var snapshot db.Snapshot
	require.NoError(t, database.DB.First(&snapshot, "agent_id = ? AND snapshot_id = ?", agent.ID, "runtime-backup-snap").Error)
}

func TestRuntimeSnapshotListResponseCompletesDurableCommandWithoutWaiter(t *testing.T) {
	database, err := db.New(t.TempDir())
	require.NoError(t, err)
	agent := db.Agent{Name: "Snapshot Response Agent", AgentToken: "snapshot-response-token", Status: "offline"}
	require.NoError(t, database.DB.Create(&agent).Error)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runtime := buildRuntime(ctx, database, nil)
	command := createMasterTestSnapshotListCommand(t, runtime.commandService, agent.ID)
	server := httptest.NewServer(runtime.router)
	t.Cleanup(server.Close)

	conn, _, err := websocket.DefaultDialer.Dial(masterWebSocketURL(server.URL, agent.AgentToken), nil)
	require.NoError(t, err)
	defer conn.Close()

	var dispatched protocol.Message
	require.NoError(t, conn.ReadJSON(&dispatched))
	require.Equal(t, command.MessageID, dispatched.ID)
	require.Equal(t, protocol.TypeSnapshotListReq, dispatched.Type)

	snapshotTime := time.Date(2026, 5, 20, 16, 30, 0, 0, time.UTC)
	response, err := protocol.NewMessage(protocol.TypeSnapshotListResp, protocol.SnapshotListRespPayload{
		AgentID: agent.ID,
		Snapshots: []protocol.SnapshotInfo{
			{ID: "runtime-list-snap", Time: snapshotTime, Paths: []string{"/srv"}, Size: 8192},
		},
	})
	require.NoError(t, err)
	response.ID = dispatched.ID
	require.NoError(t, conn.WriteJSON(response))

	require.Eventually(t, func() bool {
		var completed db.AgentCommand
		require.NoError(t, database.DB.First(&completed, "id = ?", command.ID).Error)
		return completed.Status == commands.CommandStatusSucceeded && completed.CompletedAt != nil && completed.Result != ""
	}, time.Second, 10*time.Millisecond)

	var snapshot db.Snapshot
	require.NoError(t, database.DB.First(&snapshot, "agent_id = ? AND snapshot_id = ?", agent.ID, "runtime-list-snap").Error)
	assert.True(t, snapshot.Timestamp.Equal(snapshotTime))
	assert.Equal(t, int64(8192), snapshot.Size)
}

func TestRuntimeStartsCommandTimeoutScanner(t *testing.T) {
	database, err := db.New(t.TempDir())
	require.NoError(t, err)
	agent := db.Agent{Name: "Timeout Agent", AgentToken: "timeout-token", Status: "offline"}
	require.NoError(t, database.DB.Create(&agent).Error)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runtime := buildRuntimeWithOptions(ctx, database, runtimeOptions{
		commandTimeoutScanInterval: 5 * time.Millisecond,
	}, nil)
	command := createMasterTestBackupCommand(t, runtime.commandService, agent.ID)
	expiredAt := time.Now().Add(-time.Minute)
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).
		Where("id = ?", command.ID).
		Updates(map[string]any{
			"status":      commands.CommandStatusPending,
			"deadline_at": &expiredAt,
		}).Error)

	require.Eventually(t, func() bool {
		var timedOut db.AgentCommand
		require.NoError(t, database.DB.First(&timedOut, "id = ?", command.ID).Error)
		return timedOut.Status == commands.CommandStatusTimeout && timedOut.CompletedAt != nil
	}, time.Second, 10*time.Millisecond)

	var history db.TaskHistory
	require.NoError(t, database.DB.First(&history, "command_id = ?", command.ID).Error)
	assert.Equal(t, commands.TaskStatusTimeout, history.Status)
	assert.NotNil(t, history.FinishedAt)
}

func masterWebSocketURL(serverURL string, token string) string {
	u, err := url.Parse(serverURL)
	if err != nil {
		panic(err)
	}
	u.Scheme = "ws"
	u.Path = "/ws/agent"
	u.RawQuery = url.Values{"token": []string{token}}.Encode()
	return u.String()
}

func encryptMasterTestMap(t *testing.T, database *db.Database, plaintext string) string {
	t.Helper()
	ciphertext, err := db.Encrypt(plaintext, database.MasterKey)
	require.NoError(t, err)
	return ciphertext
}

func createMasterTestPolicy(t *testing.T, database *db.Database, agentID string, storageID string) db.BackupPolicy {
	t.Helper()
	encryptedPassword, err := db.Encrypt("restic-password", database.MasterKey)
	require.NoError(t, err)
	policy := db.BackupPolicy{
		AgentID:         agentID,
		StorageID:       storageID,
		RepoPath:        "vaultfleet/" + agentID,
		ResticPassword:  encryptedPassword,
		BackupDirs:      `["/etc"]`,
		ExcludePatterns: `[]`,
		Schedule:        "0 3 * * *",
		Retention:       `{"keep_last":3}`,
		Synced:          false,
	}
	require.NoError(t, database.DB.Create(&policy).Error)
	return policy
}

func createMasterTestPolicyPushCommand(t *testing.T, service *commands.Service, agentID string) db.AgentCommand {
	t.Helper()
	msg, err := protocol.NewMessage(protocol.TypePolicyPush, protocol.PolicyPushPayload{AgentID: agentID})
	require.NoError(t, err)
	command, err := service.CreateCommand(context.Background(), commands.CreateCommandInput{
		AgentID: agentID,
		Type:    protocol.TypePolicyPush,
		Message: *msg,
	})
	require.NoError(t, err)
	return command
}

func createMasterTestBackupCommand(t *testing.T, service *commands.Service, agentID string) db.AgentCommand {
	t.Helper()

	msg, err := protocol.NewMessage(protocol.TypeBackupNow, protocol.BackupNowPayload{AgentID: agentID})
	require.NoError(t, err)
	command, err := service.CreateCommand(context.Background(), commands.CreateCommandInput{
		AgentID:   agentID,
		Type:      protocol.TypeBackupNow,
		Message:   *msg,
		TaskType:  "backup",
		TaskState: commands.TaskStatusPending,
	})
	require.NoError(t, err)
	return command
}

func createMasterTestSnapshotListCommand(t *testing.T, service *commands.Service, agentID string) db.AgentCommand {
	t.Helper()

	msg, err := protocol.NewMessage(protocol.TypeSnapshotListReq, protocol.SnapshotListReqPayload{AgentID: agentID})
	require.NoError(t, err)
	command, err := service.CreateCommand(context.Background(), commands.CreateCommandInput{
		AgentID: agentID,
		Type:    protocol.TypeSnapshotListReq,
		Message: *msg,
	})
	require.NoError(t, err)
	return command
}

func masterPolicyAckMessage(t *testing.T, messageID string, agentID string) *protocol.Message {
	t.Helper()
	msg, err := protocol.NewMessage(protocol.TypePolicyAck, protocol.PolicyAckPayload{
		AgentID: agentID,
		Success: true,
	})
	require.NoError(t, err)
	msg.ID = messageID
	return msg
}
