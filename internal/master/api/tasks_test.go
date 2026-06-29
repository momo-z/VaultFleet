package api

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vaultfleet/internal/master/commands"
	"vaultfleet/internal/master/db"
	"vaultfleet/pkg/protocol"
)

func TestBackupNowSendsAgentCommand(t *testing.T) {
	setup := setupTasksAPI(t)
	agent := createTasksTestAgent(t, setup.database, "online")
	setup.hub.online[agent.ID] = true

	w := postAnyJSON(t, setup.router, "/api/agents/"+agent.ID+"/backup-now", map[string]any{})

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, true, body["ok"])
	data := requireMap(t, body["data"])
	assert.NotEmpty(t, data["message_id"])
	assert.NotEmpty(t, data["command_id"])
	require.Len(t, setup.hub.sent, 1)
	assert.Equal(t, agent.ID, setup.hub.sent[0].agentID)
	assert.Equal(t, protocol.TypeBackupNow, setup.hub.sent[0].message.Type)
	assert.Equal(t, data["message_id"], setup.hub.sent[0].message.ID)
	payload, err := protocol.ParsePayload[protocol.BackupNowPayload](&setup.hub.sent[0].message)
	require.NoError(t, err)
	assert.Equal(t, agent.ID, payload.AgentID)

	var command db.AgentCommand
	require.NoError(t, setup.database.DB.First(&command, "id = ?", data["command_id"]).Error)
	assert.Equal(t, agent.ID, command.AgentID)
	assert.Equal(t, protocol.TypeBackupNow, command.Type)
	assert.Equal(t, commands.CommandStatusRunning, command.Status)
	assert.Equal(t, data["message_id"], command.MessageID)
	assert.Equal(t, 1, command.Attempts)
	assert.NotNil(t, command.DispatchedAt)

	var history db.TaskHistory
	require.NoError(t, setup.database.DB.First(&history, "command_id = ?", command.ID).Error)
	assert.Equal(t, agent.ID, history.AgentID)
	assert.Equal(t, "backup", history.Type)
	assert.Equal(t, commands.TaskStatusRunning, history.Status)
	assert.Equal(t, command.ID, history.CommandID)
	assert.Equal(t, data["message_id"], history.MessageID)
}

func TestMaintenanceTriggersCommand(t *testing.T) {
	setup := setupTasksAPI(t)
	agent := createTasksTestAgent(t, setup.database, "online")
	setup.hub.online[agent.ID] = true

	w := postAnyJSON(t, setup.router, "/api/agents/"+agent.ID+"/maintenance", map[string]any{"operation": "check"})

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, true, body["ok"])
	data := requireMap(t, body["data"])
	assert.NotEmpty(t, data["message_id"])
	assert.NotEmpty(t, data["command_id"])
	require.Len(t, setup.hub.sent, 1)
	assert.Equal(t, agent.ID, setup.hub.sent[0].agentID)
	assert.Equal(t, protocol.TypeMaintenance, setup.hub.sent[0].message.Type)
	assert.Equal(t, data["message_id"], setup.hub.sent[0].message.ID)
	payload, err := protocol.ParsePayload[protocol.MaintenancePayload](&setup.hub.sent[0].message)
	require.NoError(t, err)
	assert.Equal(t, agent.ID, payload.AgentID)
	assert.Equal(t, "check", payload.Operation)

	var command db.AgentCommand
	require.NoError(t, setup.database.DB.First(&command, "id = ?", data["command_id"]).Error)
	assert.Equal(t, agent.ID, command.AgentID)
	assert.Equal(t, protocol.TypeMaintenance, command.Type)

	var history db.TaskHistory
	require.NoError(t, setup.database.DB.First(&history, "command_id = ?", command.ID).Error)
	assert.Equal(t, agent.ID, history.AgentID)
	assert.Equal(t, "maintenance", history.Type)
}

func TestMaintenanceRejectsInvalidOperation(t *testing.T) {
	setup := setupTasksAPI(t)
	agent := createTasksTestAgent(t, setup.database, "online")
	setup.hub.online[agent.ID] = true

	w := postAnyJSON(t, setup.router, "/api/agents/"+agent.ID+"/maintenance", map[string]any{"operation": "bogus"})

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Empty(t, setup.hub.sent)
}

func TestBackupNowQueuesOfflineAgentCommand(t *testing.T) {
	setup := setupTasksAPI(t)
	agent := createTasksTestAgent(t, setup.database, "offline")

	w := postAnyJSON(t, setup.router, "/api/agents/"+agent.ID+"/backup-now", map[string]any{})

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, true, body["ok"])
	data := requireMap(t, body["data"])
	assert.NotEmpty(t, data["message_id"])
	assert.NotEmpty(t, data["command_id"])
	assert.Empty(t, setup.hub.sent)

	var command db.AgentCommand
	require.NoError(t, setup.database.DB.First(&command, "id = ?", data["command_id"]).Error)
	assert.Equal(t, agent.ID, command.AgentID)
	assert.Equal(t, protocol.TypeBackupNow, command.Type)
	assert.Equal(t, commands.CommandStatusPending, command.Status)
	assert.Equal(t, data["message_id"], command.MessageID)
	assert.Equal(t, 0, command.Attempts)
	assert.Nil(t, command.DispatchedAt)

	var history db.TaskHistory
	require.NoError(t, setup.database.DB.First(&history, "command_id = ?", command.ID).Error)
	assert.Equal(t, agent.ID, history.AgentID)
	assert.Equal(t, "backup", history.Type)
	assert.Equal(t, commands.TaskStatusPending, history.Status)
	assert.Equal(t, command.ID, history.CommandID)
	assert.Equal(t, data["message_id"], history.MessageID)
}

func TestBackupNowUsesPolicyTimeoutHours(t *testing.T) {
	setup := setupTasksAPI(t)
	agent := createTasksTestAgent(t, setup.database, "offline")
	storage := db.StorageConfig{Name: "Timeout Storage", RcloneType: "s3"}
	require.NoError(t, setup.database.DB.Create(&storage).Error)
	policy := db.BackupPolicy{
		AgentID:         agent.ID,
		StorageID:       storage.ID,
		RepoPath:        "vaultfleet/" + agent.ID,
		BackupDirs:      `["/etc"]`,
		ExcludePatterns: `[]`,
		Schedule:        "0 3 * * *",
		Retention:       `{"keep_last":3}`,
		TimeoutHours:    2,
	}
	require.NoError(t, setup.database.DB.Create(&policy).Error)
	now := time.Date(2026, 5, 27, 9, 0, 0, 0, time.UTC)
	setup.handler.Commands.Now = func() time.Time { return now }

	w := postAnyJSON(t, setup.router, "/api/agents/"+agent.ID+"/backup-now", map[string]any{})

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	body := parseJSON(t, w)
	data := requireMap(t, body["data"])
	var command db.AgentCommand
	require.NoError(t, setup.database.DB.First(&command, "id = ?", data["command_id"]).Error)
	require.NotNil(t, command.DeadlineAt)
	assert.Equal(t, now.Add(2*time.Hour), command.DeadlineAt.UTC())
	assert.Equal(t, policy.ID, command.PolicyID)
	assert.Equal(t, storage.ID, command.StorageID)

	var history db.TaskHistory
	require.NoError(t, setup.database.DB.First(&history, "command_id = ?", command.ID).Error)
	assert.Equal(t, policy.ID, history.PolicyID)
	assert.Equal(t, storage.ID, history.StorageID)
}

func TestCancelPendingTaskMarksCancelled(t *testing.T) {
	setup := setupTasksAPI(t)
	agent := createTasksTestAgent(t, setup.database, "online")
	now := time.Now()

	command := db.AgentCommand{
		AgentID:   agent.ID,
		Type:      protocol.TypeBackupNow,
		Status:    commands.CommandStatusPending,
		MessageID: "msg-1",
	}
	require.NoError(t, setup.database.DB.Create(&command).Error)

	history := db.TaskHistory{
		AgentID:   agent.ID,
		Type:      "backup",
		Status:    commands.TaskStatusPending,
		MessageID: "msg-1",
		CommandID: command.ID,
		CreatedAt: now,
	}
	require.NoError(t, setup.database.DB.Create(&history).Error)

	w := postAnyJSON(t, setup.router, "/api/tasks/"+history.ID+"/cancel", nil)

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())

	var updated db.TaskHistory
	require.NoError(t, setup.database.DB.First(&updated, "id = ?", history.ID).Error)
	assert.Equal(t, commands.TaskStatusCancelled, updated.Status)
	assert.NotNil(t, updated.FinishedAt)
	assert.Empty(t, setup.hub.sent)
}

func TestCancelRunningTaskSendsWSMessage(t *testing.T) {
	setup := setupTasksAPI(t)
	agent := createTasksTestAgent(t, setup.database, "online")
	setup.hub.online[agent.ID] = true
	now := time.Now()

	command := db.AgentCommand{
		AgentID:   agent.ID,
		Type:      protocol.TypeBackupNow,
		Status:    commands.CommandStatusRunning,
		MessageID: "msg-2",
	}
	require.NoError(t, setup.database.DB.Create(&command).Error)

	history := db.TaskHistory{
		AgentID:   agent.ID,
		Type:      "backup",
		Status:    commands.TaskStatusRunning,
		MessageID: "msg-2",
		CommandID: command.ID,
		CreatedAt: now,
	}
	require.NoError(t, setup.database.DB.Create(&history).Error)

	w := postAnyJSON(t, setup.router, "/api/tasks/"+history.ID+"/cancel", nil)

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.Len(t, setup.hub.sent, 1)
	assert.Equal(t, agent.ID, setup.hub.sent[0].agentID)
	assert.Equal(t, protocol.TypeCancelTask, setup.hub.sent[0].message.Type)
	payload, err := protocol.ParsePayload[protocol.CancelTaskPayload](&setup.hub.sent[0].message)
	require.NoError(t, err)
	assert.Equal(t, agent.ID, payload.AgentID)
	assert.Equal(t, command.MessageID, payload.MessageID)
}

func TestCancelRunningTaskReturnsUnavailableWhenHubMissing(t *testing.T) {
	setup := setupTasksAPI(t)
	agent := createTasksTestAgent(t, setup.database, "online")
	history := seedCommandBackedTask(t, setup.database, agent.ID, commands.CommandStatusRunning, commands.TaskStatusRunning, "msg-no-hub")
	setup.handler.Hub = nil
	setup.handler.Commands.Hub = nil

	w := postAnyJSON(t, setup.router, "/api/tasks/"+history.ID+"/cancel", nil)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestCancelRunningTaskReturnsUnavailableWhenAgentOffline(t *testing.T) {
	setup := setupTasksAPI(t)
	agent := createTasksTestAgent(t, setup.database, "online")
	history := seedCommandBackedTask(t, setup.database, agent.ID, commands.CommandStatusRunning, commands.TaskStatusRunning, "msg-offline")

	w := postAnyJSON(t, setup.router, "/api/tasks/"+history.ID+"/cancel", nil)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Empty(t, setup.hub.sent)
}

func TestCancelRunningTaskReturnsUnavailableWhenSendFails(t *testing.T) {
	setup := setupTasksAPI(t)
	agent := createTasksTestAgent(t, setup.database, "online")
	setup.hub.online[agent.ID] = true
	setup.hub.sendErr = errors.New("websocket write failed")
	history := seedCommandBackedTask(t, setup.database, agent.ID, commands.CommandStatusRunning, commands.TaskStatusRunning, "msg-send-fails")

	w := postAnyJSON(t, setup.router, "/api/tasks/"+history.ID+"/cancel", nil)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Empty(t, setup.hub.sent)
}

func TestCancelCompletedTaskReturnsConflict(t *testing.T) {
	setup := setupTasksAPI(t)
	agent := createTasksTestAgent(t, setup.database, "online")
	now := time.Now()
	history := seedTaskHistory(t, setup.database, agent.ID, "backup", commands.TaskStatusSuccess, "snap-1", now)

	w := postAnyJSON(t, setup.router, "/api/tasks/"+history.ID+"/cancel", nil)

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Empty(t, setup.hub.sent)
}

func TestListTasksFiltersAndLimitsHistory(t *testing.T) {
	setup := setupTasksAPI(t)
	agentA := createTasksTestAgent(t, setup.database, "online")
	agentB := createTasksTestAgent(t, setup.database, "online")
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	seedTaskHistory(t, setup.database, agentA.ID, "backup", "success", "snap-a-old", now.Add(-2*time.Hour))
	expected := seedTaskHistory(t, setup.database, agentA.ID, "backup", "failed", "snap-a-new", now)
	seedTaskHistory(t, setup.database, agentA.ID, "restore", "success", "snap-restore", now.Add(-time.Hour))
	seedTaskHistory(t, setup.database, agentB.ID, "backup", "success", "snap-b", now.Add(time.Hour))

	w := getJSON(t, setup.router, "/api/tasks?agent_id="+agentA.ID+"&type=backup&limit=1")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, true, body["ok"])
	data, ok := body["data"].([]any)
	require.True(t, ok)
	require.Len(t, data, 1)
	task := requireMap(t, data[0])
	assert.Equal(t, agentA.ID, task["agent_id"])
	assert.Equal(t, "backup", task["type"])
	assert.Equal(t, "snap-a-new", task["snapshot_id"])
	assert.Equal(t, expected.CommandID, task["command_id"])
	assert.Equal(t, expected.PolicyID, task["policy_id"])
	assert.Equal(t, expected.StorageID, task["storage_id"])
	assert.NotEmpty(t, task["updated_at"])
}

func TestListTasksExposesRepositorySizeAndErrorAliases(t *testing.T) {
	setup := setupTasksAPI(t)
	agent := createTasksTestAgent(t, setup.database, "online")
	finishedAt := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	history := seedTaskHistory(t, setup.database, agent.ID, "backup", "failed", "snap-failed", finishedAt)
	require.NoError(t, setup.database.DB.Model(&db.TaskHistory{}).
		Where("id = ?", history.ID).
		Updates(map[string]any{
			"repo_size": 4096,
			"error_log": "restic failed",
		}).Error)

	w := getJSON(t, setup.router, "/api/tasks?agent_id="+agent.ID+"&limit=1")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	data := requireList(t, body["data"])
	require.Len(t, data, 1)
	task := requireMap(t, data[0])
	assert.Equal(t, float64(4096), task["repo_size"])
	assert.Equal(t, float64(4096), task["repository_size_bytes"])
	assert.Equal(t, "restic failed", task["error_log"])
	assert.Equal(t, "restic failed", task["error"])
}

func TestListTasksAttachesProgressForRunningTask(t *testing.T) {
	setup := setupTasksAPI(t)
	agent := createTasksTestAgent(t, setup.database, "online")
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	history := seedTaskHistory(t, setup.database, agent.ID, "backup", commands.TaskStatusRunning, "snap-running", now)
	setup.handler.ProgressGetter = func(agentID string, messageID string) *protocol.BackupProgressPayload {
		require.Equal(t, agent.ID, agentID)
		require.Equal(t, history.MessageID, messageID)
		return &protocol.BackupProgressPayload{
			AgentID:     agentID,
			Phase:       "backup",
			PercentDone: 64.5,
			FilesDone:   8,
			TotalFiles:  12,
			BytesDone:   2048,
			TotalBytes:  4096,
			BytesPerSec: 1024,
			CurrentFile: "/srv/current.db",
		}
	}

	w := getJSON(t, setup.router, "/api/tasks?agent_id="+agent.ID+"&limit=1")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	data := requireList(t, body["data"])
	require.Len(t, data, 1)
	task := requireMap(t, data[0])
	progress := requireMap(t, task["progress"])
	assert.Equal(t, agent.ID, progress["agent_id"])
	assert.Equal(t, "backup", progress["phase"])
	assert.Equal(t, 64.5, progress["percent_done"])
	assert.Equal(t, float64(2048), progress["bytes_done"])
	assert.Equal(t, "/srv/current.db", progress["current_file"])
}

func TestListTasksAttachesProgressForPendingTask(t *testing.T) {
	setup := setupTasksAPI(t)
	agent := createTasksTestAgent(t, setup.database, "offline")
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	history := seedTaskHistory(t, setup.database, agent.ID, "backup", commands.TaskStatusPending, "snap-pending", now)
	setup.handler.ProgressGetter = func(agentID string, messageID string) *protocol.BackupProgressPayload {
		require.Equal(t, agent.ID, agentID)
		require.Equal(t, history.MessageID, messageID)
		return &protocol.BackupProgressPayload{
			AgentID:     agentID,
			Phase:       "queued",
			PercentDone: 0,
		}
	}

	w := getJSON(t, setup.router, "/api/tasks?agent_id="+agent.ID+"&limit=1")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	data := requireList(t, body["data"])
	require.Len(t, data, 1)
	task := requireMap(t, data[0])
	progress := requireMap(t, task["progress"])
	assert.Equal(t, "queued", progress["phase"])
}

func TestListTasksOmitsBackupProgressForRestoreTask(t *testing.T) {
	setup := setupTasksAPI(t)
	agent := createTasksTestAgent(t, setup.database, "online")
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	seedTaskHistory(t, setup.database, agent.ID, "restore", commands.TaskStatusRunning, "snap-restore", now)
	progressGetterCalled := false
	setup.handler.ProgressGetter = func(agentID string, messageID string) *protocol.BackupProgressPayload {
		progressGetterCalled = true
		return &protocol.BackupProgressPayload{
			AgentID: agentID,
			Phase:   "backup",
		}
	}

	w := getJSON(t, setup.router, "/api/tasks?agent_id="+agent.ID+"&limit=1")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	data := requireList(t, body["data"])
	require.Len(t, data, 1)
	task := requireMap(t, data[0])
	assert.NotContains(t, task, "progress")
	assert.False(t, progressGetterCalled, "restore tasks should not look up backup progress")
}

func TestListTasksOmitsProgressForCompletedTask(t *testing.T) {
	setup := setupTasksAPI(t)
	agent := createTasksTestAgent(t, setup.database, "online")
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	history := seedTaskHistory(t, setup.database, agent.ID, "backup", commands.TaskStatusSuccess, "snap-success", now)
	setup.handler.ProgressGetter = func(agentID string, messageID string) *protocol.BackupProgressPayload {
		require.Equal(t, agent.ID, agentID)
		require.Equal(t, history.MessageID, messageID)
		return &protocol.BackupProgressPayload{
			AgentID:     agentID,
			Phase:       "backup",
			PercentDone: 100,
		}
	}

	w := getJSON(t, setup.router, "/api/tasks?agent_id="+agent.ID+"&limit=1")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	data := requireList(t, body["data"])
	require.Len(t, data, 1)
	task := requireMap(t, data[0])
	assert.NotContains(t, task, "progress")
}

func TestListTasksUsesMessageIDWhenAttachingBackupProgress(t *testing.T) {
	setup := setupTasksAPI(t)
	agent := createTasksTestAgent(t, setup.database, "online")
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	first := seedTaskHistoryWithMessageID(t, setup.database, agent.ID, "backup", commands.TaskStatusRunning, "snap-first", now.Add(time.Second), "backup-msg-1")
	second := seedTaskHistoryWithMessageID(t, setup.database, agent.ID, "backup", commands.TaskStatusRunning, "snap-second", now, "backup-msg-2")
	calls := map[string]int{}
	setup.handler.ProgressGetter = func(agentID string, messageID string) *protocol.BackupProgressPayload {
		require.Equal(t, agent.ID, agentID)
		calls[messageID]++
		if messageID != first.MessageID {
			return nil
		}
		return &protocol.BackupProgressPayload{
			AgentID:     agentID,
			Phase:       "backup",
			PercentDone: 25,
		}
	}

	w := getJSON(t, setup.router, "/api/tasks?agent_id="+agent.ID+"&limit=2")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	data := requireList(t, body["data"])
	require.Len(t, data, 2)
	for _, item := range data {
		task := requireMap(t, item)
		switch task["message_id"] {
		case first.MessageID:
			progress := requireMap(t, task["progress"])
			assert.Equal(t, "backup", progress["phase"])
		case second.MessageID:
			assert.NotContains(t, task, "progress")
		default:
			t.Fatalf("unexpected task message_id %v", task["message_id"])
		}
	}
	assert.Equal(t, map[string]int{first.MessageID: 1, second.MessageID: 1}, calls)
}

type tasksAPISetup struct {
	database *db.Database
	hub      *fakeCommandHub
	handler  *TaskHandler
	router   *gin.Engine
}

func setupTasksAPI(t *testing.T) tasksAPISetup {
	t.Helper()

	gin.SetMode(gin.TestMode)
	database, err := db.New(t.TempDir())
	require.NoError(t, err)

	hub := &fakeCommandHub{online: map[string]bool{}}
	commandService := commands.NewService(database, hub)
	handler := NewTaskHandler(database, hub)
	handler.Commands = commandService
	router := gin.New()
	RegisterTaskRoutes(router.Group("/api"), handler)

	return tasksAPISetup{database: database, hub: hub, handler: handler, router: router}
}

type sentCommandMessage struct {
	agentID string
	message protocol.Message
}

type fakeCommandHub struct {
	online  map[string]bool
	sendErr error
	sent    []sentCommandMessage
}

func (h *fakeCommandHub) IsOnline(agentID string) bool {
	return h.online[agentID]
}

func (h *fakeCommandHub) Send(agentID string, msg interface{}) error {
	if h.sendErr != nil {
		return h.sendErr
	}
	message, ok := msg.(protocol.Message)
	if !ok {
		return errors.New("message is not protocol.Message")
	}
	h.sent = append(h.sent, sentCommandMessage{agentID: agentID, message: message})
	return nil
}

func createTasksTestAgent(t *testing.T, database *db.Database, status string) db.Agent {
	t.Helper()

	agent := db.Agent{Name: "Task Agent", Status: status}
	require.NoError(t, database.DB.Create(&agent).Error)
	return agent
}

func seedTaskHistory(t *testing.T, database *db.Database, agentID string, taskType string, status string, snapshotID string, createdAt time.Time) db.TaskHistory {
	t.Helper()
	return seedTaskHistoryWithMessageID(t, database, agentID, taskType, status, snapshotID, createdAt, "msg-"+snapshotID)
}

func seedTaskHistoryWithMessageID(t *testing.T, database *db.Database, agentID string, taskType string, status string, snapshotID string, createdAt time.Time, messageID string) db.TaskHistory {
	t.Helper()

	startedAt := createdAt.Add(-time.Minute)
	finishedAt := createdAt
	history := db.TaskHistory{
		AgentID:    agentID,
		Type:       taskType,
		Status:     status,
		SnapshotID: snapshotID,
		MessageID:  messageID,
		CommandID:  "cmd-" + snapshotID,
		PolicyID:   "policy-" + snapshotID,
		StorageID:  "storage-" + snapshotID,
		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
		DurationMs: 60000,
		CreatedAt:  createdAt,
	}
	require.NoError(t, database.DB.Create(&history).Error)
	return history
}

func seedCommandBackedTask(t *testing.T, database *db.Database, agentID string, commandStatus string, taskStatus string, messageID string) db.TaskHistory {
	t.Helper()

	command := db.AgentCommand{
		AgentID:   agentID,
		Type:      protocol.TypeBackupNow,
		Status:    commandStatus,
		MessageID: messageID,
	}
	require.NoError(t, database.DB.Create(&command).Error)
	history := db.TaskHistory{
		AgentID:   agentID,
		Type:      "backup",
		Status:    taskStatus,
		MessageID: messageID,
		CommandID: command.ID,
		CreatedAt: time.Now(),
	}
	require.NoError(t, database.DB.Create(&history).Error)
	return history
}
