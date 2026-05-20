# VaultFleet Core Capability Upgrade Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the A+ core capability upgrade: durable Agent commands, stronger task records, reconnect delivery, storage connection tests, and health/readiness/metrics endpoints.

**Architecture:** Add a focused `internal/master/commands` service as the durable command boundary. Existing API handlers create command records before sending WebSocket messages, WebSocket connection setup dispatches pending commands, and task result/ack processors close the lifecycle. Storage testing and health/metrics stay in separate small services so command durability does not leak into unrelated handlers.

**Tech Stack:** Go 1.26, Gin, GORM, SQLite WAL, Gorilla WebSocket, existing VaultFleet protocol messages, standard-library `os/exec` for rclone checks, Prometheus text format generated without a new dependency.

---

## Scope Check

This plan implements the approved spec [2026-05-20-vaultfleet-core-capability-upgrade-design.md](../specs/2026-05-20-vaultfleet-core-capability-upgrade-design.md). The work touches multiple surfaces, but they form one coherent release because durable commands are the foundation for backup, restore, policy push, snapshot refresh, task records, and metrics. The plan is split into independently committable tasks.

## File Structure

Create:

- `internal/master/commands/service.go`: command statuses, deadlines, encrypted payload persistence, dispatch, completion, timeout scanning, query helpers.
- `internal/master/commands/service_test.go`: focused command service tests.
- `internal/master/api/commands.go`: command query API and response redaction.
- `internal/master/api/commands_test.go`: command API tests.
- `internal/master/storagecheck/service.go`: storage connection test service with fakeable command runner.
- `internal/master/storagecheck/service_test.go`: storage test service tests.
- `internal/master/api/health.go`: `/health`, `/ready`, `/metrics` handlers.
- `internal/master/api/health_test.go`: health/readiness/metrics tests.

Modify:

- `internal/master/db/models.go`: add `AgentCommand`; extend `TaskHistory` with `CommandID`, `PolicyID`, `StorageID`, `UpdatedAt`.
- `internal/master/db/db.go`: include `AgentCommand` in `AutoMigrate`.
- `internal/master/db/db_test.go`: cover new model fields.
- `internal/master/api/router.go`: wire `CommandService`, command routes, storage test routes, health routes.
- `internal/master/api/tasks.go`: create durable `backup_now` commands and associated task records.
- `internal/master/api/tasks_test.go`: update online/offline backup tests and task list expectations.
- `internal/master/api/restore.go`: create durable `restore_req` commands and task records.
- `internal/master/api/restore_test.go`: update restore tests for queued offline behavior.
- `internal/master/api/snapshots.go`: persist `snapshot_list_req` commands and complete them on response/timeout.
- `internal/master/api/snapshots_test.go`: update snapshot refresh tests.
- `internal/master/api/policy_pusher.go`: enqueue durable `policy_push` commands instead of direct ephemeral sends.
- `internal/master/api/router_test.go`: update policy ack/pusher tests.
- `internal/master/ws/handler.go`: dispatch pending commands after Agent connects.
- `internal/master/ws/handler_test.go`: cover connect-time dispatch.
- `cmd/master/main.go`: instantiate command service, storage check service, timeout scanner, policy pusher, task result processor.

Avoid editing frontend files in this plan unless implementation changes API client types in a later, separate frontend task.

Status convention for this implementation:

- `AgentCommand.status = pending`: created but not successfully written to the Agent WebSocket.
- `AgentCommand.status = running`: successfully written to the Agent WebSocket for long-running commands (`backup_now`, `restore_req`).
- `AgentCommand.status = dispatched`: successfully written for short commands that do not have a long-running task row (`policy_push`, `snapshot_list_req`) when the implementation chooses not to keep them in `running`.
- `TaskHistory.status = pending`: queued but not sent.
- `TaskHistory.status = running`: sent to Agent and expected to produce `task_result`.
- Existing Agent result status `success` remains `success`; do not rename task history success to `succeeded`.

---

### Task 1: Database Model And Command Service Foundation

**Files:**
- Create: `internal/master/commands/service.go`
- Create: `internal/master/commands/service_test.go`
- Modify: `internal/master/db/models.go`
- Modify: `internal/master/db/db.go`
- Modify: `internal/master/db/db_test.go`

- [ ] **Step 1: Write failing DB model tests**

Append these tests to `internal/master/db/db_test.go`:

```go
func TestAgentCommandCRUD(t *testing.T) {
	database := setupTestDB(t)
	now := time.Now().UTC()
	completed := now.Add(time.Minute)

	command := AgentCommand{
		AgentID:      "agent-001",
		Type:         "backup_now",
		Status:       "pending",
		MessageID:    "msg-001",
		Payload:      "encrypted-payload",
		Result:       "",
		ErrorMessage: "",
		Attempts:     0,
		DeadlineAt:   &completed,
	}
	require.NoError(t, database.DB.Create(&command).Error)
	assert.NotEmpty(t, command.ID)

	var found AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, "agent-001", found.AgentID)
	assert.Equal(t, "backup_now", found.Type)
	assert.Equal(t, "pending", found.Status)
	assert.Equal(t, "msg-001", found.MessageID)

	require.NoError(t, database.DB.Model(&found).Updates(map[string]any{
		"status":       "succeeded",
		"completed_at": &completed,
		"result":       `{"status":"success"}`,
	}).Error)

	var updated AgentCommand
	require.NoError(t, database.DB.First(&updated, "id = ?", command.ID).Error)
	assert.Equal(t, "succeeded", updated.Status)
	assert.JSONEq(t, `{"status":"success"}`, updated.Result)
	assert.NotNil(t, updated.CompletedAt)
}

func TestTaskHistoryRunFieldsCRUD(t *testing.T) {
	database := setupTestDB(t)
	now := time.Now().UTC()
	history := TaskHistory{
		AgentID:   "agent-001",
		Type:      "backup",
		Status:    "pending",
		CommandID: "command-001",
		PolicyID:  "policy-001",
		StorageID: "storage-001",
		StartedAt: &now,
	}
	require.NoError(t, database.DB.Create(&history).Error)

	var found TaskHistory
	require.NoError(t, database.DB.First(&found, "id = ?", history.ID).Error)
	assert.Equal(t, "command-001", found.CommandID)
	assert.Equal(t, "policy-001", found.PolicyID)
	assert.Equal(t, "storage-001", found.StorageID)
	assert.False(t, found.UpdatedAt.IsZero())
}
```

- [ ] **Step 2: Run DB tests and verify they fail**

Run:

```bash
go test ./internal/master/db -run 'TestAgentCommandCRUD|TestTaskHistoryRunFieldsCRUD' -count=1
```

Expected: FAIL with compile errors for undefined `AgentCommand` and missing `TaskHistory.CommandID`, `TaskHistory.PolicyID`, `TaskHistory.StorageID`, or `TaskHistory.UpdatedAt`.

- [ ] **Step 3: Add DB models**

Modify `internal/master/db/models.go`:

```go
type AgentCommand struct {
	ID           string     `gorm:"type:text;primaryKey" json:"id"`
	AgentID      string     `gorm:"type:text;index;not null" json:"agent_id"`
	Type         string     `gorm:"type:text;index;not null" json:"type"`
	Status       string     `gorm:"type:text;index;not null" json:"status"`
	MessageID    string     `gorm:"type:text;uniqueIndex;not null" json:"message_id"`
	Payload      string     `gorm:"type:text" json:"-"`
	Result       string     `gorm:"type:text" json:"result,omitempty"`
	ErrorMessage string     `gorm:"type:text" json:"error_message,omitempty"`
	Attempts     int        `json:"attempts"`
	PolicyID     string     `gorm:"type:text;index" json:"policy_id,omitempty"`
	StorageID    string     `gorm:"type:text;index" json:"storage_id,omitempty"`
	DeadlineAt   *time.Time `json:"deadline_at"`
	DispatchedAt *time.Time `json:"dispatched_at"`
	CompletedAt  *time.Time `json:"completed_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func (c *AgentCommand) BeforeCreate(tx *gorm.DB) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	return nil
}
```

Extend `TaskHistory` in `internal/master/db/models.go`:

```go
	CommandID string    `gorm:"type:text;index" json:"command_id,omitempty"`
	PolicyID  string    `gorm:"type:text;index" json:"policy_id,omitempty"`
	StorageID string    `gorm:"type:text;index" json:"storage_id,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
```

Place these fields after `MessageID` to keep command-related fields grouped.

Modify `internal/master/db/db.go` so `AutoMigrate` includes `&AgentCommand{}` before `&TaskHistory{}`:

```go
		&BackupPolicy{},
		&AgentCommand{},
		&TaskHistory{},
```

- [ ] **Step 4: Run DB tests and verify they pass**

Run:

```bash
go test ./internal/master/db -run 'TestAgentCommandCRUD|TestTaskHistoryRunFieldsCRUD' -count=1
```

Expected: PASS.

- [ ] **Step 5: Write failing command service tests**

Create `internal/master/commands/service_test.go`:

```go
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
	assert.Contains(t, found.ErrorMessage, "write failed")
}

func setupCommandTestDB(t *testing.T) *db.Database {
	t.Helper()
	database, err := db.New(t.TempDir())
	require.NoError(t, err)
	return database
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

func payloadJSON(t *testing.T, msg protocol.Message) map[string]any {
	t.Helper()
	var result map[string]any
	require.NoError(t, json.Unmarshal(msg.Payload, &result))
	return result
}
```

- [ ] **Step 6: Run command service tests and verify they fail**

Run:

```bash
go test ./internal/master/commands -count=1
```

Expected: FAIL with undefined package symbols such as `NewService`, `CreateCommandInput`, and command status constants.

- [ ] **Step 7: Implement command service foundation**

Create `internal/master/commands/service.go` with these exported names and behavior:

```go
package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"vaultfleet/internal/master/db"
	"vaultfleet/pkg/protocol"
)

const (
	CommandStatusPending    = "pending"
	CommandStatusDispatched = "dispatched"
	CommandStatusRunning    = "running"
	CommandStatusSucceeded  = "succeeded"
	CommandStatusFailed     = "failed"
	CommandStatusTimeout    = "timeout"

	TaskStatusPending = "pending"
	TaskStatusRunning = "running"
	TaskStatusSuccess = "success"
	TaskStatusFailed  = "failed"
	TaskStatusTimeout = "timeout"
)

type Hub interface {
	IsOnline(agentID string) bool
	Send(agentID string, msg interface{}) error
}

type Service struct {
	DB  *db.Database
	Hub Hub
	Now func() time.Time
}

type CreateCommandInput struct {
	AgentID    string
	Type       string
	Message    protocol.Message
	TaskType   string
	TaskState  string
	SnapshotID string
	PolicyID   string
	StorageID  string
}

func NewService(database *db.Database, hub Hub) *Service {
	return &Service{DB: database, Hub: hub, Now: time.Now}
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func DeadlineForType(commandType string, now time.Time) time.Time {
	switch commandType {
	case protocol.TypePolicyPush:
		return now.Add(5 * time.Minute)
	case protocol.TypeSnapshotListReq:
		return now.Add(2 * time.Minute)
	case protocol.TypeBackupNow, protocol.TypeRestoreReq:
		return now.Add(6 * time.Hour)
	default:
		return now.Add(30 * time.Minute)
	}
}

func (s *Service) CreateCommand(ctx context.Context, input CreateCommandInput) (db.AgentCommand, error) {
	if s == nil || s.DB == nil || s.DB.DB == nil {
		return db.AgentCommand{}, errors.New("command service database not configured")
	}
	if input.AgentID == "" || input.Type == "" || input.Message.ID == "" {
		return db.AgentCommand{}, errors.New("agent id, command type, and message id are required")
	}
	raw, err := json.Marshal(input.Message)
	if err != nil {
		return db.AgentCommand{}, fmt.Errorf("marshal command payload: %w", err)
	}
	encrypted, err := db.Encrypt(string(raw), s.DB.MasterKey)
	if err != nil {
		return db.AgentCommand{}, fmt.Errorf("encrypt command payload: %w", err)
	}
	deadline := DeadlineForType(input.Type, s.now())
	command := db.AgentCommand{
		AgentID:    input.AgentID,
		Type:       input.Type,
		Status:     CommandStatusPending,
		MessageID:  input.Message.ID,
		Payload:    encrypted,
		PolicyID:   input.PolicyID,
		StorageID:  input.StorageID,
		DeadlineAt: &deadline,
	}
	err = s.DB.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&command).Error; err != nil {
			return err
		}
		if input.TaskType == "" {
			return nil
		}
		state := input.TaskState
		if state == "" {
			state = TaskStatusPending
		}
		history := db.TaskHistory{
			AgentID:    input.AgentID,
			Type:       input.TaskType,
			Status:     state,
			SnapshotID: input.SnapshotID,
			MessageID:  input.Message.ID,
			CommandID:  command.ID,
			PolicyID:   input.PolicyID,
			StorageID:  input.StorageID,
		}
		return tx.Create(&history).Error
	})
	return command, err
}
```

Add these methods in the same file:

```go
func (s *Service) DispatchPendingForAgent(ctx context.Context, agentID string, limit int) error {
	if s == nil || s.DB == nil || s.DB.DB == nil || s.Hub == nil {
		return nil
	}
	if agentID == "" || !s.Hub.IsOnline(agentID) {
		return nil
	}
	if limit <= 0 {
		limit = 20
	}
	now := s.now()
	var items []db.AgentCommand
	if err := s.DB.DB.WithContext(ctx).
		Where("agent_id = ? AND status IN ? AND (deadline_at IS NULL OR deadline_at > ?)", agentID, []string{CommandStatusPending, CommandStatusDispatched}, now).
		Order("created_at ASC").
		Limit(limit).
		Find(&items).Error; err != nil {
		return err
	}
	for _, command := range items {
		if err := s.dispatch(ctx, command); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) dispatch(ctx context.Context, command db.AgentCommand) error {
	message, err := s.messageFromCommand(command)
	if err != nil {
		return s.recordDispatchFailure(ctx, command.ID, err)
	}
	now := s.now()
	attempts := command.Attempts + 1
	if err := s.Hub.Send(command.AgentID, message); err != nil {
		return s.DB.DB.WithContext(ctx).Model(&db.AgentCommand{}).
			Where("id = ?", command.ID).
			Updates(map[string]any{
				"attempts":      attempts,
				"error_message": err.Error(),
				"updated_at":    now,
			}).Error
	}
	nextStatus := CommandStatusDispatched
	if command.Type == protocol.TypeBackupNow || command.Type == protocol.TypeRestoreReq {
		nextStatus = CommandStatusRunning
	}
	return s.DB.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&db.AgentCommand{}).Where("id = ?", command.ID).Updates(map[string]any{
			"status":        nextStatus,
			"attempts":      attempts,
			"dispatched_at": &now,
			"error_message": "",
		}).Error; err != nil {
			return err
		}
		if nextStatus == CommandStatusRunning {
			return tx.Model(&db.TaskHistory{}).
				Where("command_id = ? AND status = ?", command.ID, TaskStatusPending).
				Update("status", TaskStatusRunning).Error
		}
		return nil
	})
}

func (s *Service) messageFromCommand(command db.AgentCommand) (protocol.Message, error) {
	plaintext, err := db.Decrypt(command.Payload, s.DB.MasterKey)
	if err != nil {
		return protocol.Message{}, err
	}
	var message protocol.Message
	if err := json.Unmarshal([]byte(plaintext), &message); err != nil {
		return protocol.Message{}, err
	}
	return message, nil
}

func (s *Service) recordDispatchFailure(ctx context.Context, commandID string, err error) error {
	return s.DB.DB.WithContext(ctx).Model(&db.AgentCommand{}).
		Where("id = ?", commandID).
		Update("error_message", err.Error()).Error
}
```

- [ ] **Step 8: Run service and DB tests**

Run:

```bash
go test ./internal/master/db ./internal/master/commands -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit Task 1**

```bash
git add internal/master/db/models.go internal/master/db/db.go internal/master/db/db_test.go internal/master/commands/service.go internal/master/commands/service_test.go
git commit -m "feat: add durable agent command model"
```

---

### Task 2: Backup, Restore, And Command Query APIs

**Files:**
- Create: `internal/master/api/commands.go`
- Create: `internal/master/api/commands_test.go`
- Modify: `internal/master/api/router.go`
- Modify: `internal/master/api/tasks.go`
- Modify: `internal/master/api/tasks_test.go`
- Modify: `internal/master/api/restore.go`
- Modify: `internal/master/api/restore_test.go`

- [ ] **Step 1: Write failing backup API tests**

Update `internal/master/api/tasks_test.go`.

Change `TestBackupNowRejectsOfflineAgent` into:

```go
func TestBackupNowQueuesCommandForOfflineAgent(t *testing.T) {
	setup := setupTasksAPI(t)
	agent := createTasksTestAgent(t, setup.database, "offline")

	w := postAnyJSON(t, setup.router, "/api/agents/"+agent.ID+"/backup-now", map[string]any{})

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, true, body["ok"])
	data := requireMap(t, body["data"])
	assert.NotEmpty(t, data["command_id"])
	assert.NotEmpty(t, data["message_id"])
	require.Empty(t, setup.hub.sent)

	var command db.AgentCommand
	require.NoError(t, setup.database.DB.First(&command, "id = ?", data["command_id"]).Error)
	assert.Equal(t, "backup_now", command.Type)
	assert.Equal(t, "pending", command.Status)

	var history db.TaskHistory
	require.NoError(t, setup.database.DB.First(&history, "command_id = ?", command.ID).Error)
	assert.Equal(t, "backup", history.Type)
	assert.Equal(t, "pending", history.Status)
}
```

In `TestBackupNowSendsAgentCommand`, add assertions:

```go
assert.NotEmpty(t, data["command_id"])
var command db.AgentCommand
require.NoError(t, setup.database.DB.First(&command, "id = ?", data["command_id"]).Error)
assert.Equal(t, "running", command.Status)
assert.Equal(t, data["message_id"], command.MessageID)

var history db.TaskHistory
require.NoError(t, setup.database.DB.First(&history, "command_id = ?", command.ID).Error)
assert.Equal(t, "backup", history.Type)
assert.Equal(t, "running", history.Status)
```

Update `setupTasksAPI` to create and pass the command service:

```go
commandService := commands.NewService(database, hub)
handler := NewTaskHandler(database, hub)
handler.Commands = commandService
```

Add import:

```go
"vaultfleet/internal/master/commands"
```

- [ ] **Step 2: Run backup API tests and verify they fail**

Run:

```bash
go test ./internal/master/api -run 'TestBackupNow' -count=1
```

Expected: FAIL because `TaskHandler` has no `Commands` field and offline backup still returns `502`.

- [ ] **Step 3: Implement backup command creation**

Modify `internal/master/api/tasks.go`:

```go
import "vaultfleet/internal/master/commands"
```

Add to `TaskHandler`:

```go
	Commands *commands.Service
```

In `BackupNow`, replace the offline rejection and direct send with:

```go
	if h.Commands == nil {
		writeErrorResponse(c, http.StatusInternalServerError, "command service not configured")
		return
	}
	msg, err := protocol.NewMessage(protocol.TypeBackupNow, protocol.BackupNowPayload{AgentID: agentID})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "encode backup request"})
		return
	}
	command, err := h.Commands.CreateCommand(c.Request.Context(), commands.CreateCommandInput{
		AgentID:   agentID,
		Type:      protocol.TypeBackupNow,
		Message:   *msg,
		TaskType:  "backup",
		TaskState: commands.TaskStatusPending,
	})
	if err != nil {
		writeErrorResponse(c, http.StatusInternalServerError, "database error")
		return
	}
	if h.Hub != nil && h.Hub.IsOnline(agentID) {
		if err := h.Commands.DispatchPendingForAgent(c.Request.Context(), agentID, 10); err != nil {
			writeErrorResponse(c, http.StatusInternalServerError, "dispatch command")
			return
		}
	}
	c.JSON(http.StatusAccepted, gin.H{
		"ok": true,
		"data": gin.H{
			"command_id": command.ID,
			"message_id": msg.ID,
		},
	})
```

Update `taskResponse` and `newTaskResponse` to include:

```go
	CommandID string `json:"command_id,omitempty"`
	PolicyID  string `json:"policy_id,omitempty"`
	StorageID string `json:"storage_id,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
```

- [ ] **Step 4: Run backup API tests**

Run:

```bash
go test ./internal/master/api -run 'TestBackupNow|TestListTasksFiltersAndLimitsHistory' -count=1
```

Expected: PASS.

- [ ] **Step 5: Write failing restore API tests**

Update `internal/master/api/restore_test.go`.

Change `TestRestoreOffline` into:

```go
func TestRestoreOfflineQueuesCommand(t *testing.T) {
	setup := setupRestoreAPI(t)
	agent := createRestoreTestAgent(t, setup.database, "offline")

	w := postAnyJSON(t, setup.router, "/api/agents/"+agent.ID+"/restore", map[string]any{
		"snapshot_id": "snap-1",
		"target_path": "/restore",
	})

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, true, body["ok"])
	data := requireMap(t, body["data"])
	assert.Equal(t, "restore queued", data["message"])
	assert.NotEmpty(t, data["command_id"])
	assert.NotEmpty(t, data["message_id"])
	require.Empty(t, setup.hub.sent)

	var command db.AgentCommand
	require.NoError(t, setup.database.DB.First(&command, "id = ?", data["command_id"]).Error)
	assert.Equal(t, "restore_req", command.Type)
	assert.Equal(t, "pending", command.Status)

	var history db.TaskHistory
	require.NoError(t, setup.database.DB.First(&history, "command_id = ?", command.ID).Error)
	assert.Equal(t, "restore", history.Type)
	assert.Equal(t, "pending", history.Status)
	assert.Equal(t, "snap-1", history.SnapshotID)
}
```

Update `setupRestoreAPI`:

```go
commandService := commands.NewService(database, hub)
handler := NewRestoreHandler(database, hub)
handler.Commands = commandService
```

Add import:

```go
"vaultfleet/internal/master/commands"
```

- [ ] **Step 6: Run restore tests and verify they fail**

Run:

```bash
go test ./internal/master/api -run 'TestRestore' -count=1
```

Expected: FAIL because `RestoreHandler` has no `Commands` field and offline restore still returns `502`.

- [ ] **Step 7: Implement restore command creation**

Modify `internal/master/api/restore.go`:

```go
import "vaultfleet/internal/master/commands"
```

Add to `RestoreHandler`:

```go
	Commands *commands.Service
```

Replace the offline rejection, manual `TaskHistory` creation, direct send, and send-failure update with the same command-first flow:

```go
	if h.Commands == nil {
		writeErrorResponse(c, http.StatusInternalServerError, "command service not configured")
		return
	}
	msg, err := protocol.NewMessage(protocol.TypeRestoreReq, protocol.RestoreReqPayload{
		SnapshotID: request.SnapshotID,
		Target:     targetPath,
	})
	if err != nil {
		writeErrorResponse(c, http.StatusInternalServerError, "encode restore request")
		return
	}
	command, err := h.Commands.CreateCommand(c.Request.Context(), commands.CreateCommandInput{
		AgentID:    agentID,
		Type:       protocol.TypeRestoreReq,
		Message:    *msg,
		TaskType:   "restore",
		TaskState:  commands.TaskStatusPending,
		SnapshotID: request.SnapshotID,
	})
	if err != nil {
		writeErrorResponse(c, http.StatusInternalServerError, "database error")
		return
	}
	if h.Hub != nil && h.Hub.IsOnline(agentID) {
		if err := h.Commands.DispatchPendingForAgent(c.Request.Context(), agentID, 10); err != nil {
			writeErrorResponse(c, http.StatusInternalServerError, "dispatch command")
			return
		}
	}
	message := "restore queued"
	if h.Hub != nil && h.Hub.IsOnline(agentID) {
		message = "restore started"
	}
	writeDataResponse(c, http.StatusAccepted, gin.H{
		"message":    message,
		"command_id": command.ID,
		"message_id": msg.ID,
	})
```

- [ ] **Step 8: Add command query API tests**

Create `internal/master/api/commands_test.go`:

```go
package api

import (
	"context"
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

func TestGetCommandRedactsPayload(t *testing.T) {
	setup := setupCommandsAPI(t)
	command := seedAPICommand(t, setup.service, "agent-1", protocol.TypeBackupNow)

	w := getJSON(t, setup.router, "/api/commands/"+command.ID)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	data := requireMap(t, body["data"])
	assert.Equal(t, command.ID, data["id"])
	assert.Equal(t, "agent-1", data["agent_id"])
	assert.Equal(t, "backup_now", data["type"])
	assert.NotContains(t, data, "payload")
}

func TestListAgentCommandsFiltersStatusAndLimit(t *testing.T) {
	setup := setupCommandsAPI(t)
	require.NoError(t, setup.database.DB.Create(&db.Agent{ID: "agent-1", Name: "Agent 1", Status: "online"}).Error)
	first := seedAPICommand(t, setup.service, "agent-1", protocol.TypeBackupNow)
	second := seedAPICommand(t, setup.service, "agent-1", protocol.TypeRestoreReq)
	require.NoError(t, setup.database.DB.Model(&db.AgentCommand{}).Where("id = ?", first.ID).Update("status", "succeeded").Error)
	require.NoError(t, setup.database.DB.Model(&db.AgentCommand{}).Where("id = ?", second.ID).Update("status", "pending").Error)

	w := getJSON(t, setup.router, "/api/agents/agent-1/commands?status=pending&limit=1")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	items := requireList(t, body["data"])
	require.Len(t, items, 1)
	item := requireMap(t, items[0])
	assert.Equal(t, second.ID, item["id"])
	assert.Equal(t, "pending", item["status"])
}

type commandsAPISetup struct {
	database *db.Database
	service  *commands.Service
	router   *gin.Engine
}

func setupCommandsAPI(t *testing.T) commandsAPISetup {
	t.Helper()
	gin.SetMode(gin.TestMode)
	database, err := db.New(t.TempDir())
	require.NoError(t, err)
	service := commands.NewService(database, nil)
	router := gin.New()
	RegisterCommandRoutes(router.Group("/api"), NewCommandHandler(database))
	return commandsAPISetup{database: database, service: service, router: router}
}

func seedAPICommand(t *testing.T, service *commands.Service, agentID string, msgType string) db.AgentCommand {
	t.Helper()
	payload := any(protocol.BackupNowPayload{AgentID: agentID})
	taskType := "backup"
	if msgType == protocol.TypeRestoreReq {
		payload = protocol.RestoreReqPayload{SnapshotID: "snap-1", Target: "/restore"}
		taskType = "restore"
	}
	msg, err := protocol.NewMessage(msgType, payload)
	require.NoError(t, err)
	command, err := service.CreateCommand(context.Background(), commands.CreateCommandInput{
		AgentID:   agentID,
		Type:      msgType,
		Message:   *msg,
		TaskType:  taskType,
		TaskState: commands.TaskStatusPending,
	})
	require.NoError(t, err)
	require.NoError(t, service.DB.DB.Model(&db.AgentCommand{}).Where("id = ?", command.ID).Update("created_at", time.Now()).Error)
	return command
}
```

- [ ] **Step 9: Implement command query API**

Create `internal/master/api/commands.go`:

```go
package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"vaultfleet/internal/master/db"
)

const defaultCommandListLimit = 50
const maxCommandListLimit = 200

type CommandHandler struct {
	DB *db.Database
}

type commandResponse struct {
	ID           string     `json:"id"`
	AgentID      string     `json:"agent_id"`
	Type         string     `json:"type"`
	Status       string     `json:"status"`
	MessageID    string     `json:"message_id"`
	Result       string     `json:"result,omitempty"`
	ErrorMessage string     `json:"error_message,omitempty"`
	Attempts     int        `json:"attempts"`
	PolicyID     string     `json:"policy_id,omitempty"`
	StorageID    string     `json:"storage_id,omitempty"`
	DeadlineAt   *time.Time `json:"deadline_at"`
	DispatchedAt *time.Time `json:"dispatched_at"`
	CompletedAt  *time.Time `json:"completed_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func NewCommandHandler(database *db.Database) *CommandHandler {
	return &CommandHandler{DB: database}
}

func RegisterCommandRoutes(rg *gin.RouterGroup, h *CommandHandler) {
	rg.GET("/commands/:id", h.Get)
	rg.GET("/agents/:id/commands", h.ListForAgent)
}

func (h *CommandHandler) Get(c *gin.Context) {
	var command db.AgentCommand
	err := h.DB.DB.First(&command, "id = ?", c.Param("id")).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeErrorResponse(c, http.StatusNotFound, "command not found")
		return
	}
	if err != nil {
		writeErrorResponse(c, http.StatusInternalServerError, "database error")
		return
	}
	writeDataResponse(c, http.StatusOK, newCommandResponse(command))
}

func (h *CommandHandler) ListForAgent(c *gin.Context) {
	agentID := c.Param("id")
	if !agentExistsByID(c, h.DB, agentID) {
		return
	}
	query := h.DB.DB.Where("agent_id = ?", agentID).Order("created_at DESC").Limit(parseCommandLimit(c.Query("limit")))
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	if commandType := c.Query("type"); commandType != "" {
		query = query.Where("type = ?", commandType)
	}
	var commands []db.AgentCommand
	if err := query.Find(&commands).Error; err != nil {
		writeErrorResponse(c, http.StatusInternalServerError, "database error")
		return
	}
	responses := make([]commandResponse, 0, len(commands))
	for _, command := range commands {
		responses = append(responses, newCommandResponse(command))
	}
	writeDataResponse(c, http.StatusOK, responses)
}

func parseCommandLimit(raw string) int {
	if raw == "" {
		return defaultCommandListLimit
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return defaultCommandListLimit
	}
	if limit > maxCommandListLimit {
		return maxCommandListLimit
	}
	return limit
}

func newCommandResponse(command db.AgentCommand) commandResponse {
	return commandResponse{
		ID:           command.ID,
		AgentID:      command.AgentID,
		Type:         command.Type,
		Status:       command.Status,
		MessageID:    command.MessageID,
		Result:       command.Result,
		ErrorMessage: command.ErrorMessage,
		Attempts:     command.Attempts,
		PolicyID:     command.PolicyID,
		StorageID:    command.StorageID,
		DeadlineAt:   command.DeadlineAt,
		DispatchedAt: command.DispatchedAt,
		CompletedAt:  command.CompletedAt,
		CreatedAt:    command.CreatedAt,
		UpdatedAt:    command.UpdatedAt,
	}
}
```

Modify `internal/master/api/router.go`:

```go
import "vaultfleet/internal/master/commands"
```

Add to `RouterConfig`:

```go
	CommandService *commands.Service
```

In `NewRouter`, create or reuse service:

```go
	commandService := cfg.CommandService
	if commandService == nil {
		commandService = commands.NewService(cfg.Database, cfg.Hub)
	}
```

Then set:

```go
	taskHandler.Commands = commandService
	restoreHandler.Commands = commandService
	commandHandler := NewCommandHandler(cfg.Database)
```

Register:

```go
	RegisterCommandRoutes(protected, commandHandler)
```

- [ ] **Step 10: Run API tests**

Run:

```bash
go test ./internal/master/api -run 'TestBackupNow|TestRestore|TestGetCommand|TestListAgentCommands|TestListTasks' -count=1
```

Expected: PASS.

- [ ] **Step 11: Commit Task 2**

```bash
git add internal/master/api/commands.go internal/master/api/commands_test.go internal/master/api/router.go internal/master/api/tasks.go internal/master/api/tasks_test.go internal/master/api/restore.go internal/master/api/restore_test.go
git commit -m "feat: queue backup and restore commands"
```

---

### Task 3: Agent Reconnect Delivery And Durable Policy Push

**Files:**
- Modify: `internal/master/commands/service.go`
- Modify: `internal/master/commands/service_test.go`
- Modify: `internal/master/api/policy_pusher.go`
- Modify: `internal/master/api/router.go`
- Modify: `internal/master/api/router_test.go`
- Modify: `internal/master/ws/handler.go`
- Modify: `internal/master/ws/handler_test.go`

- [ ] **Step 1: Write failing reconnect dispatch test**

Add to `internal/master/ws/handler_test.go`:

```go
func TestHandlerDispatchesPendingCommandsOnConnect(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)
	dispatched := make(chan string, 1)
	handler := NewHandler(setup.hub, setup.bus, validTestAuth, noPolicy, nil)
	handler.PendingCommandDispatcher = func(agentID string) error {
		dispatched <- agentID
		return nil
	}
	setup.router.GET("/ws-command-dispatch", handler.HandleWebSocket)

	conn, _, err := websocket.DefaultDialer.Dial(
		websocketURL(setup.server.URL, "/ws-command-dispatch", url.Values{"token": []string{"valid"}}),
		nil,
	)
	require.NoError(t, err)
	defer conn.Close()

	select {
	case agentID := <-dispatched:
		assert.Equal(t, "agent-1", agentID)
	case <-time.After(time.Second):
		t.Fatal("pending command dispatcher was not called")
	}
}
```

- [ ] **Step 2: Run reconnect test and verify it fails**

Run:

```bash
go test ./internal/master/ws -run TestHandlerDispatchesPendingCommandsOnConnect -count=1
```

Expected: FAIL because `PendingCommandDispatcher` does not exist.

- [ ] **Step 3: Add connect-time dispatcher hook**

Modify `internal/master/ws/handler.go`:

```go
type PendingCommandDispatcherFunc func(agentID string) error
```

Add to `Handler`:

```go
	PendingCommandDispatcher PendingCommandDispatcherFunc
```

In `HandleWebSocket`, after optional policy lookup block and before `h.readLoop(agentID, safeConn)`:

```go
	if h.PendingCommandDispatcher != nil {
		if err := h.PendingCommandDispatcher(agentID); err != nil {
			log.Printf("dispatch pending commands for agent %s failed: %v", agentID, err)
		}
	}
```

Run:

```bash
go test ./internal/master/ws -run TestHandlerDispatchesPendingCommandsOnConnect -count=1
```

Expected: PASS.

- [ ] **Step 4: Write failing policy command service tests**

Add to `internal/master/commands/service_test.go`:

```go
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
}
```

- [ ] **Step 5: Implement policy ack command completion**

Add to `internal/master/commands/service.go`:

```go
func (s *Service) CompletePolicyAck(ctx context.Context, agentID string, messageID string, success bool, errorText string) error {
	if s == nil || s.DB == nil || s.DB.DB == nil || messageID == "" {
		return nil
	}
	now := s.now()
	status := CommandStatusSucceeded
	if !success {
		status = CommandStatusFailed
	}
	updates := map[string]any{
		"status":       status,
		"completed_at": &now,
		"updated_at":   now,
	}
	if errorText != "" {
		updates["error_message"] = errorText
	}
	return s.DB.DB.WithContext(ctx).
		Model(&db.AgentCommand{}).
		Where("agent_id = ? AND message_id = ? AND type = ?", agentID, messageID, protocol.TypePolicyPush).
		Updates(updates).Error
}
```

Run:

```bash
go test ./internal/master/commands -run 'TestCompletePolicyAck' -count=1
```

Expected: PASS.

- [ ] **Step 6: Write failing policy pusher tests**

Update `internal/master/api/router_test.go` policy pusher tests so they verify a command is created before sending. In `TestPolicyChangedPusherSendsCurrentPolicyToOnlineAgent`, after `pusher.Handle`, add:

```go
var command db.AgentCommand
require.NoError(t, database.DB.First(&command, "agent_id = ? AND type = ?", agent.ID, protocol.TypePolicyPush).Error)
assert.Equal(t, "running", command.Status)
assert.Equal(t, policy.ID, command.PolicyID)
assert.Equal(t, storage.ID, command.StorageID)
assert.Equal(t, hub.sent[0].message.ID, command.MessageID)
```

Change pusher setup to:

```go
commandService := commands.NewService(database, hub)
pusher := NewPolicyChangedPusher(database, hub, CurrentPolicyLookupWithTracker(database, tracker))
pusher.Commands = commandService
```

Add import:

```go
"vaultfleet/internal/master/commands"
```

- [ ] **Step 7: Implement durable policy push in pusher**

Modify `internal/master/api/policy_pusher.go`:

```go
import "vaultfleet/internal/master/commands"
```

Add:

```go
	Commands *commands.Service
```

In `Handle`, replace direct `p.Hub.Send(agentID, *msg)` with:

```go
	if p.Commands == nil {
		if err := p.Hub.Send(agentID, *msg); err != nil {
			log.Printf("push policy to agent %s failed: %v", agentID, err)
		}
		return
	}
	policyID, storageID := p.currentPolicyRefs(agentID)
	if _, err := p.Commands.CreateCommand(context.Background(), commands.CreateCommandInput{
		AgentID:   agentID,
		Type:      protocol.TypePolicyPush,
		Message:   *msg,
		PolicyID:  policyID,
		StorageID: storageID,
	}); err != nil {
		log.Printf("create policy command for agent %s failed: %v", agentID, err)
		return
	}
	if err := p.Commands.DispatchPendingForAgent(context.Background(), agentID, 10); err != nil {
		log.Printf("dispatch policy command for agent %s failed: %v", agentID, err)
	}
```

Add helper:

```go
func (p *PolicyChangedPusher) currentPolicyRefs(agentID string) (string, string) {
	if p == nil || p.DB == nil || p.DB.DB == nil {
		return "", ""
	}
	var policy db.BackupPolicy
	if err := p.DB.DB.Where("agent_id = ? AND synced = ?", agentID, false).Order("updated_at DESC").First(&policy).Error; err != nil {
		return "", ""
	}
	return policy.ID, policy.StorageID
}
```

Add `context` import.

- [ ] **Step 8: Update policy ack processor to mark commands**

Modify `internal/master/api/router.go` where the current policy ack processor constructors are defined:

Add interface:

```go
type PolicyCommandCompleter interface {
	CompletePolicyAck(ctx context.Context, agentID string, messageID string, success bool, errorText string) error
}
```

Change constructor:

```go
func NewPolicyAckProcessor(database *db.Database, completer ...PolicyCommandCompleter) func(agentID string, msg protocol.Message) error {
	return NewPolicyAckProcessorWithTracker(database, defaultPolicyPushTracker, completer...)
}
```

Change tracker constructor signature:

```go
func NewPolicyAckProcessorWithTracker(database *db.Database, tracker *PolicyPushTracker, completer ...PolicyCommandCompleter) func(agentID string, msg protocol.Message) error
```

Inside returned func, after parsing `ack`:

```go
	if len(completer) > 0 && completer[0] != nil {
		if err := completer[0].CompletePolicyAck(context.Background(), agentID, msg.ID, ack.Success, ack.Error); err != nil {
			return err
		}
	}
```

Keep existing tracker policy synced logic after this block.

- [ ] **Step 9: Run policy and ws tests**

Run:

```bash
go test ./internal/master/ws -run TestHandlerDispatchesPendingCommandsOnConnect -count=1
go test ./internal/master/api -run 'TestPolicyChangedPusher|TestPolicyAckProcessor' -count=1
```

Expected: PASS.

- [ ] **Step 10: Commit Task 3**

```bash
git add internal/master/commands/service.go internal/master/commands/service_test.go internal/master/api/policy_pusher.go internal/master/api/router.go internal/master/api/router_test.go internal/master/ws/handler.go internal/master/ws/handler_test.go
git commit -m "feat: dispatch queued commands on agent connect"
```

---

### Task 4: Task Results, Timeout Scanner, And Snapshot Refresh Commands

**Files:**
- Modify: `internal/master/commands/service.go`
- Modify: `internal/master/commands/service_test.go`
- Modify: `internal/master/api/snapshots.go`
- Modify: `internal/master/api/snapshots_test.go`
- Modify: `internal/master/ws/handler_test.go`

- [ ] **Step 1: Write failing task result completion tests**

Add to `internal/master/commands/service_test.go`:

```go
func TestCompleteTaskResultUpdatesCommandAndTaskHistory(t *testing.T) {
	database := setupCommandTestDB(t)
	service := NewService(database, nil)
	started := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	finished := started.Add(2 * time.Minute)
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

	result := protocol.TaskResultPayload{
		AgentID:    "agent-1",
		TaskType:   "backup",
		Status:     "success",
		SnapshotID: "snap-1",
		DurationMs: 120000,
		RepoSize:   2048,
		StartedAt:  started,
		FinishedAt: finished,
	}
	require.NoError(t, service.CompleteTaskResult(context.Background(), "agent-1", msg.ID, result))

	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusSucceeded, found.Status)
	assert.NotNil(t, found.CompletedAt)
	assert.Contains(t, found.Result, `"snapshot_id":"snap-1"`)

	var history db.TaskHistory
	require.NoError(t, database.DB.First(&history, "command_id = ?", command.ID).Error)
	assert.Equal(t, "success", history.Status)
	assert.Equal(t, "snap-1", history.SnapshotID)
	assert.Equal(t, int64(120000), history.DurationMs)
}

func TestCompleteTaskResultMarksCommandFailed(t *testing.T) {
	database := setupCommandTestDB(t)
	service := NewService(database, nil)
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

	result := protocol.TaskResultPayload{AgentID: "agent-1", TaskType: "backup", Status: "failed", ErrorLog: "restic failed"}
	require.NoError(t, service.CompleteTaskResult(context.Background(), "agent-1", msg.ID, result))

	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusFailed, found.Status)
	assert.Equal(t, "restic failed", found.ErrorMessage)
}
```

- [ ] **Step 2: Implement task result completion**

Add to `internal/master/commands/service.go`:

```go
func (s *Service) CompleteTaskResult(ctx context.Context, agentID string, messageID string, result protocol.TaskResultPayload) error {
	if s == nil || s.DB == nil || s.DB.DB == nil || messageID == "" {
		return nil
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	now := s.now()
	commandStatus := CommandStatusSucceeded
	if result.Status != TaskStatusSuccess {
		commandStatus = CommandStatusFailed
	}
	updates := map[string]any{
		"status":       commandStatus,
		"result":       string(raw),
		"completed_at": &now,
		"updated_at":   now,
	}
	if result.ErrorLog != "" {
		updates["error_message"] = result.ErrorLog
	}
	return s.DB.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&db.AgentCommand{}).
			Where("agent_id = ? AND message_id = ?", agentID, messageID).
			Updates(updates).Error; err != nil {
			return err
		}
		taskUpdates := map[string]any{
			"status":      result.Status,
			"snapshot_id": result.SnapshotID,
			"duration_ms": result.DurationMs,
			"repo_size":   result.RepoSize,
			"error_log":   result.ErrorLog,
		}
		if !result.StartedAt.IsZero() {
			startedAt := result.StartedAt
			taskUpdates["started_at"] = &startedAt
		}
		if !result.FinishedAt.IsZero() {
			finishedAt := result.FinishedAt
			taskUpdates["finished_at"] = &finishedAt
		} else if result.Status != TaskStatusRunning && result.Status != TaskStatusPending {
			taskUpdates["finished_at"] = &now
		}
		return tx.Model(&db.TaskHistory{}).
			Where("agent_id = ? AND message_id = ?", agentID, messageID).
			Updates(taskUpdates).Error
	})
}
```

- [ ] **Step 3: Update task result processor**

Modify `internal/master/api/snapshots.go`:

```go
type TaskResultCommandCompleter interface {
	CompleteTaskResult(ctx context.Context, agentID string, messageID string, result protocol.TaskResultPayload) error
}

func NewTaskResultProcessor(database *db.Database, completer ...TaskResultCommandCompleter) func(agentID string, msg protocol.Message) error {
	return func(agentID string, msg protocol.Message) error {
		result, err := protocol.ParsePayload[protocol.TaskResultPayload](&msg)
		if err != nil {
			return err
		}
		if len(completer) > 0 && completer[0] != nil {
			if err := completer[0].CompleteTaskResult(context.Background(), agentID, msg.ID, *result); err != nil {
				return err
			}
		}
		return recordTaskResult(database, agentID, msg.ID, *result)
	}
}
```

Add `context` import.

Update `recordTaskResult` so it does not create a duplicate row when a command-linked task already exists:

```go
var existing db.TaskHistory
err := database.DB.First(&existing, "agent_id = ? AND message_id = ?", agentID, messageID).Error
if err == nil && existing.CommandID != "" {
	return nil
}
if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
	return err
}
```

Place this check after `agentID` normalization and before restore/backup branching.

- [ ] **Step 4: Write failing timeout tests**

Add to `internal/master/commands/service_test.go`:

```go
func TestTimeoutExpiredCommandsMarksCommandAndTask(t *testing.T) {
	database := setupCommandTestDB(t)
	service := NewService(database, nil)
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	service.Now = func() time.Time { return now.Add(7 * time.Hour) }
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
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).Where("id = ?", command.ID).Updates(map[string]any{
		"deadline_at": now.Add(-time.Minute),
		"status":      CommandStatusRunning,
	}).Error)

	count, err := service.TimeoutExpired(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, CommandStatusTimeout, found.Status)
	assert.Equal(t, "command timeout", found.ErrorMessage)

	var history db.TaskHistory
	require.NoError(t, database.DB.First(&history, "command_id = ?", command.ID).Error)
	assert.Equal(t, TaskStatusTimeout, history.Status)
	assert.Contains(t, history.ErrorLog, "command timeout")
	assert.NotNil(t, history.FinishedAt)
}
```

- [ ] **Step 5: Implement timeout scanner method**

Add to `internal/master/commands/service.go`:

```go
func (s *Service) TimeoutExpired(ctx context.Context) (int64, error) {
	if s == nil || s.DB == nil || s.DB.DB == nil {
		return 0, nil
	}
	now := s.now()
	var expired []db.AgentCommand
	if err := s.DB.DB.WithContext(ctx).
		Where("status IN ? AND deadline_at IS NOT NULL AND deadline_at <= ?", []string{CommandStatusPending, CommandStatusDispatched, CommandStatusRunning}, now).
		Find(&expired).Error; err != nil {
		return 0, err
	}
	for _, command := range expired {
		finishedAt := now
		err := s.DB.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&db.AgentCommand{}).Where("id = ?", command.ID).Updates(map[string]any{
				"status":        CommandStatusTimeout,
				"error_message": "command timeout",
				"completed_at":  &finishedAt,
			}).Error; err != nil {
				return err
			}
			return tx.Model(&db.TaskHistory{}).Where("command_id = ?", command.ID).Updates(map[string]any{
				"status":      TaskStatusTimeout,
				"finished_at": &finishedAt,
				"error_log":   "command timeout",
			}).Error
		})
		if err != nil {
			return 0, err
		}
	}
	return int64(len(expired)), nil
}

func (s *Service) RunTimeoutScanner(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = s.TimeoutExpired(ctx)
		}
	}
}
```

- [ ] **Step 6: Update snapshot refresh tests**

Change `TestRefreshSnapshotsOffline` in `internal/master/api/snapshots_test.go`:

```go
func TestRefreshSnapshotsOfflineQueuesCommand(t *testing.T) {
	setup := setupSnapshotAPI(t)
	agent := createSnapshotTestAgent(t, setup.database, "offline")

	w := postAnyJSON(t, setup.router, "/api/agents/"+agent.ID+"/snapshots/refresh", map[string]any{})

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	body := parseJSON(t, w)
	data := requireMap(t, body["data"])
	assert.NotEmpty(t, data["command_id"])
	assert.NotEmpty(t, data["message_id"])

	var command db.AgentCommand
	require.NoError(t, setup.database.DB.First(&command, "id = ?", data["command_id"]).Error)
	assert.Equal(t, protocol.TypeSnapshotListReq, command.Type)
	assert.Equal(t, "pending", command.Status)
}
```

Update `setupSnapshotAPI`:

```go
commandService := commands.NewService(database, hub)
handler := NewSnapshotHandler(database, hub)
handler.Commands = commandService
```

Add import:

```go
"vaultfleet/internal/master/commands"
```

- [ ] **Step 7: Implement snapshot command persistence**

Modify `internal/master/api/snapshots.go`:

Add to `SnapshotHandler`:

```go
	Commands *commands.Service
```

In `RefreshSnapshots`, after message creation and before wait setup:

```go
	if h.Commands == nil {
		writeErrorResponse(c, http.StatusInternalServerError, "command service not configured")
		return
	}
	command, err := h.Commands.CreateCommand(c.Request.Context(), commands.CreateCommandInput{
		AgentID: agentID,
		Type:    protocol.TypeSnapshotListReq,
		Message: *msg,
	})
	if err != nil {
		writeErrorResponse(c, http.StatusInternalServerError, "database error")
		return
	}
	if h.Hub == nil || !h.Hub.IsOnline(agentID) {
		writeDataResponse(c, http.StatusAccepted, gin.H{
			"message":    "snapshot refresh queued",
			"command_id": command.ID,
			"message_id": msg.ID,
		})
		return
	}
```

After successful response and snapshot upsert:

```go
		_ = h.Commands.CompleteSnapshotList(c.Request.Context(), agentID, msg.ID, payload.Snapshots)
```

On timeout and payload error, update the command with these helpers in `internal/master/commands/service.go`:

```go
func (s *Service) CompleteSnapshotList(ctx context.Context, agentID string, messageID string, snapshots []protocol.SnapshotInfo) error {
	raw, err := json.Marshal(snapshots)
	if err != nil {
		return err
	}
	return s.completeCommand(ctx, agentID, messageID, CommandStatusSucceeded, string(raw), "")
}

func (s *Service) FailCommand(ctx context.Context, agentID string, messageID string, errorText string) error {
	return s.completeCommand(ctx, agentID, messageID, CommandStatusFailed, "", errorText)
}

func (s *Service) TimeoutCommand(ctx context.Context, agentID string, messageID string) error {
	return s.completeCommand(ctx, agentID, messageID, CommandStatusTimeout, "", "command timeout")
}

func (s *Service) completeCommand(ctx context.Context, agentID string, messageID string, status string, result string, errorText string) error {
	if s == nil || s.DB == nil || s.DB.DB == nil || messageID == "" {
		return nil
	}
	now := s.now()
	updates := map[string]any{
		"status":       status,
		"completed_at": &now,
		"updated_at":   now,
	}
	if result != "" {
		updates["result"] = result
	}
	if errorText != "" {
		updates["error_message"] = errorText
	}
	return s.DB.DB.WithContext(ctx).
		Model(&db.AgentCommand{}).
		Where("agent_id = ? AND message_id = ?", agentID, messageID).
		Updates(updates).Error
}
```

Use `h.Commands.FailCommand(c.Request.Context(), agentID, msg.ID, payload.Error)` when the Agent snapshot response includes `payload.Error`. Use `h.Commands.TimeoutCommand(c.Request.Context(), agentID, msg.ID)` when the response channel closes without a response.

- [ ] **Step 8: Run command, snapshot, and ws tests**

Run:

```bash
go test ./internal/master/commands ./internal/master/api ./internal/master/ws -run 'TestCompleteTaskResult|TestTimeoutExpired|TestRefreshSnapshots|TestHandler_TaskResultProcessor' -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit Task 4**

```bash
git add internal/master/commands/service.go internal/master/commands/service_test.go internal/master/api/snapshots.go internal/master/api/snapshots_test.go internal/master/ws/handler_test.go
git commit -m "feat: complete command lifecycle from agent results"
```

---

### Task 5: Storage Connection Test Service And API

**Files:**
- Create: `internal/master/storagecheck/service.go`
- Create: `internal/master/storagecheck/service_test.go`
- Modify: `internal/master/api/storage.go`
- Modify: `internal/master/api/storage_test.go`

- [ ] **Step 1: Write failing storagecheck service tests**

Create `internal/master/storagecheck/service_test.go`:

```go
package storagecheck

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceRunsRcloneWithTempConfigAndRedactsSecrets(t *testing.T) {
	runner := &recordingRunner{err: errors.New("failed with SECRET456 and token-123")}
	service := NewService(runner)
	service.Now = func() time.Time { return time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC) }

	result := service.Test(context.Background(), Request{
		RcloneType: "s3",
		RcloneConfig: map[string]string{
			"provider":          "Cloudflare",
			"access_key_id":     "AKID123",
			"secret_access_key": "SECRET456",
			"token":             "token-123",
		},
	})

	assert.False(t, result.OK)
	assert.Contains(t, result.Error, "[redacted]")
	assert.NotContains(t, result.Error, "SECRET456")
	assert.NotContains(t, result.Error, "token-123")
	require.Len(t, runner.calls, 1)
	assert.Contains(t, runner.calls[0].args, "lsd")
	assert.Contains(t, runner.calls[0].args, "vaultfleet:")
	_, err := os.Stat(runner.calls[0].configPath)
	assert.True(t, os.IsNotExist(err), "temp rclone config should be removed")
}

func TestServiceReportsSuccessfulConnection(t *testing.T) {
	runner := &recordingRunner{}
	service := NewService(runner)

	result := service.Test(context.Background(), Request{
		RcloneType:   "local",
		RcloneConfig: map[string]string{"nounc": "true"},
	})

	assert.True(t, result.OK)
	assert.Empty(t, result.Error)
	assert.GreaterOrEqual(t, result.LatencyMs, int64(0))
	assert.False(t, result.CheckedAt.IsZero())
}

type recordingRunner struct {
	err   error
	calls []runCall
}

type runCall struct {
	configPath string
	args       string
}

func (r *recordingRunner) Run(ctx context.Context, configPath string, args ...string) error {
	r.calls = append(r.calls, runCall{configPath: configPath, args: strings.Join(args, " ")})
	return r.err
}
```

- [ ] **Step 2: Implement storagecheck service**

Create `internal/master/storagecheck/service.go`:

```go
package storagecheck

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const remoteName = "vaultfleet"
const defaultTimeout = 15 * time.Second

type Runner interface {
	Run(ctx context.Context, configPath string, args ...string) error
}

type ExecRunner struct{}

type Service struct {
	Runner Runner
	Now    func() time.Time
	Timeout time.Duration
}

type Request struct {
	RcloneType   string
	RcloneConfig map[string]string
}

type Result struct {
	OK        bool      `json:"ok"`
	LatencyMs int64     `json:"latency_ms"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

func NewService(runner Runner) *Service {
	if runner == nil {
		runner = ExecRunner{}
	}
	return &Service{Runner: runner, Now: time.Now, Timeout: defaultTimeout}
}

func (s *Service) Test(ctx context.Context, request Request) Result {
	now := s.now()
	start := now
	result := Result{CheckedAt: now}
	dir, err := os.MkdirTemp("", "vaultfleet-rclone-test-*")
	if err != nil {
		result.Error = "create temp config failed"
		return result
	}
	defer os.RemoveAll(dir)
	configPath := filepath.Join(dir, "rclone.conf")
	if err := writeConfig(configPath, request); err != nil {
		result.Error = redact(err.Error(), request.RcloneConfig)
		return result
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err = s.Runner.Run(runCtx, configPath, "lsd", remoteName+":")
	result.LatencyMs = s.now().Sub(start).Milliseconds()
	if err != nil {
		result.Error = redact(err.Error(), request.RcloneConfig)
		return result
	}
	result.OK = true
	return result
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (ExecRunner) Run(ctx context.Context, configPath string, args ...string) error {
	fullArgs := append([]string{"--config", configPath}, args...)
	out, err := exec.CommandContext(ctx, "rclone", fullArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func writeConfig(path string, request Request) error {
	var builder strings.Builder
	builder.WriteString("[" + remoteName + "]\n")
	builder.WriteString("type = " + request.RcloneType + "\n")
	keys := make([]string, 0, len(request.RcloneConfig))
	for key := range request.RcloneConfig {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		builder.WriteString(key + " = " + request.RcloneConfig[key] + "\n")
	}
	return os.WriteFile(path, []byte(builder.String()), 0o600)
}

func redact(message string, config map[string]string) string {
	result := message
	for key, value := range config {
		lower := strings.ToLower(key)
		if value == "" {
			continue
		}
		if strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || lower == "pass" || strings.Contains(lower, "access_key") {
			result = strings.ReplaceAll(result, value, "[redacted]")
		}
	}
	return result
}
```

- [ ] **Step 3: Run storagecheck tests**

Run:

```bash
go test ./internal/master/storagecheck -count=1
```

Expected: PASS.

- [ ] **Step 4: Write failing storage API tests**

Append to `internal/master/api/storage_test.go`:

```go
func TestStorageTestUnsavedConfigDoesNotPersistAndReturnsResult(t *testing.T) {
	setup := setupTestConfigAPI(t)
	fake := &fakeStorageTester{result: storagecheck.Result{OK: true, LatencyMs: 12, CheckedAt: time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)}}
	handler := NewConfigHandler(setup.database)
	handler.StorageTester = fake
	router := gin.New()
	RegisterStorageRoutes(router.Group("/api"), handler)

	w := postAnyJSON(t, router, "/api/storage/test", map[string]any{
		"rclone_type": "s3",
		"rclone_config": map[string]any{
			"secret_access_key": "SECRET456",
			"endpoint":          "https://example.test",
		},
	})

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	data := requireMap(t, body["data"])
	assert.Equal(t, true, data["ok"])
	assert.Equal(t, float64(12), data["latency_ms"])

	var count int64
	require.NoError(t, setup.database.DB.Model(&db.StorageConfig{}).Count(&count).Error)
	assert.Equal(t, int64(0), count)
	require.Len(t, fake.requests, 1)
	assert.Equal(t, "SECRET456", fake.requests[0].RcloneConfig["secret_access_key"])
}

func TestStorageTestSavedConfigDecryptsConfig(t *testing.T) {
	setup := setupTestConfigAPI(t)
	created := createStorageConfig(t, setup.router, "R2", map[string]any{"secret_access_key": "SECRET456"})
	fake := &fakeStorageTester{result: storagecheck.Result{OK: true, CheckedAt: time.Now()}}
	handler := NewConfigHandler(setup.database)
	handler.StorageTester = fake
	router := gin.New()
	RegisterStorageRoutes(router.Group("/api"), handler)

	w := postAnyJSON(t, router, "/api/storage/"+created["id"].(string)+"/test", map[string]any{})

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, fake.requests, 1)
	assert.Equal(t, "SECRET456", fake.requests[0].RcloneConfig["secret_access_key"])
}

type fakeStorageTester struct {
	result   storagecheck.Result
	requests []storagecheck.Request
}

func (f *fakeStorageTester) Test(ctx context.Context, request storagecheck.Request) storagecheck.Result {
	f.requests = append(f.requests, request)
	return f.result
}
```

Add imports:

```go
"context"
"vaultfleet/internal/master/storagecheck"
```

- [ ] **Step 5: Implement storage test routes**

Modify `internal/master/api/storage.go`:

Add imports:

```go
"context"
"vaultfleet/internal/master/storagecheck"
```

Add interface and field:

```go
type StorageTester interface {
	Test(ctx context.Context, request storagecheck.Request) storagecheck.Result
}

type ConfigHandler struct {
	DB        *db.Database
	EventBus  *events.Bus
	MasterKey []byte
	StorageTester StorageTester
	...
}
```

Set default in `NewConfigHandler`:

```go
StorageTester: storagecheck.NewService(nil),
```

Register routes:

```go
	rg.POST("/storage/test", h.TestUnsavedStorage)
	rg.POST("/storage/:id/test", h.TestSavedStorage)
```

Add request and helpers:

```go
type testStorageRequest struct {
	RcloneType   string         `json:"rclone_type" binding:"required"`
	RcloneConfig map[string]any `json:"rclone_config" binding:"required"`
}

func (h *ConfigHandler) TestUnsavedStorage(c *gin.Context) {
	var request testStorageRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeErrorResponse(c, http.StatusBadRequest, "invalid request")
		return
	}
	config, ok := stringifyRcloneConfig(c, request.RcloneConfig)
	if !ok {
		return
	}
	result := h.StorageTester.Test(c.Request.Context(), storagecheck.Request{RcloneType: request.RcloneType, RcloneConfig: config})
	writeDataResponse(c, http.StatusOK, result)
}

func (h *ConfigHandler) TestSavedStorage(c *gin.Context) {
	storage, ok := h.findStorageByID(c, c.Param("id"))
	if !ok {
		return
	}
	configAny, err := h.decryptMap(storage.RcloneConfig)
	if err != nil {
		writeErrorResponse(c, http.StatusInternalServerError, "decrypt storage config")
		return
	}
	config, ok := stringifyRcloneConfig(c, configAny)
	if !ok {
		return
	}
	result := h.StorageTester.Test(c.Request.Context(), storagecheck.Request{RcloneType: storage.RcloneType, RcloneConfig: config})
	writeDataResponse(c, http.StatusOK, result)
}

func stringifyRcloneConfig(c *gin.Context, input map[string]any) (map[string]string, bool) {
	result := make(map[string]string, len(input))
	for key, value := range input {
		text, ok := value.(string)
		if !ok {
			writeErrorResponse(c, http.StatusBadRequest, "rclone config values must be strings")
			return nil, false
		}
		result[key] = text
	}
	return result, true
}
```

- [ ] **Step 6: Run storage tests**

Run:

```bash
go test ./internal/master/storagecheck ./internal/master/api -run 'TestStorageTest|TestCreateStorage|TestUpdateStorage|TestStorageResponses' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit Task 5**

```bash
git add internal/master/storagecheck/service.go internal/master/storagecheck/service_test.go internal/master/api/storage.go internal/master/api/storage_test.go
git commit -m "feat: add storage connection test API"
```

---

### Task 6: Health, Readiness, Metrics, And Master Wiring

**Files:**
- Create: `internal/master/api/health.go`
- Create: `internal/master/api/health_test.go`
- Modify: `internal/master/api/router.go`
- Modify: `cmd/master/main.go`
- Modify: `internal/master/api/router_test.go`

- [ ] **Step 1: Write failing health and metrics tests**

Create `internal/master/api/health_test.go`:

```go
package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vaultfleet/internal/master/db"
	"vaultfleet/internal/master/ws"
)

func TestHealthDoesNotRequireDatabase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterHealthRoutes(router, NewHealthHandler(nil, nil))

	w := getJSON(t, router, "/health")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, true, body["ok"])
	assert.Equal(t, "healthy", body["status"])
}

func TestReadyChecksDatabase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database, err := db.New(t.TempDir())
	require.NoError(t, err)
	router := gin.New()
	RegisterHealthRoutes(router, NewHealthHandler(database, nil))

	w := getJSON(t, router, "/ready")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, true, body["ok"])
	assert.Equal(t, "ready", body["status"])
}

func TestReadyFailsWhenDatabaseClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database, err := db.New(t.TempDir())
	require.NoError(t, err)
	sqlDB, err := database.DB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	router := gin.New()
	RegisterHealthRoutes(router, NewHealthHandler(database, nil))

	w := getJSON(t, router, "/ready")

	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, false, body["ok"])
	assert.Equal(t, "not ready", body["error"])
}

func TestMetricsOutputsPrometheusText(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database, err := db.New(t.TempDir())
	require.NoError(t, err)
	hub := ws.NewHub()
	agent := db.Agent{Name: "Metrics Agent", Status: "online"}
	require.NoError(t, database.DB.Create(&agent).Error)
	router := gin.New()
	RegisterHealthRoutes(router, NewHealthHandler(database, hub))

	w := getJSON(t, router, "/metrics")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Header().Get("Content-Type"), "text/plain")
	body := w.Body.String()
	for _, metric := range []string{
		"vaultfleet_agents_total",
		"vaultfleet_agents_online",
		"vaultfleet_agent_commands_total",
		"vaultfleet_tasks_total",
		"vaultfleet_last_successful_backup_timestamp_seconds",
	} {
		assert.True(t, strings.Contains(body, metric), metric)
	}
}
```

- [ ] **Step 2: Implement health handler**

Create `internal/master/api/health.go`:

```go
package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"vaultfleet/internal/master/db"
	"vaultfleet/internal/master/ws"
)

type AgentStatusProvider interface {
	GetAllAgents() map[string]*ws.AgentStatus
}

type HealthHandler struct {
	DB  *db.Database
	Hub AgentStatusProvider
}

func NewHealthHandler(database *db.Database, hub AgentStatusProvider) *HealthHandler {
	return &HealthHandler{DB: database, Hub: hub}
}

func RegisterHealthRoutes(router *gin.Engine, h *HealthHandler) {
	router.GET("/health", h.Health)
	router.GET("/ready", h.Ready)
	router.GET("/metrics", h.Metrics)
}

func (h *HealthHandler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": "healthy"})
}

func (h *HealthHandler) Ready(c *gin.Context) {
	if h == nil || h.DB == nil || h.DB.DB == nil || len(h.DB.MasterKey) != 32 || h.DB.DataDir == "" {
		writeErrorResponse(c, http.StatusServiceUnavailable, "not ready")
		return
	}
	sqlDB, err := h.DB.DB.DB()
	if err != nil || sqlDB.Ping() != nil {
		writeErrorResponse(c, http.StatusServiceUnavailable, "not ready")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": "ready"})
}

func (h *HealthHandler) Metrics(c *gin.Context) {
	if h == nil || h.DB == nil || h.DB.DB == nil {
		c.String(http.StatusInternalServerError, "database not configured\n")
		return
	}
	var totalAgents int64
	if err := h.DB.DB.Model(&db.Agent{}).Count(&totalAgents).Error; err != nil {
		c.String(http.StatusInternalServerError, "database error\n")
		return
	}
	online := 0
	if h.Hub != nil {
		for _, status := range h.Hub.GetAllAgents() {
			if status != nil && status.Online {
				online++
			}
		}
	}
	var builder strings.Builder
	builder.WriteString("# TYPE vaultfleet_agents_total gauge\n")
	builder.WriteString(fmt.Sprintf("vaultfleet_agents_total %d\n", totalAgents))
	builder.WriteString("# TYPE vaultfleet_agents_online gauge\n")
	builder.WriteString(fmt.Sprintf("vaultfleet_agents_online %d\n", online))
	writeCommandMetrics(&builder, h.DB)
	writeTaskMetrics(&builder, h.DB)
	writeLastBackupMetric(&builder, h.DB)
	c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", []byte(builder.String()))
}
```

Add helper functions in the same file:

```go
func writeCommandMetrics(builder *strings.Builder, database *db.Database) {
	type row struct{ Status, Type string; Count int64 }
	var rows []row
	_ = database.DB.Model(&db.AgentCommand{}).Select("status, type, count(*) as count").Group("status, type").Find(&rows).Error
	builder.WriteString("# TYPE vaultfleet_agent_commands_total gauge\n")
	for _, item := range rows {
		builder.WriteString(fmt.Sprintf("vaultfleet_agent_commands_total{status=%q,type=%q} %d\n", item.Status, item.Type, item.Count))
	}
}

func writeTaskMetrics(builder *strings.Builder, database *db.Database) {
	type row struct{ Status, Type string; Count int64 }
	var rows []row
	_ = database.DB.Model(&db.TaskHistory{}).Select("status, type, count(*) as count").Group("status, type").Find(&rows).Error
	builder.WriteString("# TYPE vaultfleet_tasks_total gauge\n")
	for _, item := range rows {
		builder.WriteString(fmt.Sprintf("vaultfleet_tasks_total{status=%q,type=%q} %d\n", item.Status, item.Type, item.Count))
	}
}

func writeLastBackupMetric(builder *strings.Builder, database *db.Database) {
	var history db.TaskHistory
	err := database.DB.Where("type = ? AND status = ?", "backup", "success").Order("finished_at DESC").First(&history).Error
	value := int64(0)
	if err == nil && history.FinishedAt != nil {
		value = history.FinishedAt.Unix()
	}
	builder.WriteString("# TYPE vaultfleet_last_successful_backup_timestamp_seconds gauge\n")
	builder.WriteString(fmt.Sprintf("vaultfleet_last_successful_backup_timestamp_seconds %d\n", value))
}
```

- [ ] **Step 3: Register health routes**

Modify `internal/master/api/router.go`:

```go
	RegisterHealthRoutes(r, NewHealthHandler(cfg.Database, cfg.Hub))
```

This must be registered before `RegisterFrontendRoutes(r)` so `/health`, `/ready`, and `/metrics` never fall through to the SPA.

Extend `RouterHub` with `GetAllAgents`:

```go
	GetAllAgents() map[string]*ws.AgentStatus
```

Then import `vaultfleet/internal/master/ws` in `router.go`.

- [ ] **Step 4: Wire command service in master**

Modify `cmd/master/main.go`:

```go
commandService := commands.NewService(database, hub)
policyLookup := api.CurrentPolicyLookup(database)
```

Update imports:

```go
"vaultfleet/internal/master/commands"
```

Pass command service into processors:

```go
	api.NewTaskResultProcessor(database, commandService),
```

Policy ack:

```go
wsHandler.PolicyAckProcessor = api.NewPolicyAckProcessor(database, commandService)
```

Set connect dispatcher:

```go
wsHandler.PendingCommandDispatcher = func(agentID string) error {
	return commandService.DispatchPendingForAgent(ctx, agentID, 20)
}
```

Wire pusher:

```go
pusher := api.NewPolicyChangedPusher(database, hub, policyLookup)
pusher.Commands = commandService
bus.Subscribe(events.PolicyChanged, pusher.Handle)
```

Pass to router:

```go
CommandService: commandService,
```

Start timeout scanner:

```go
go commandService.RunTimeoutScanner(ctx, time.Minute)
```

- [ ] **Step 5: Run health and router tests**

Run:

```bash
go test ./internal/master/api -run 'TestHealth|TestReady|TestMetrics|TestRouterAssembly' -count=1
```

Expected: PASS.

- [ ] **Step 6: Run full backend suite**

Run:

```bash
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit Task 6**

```bash
git add internal/master/api/health.go internal/master/api/health_test.go internal/master/api/router.go internal/master/api/router_test.go cmd/master/main.go
git commit -m "feat: add health readiness and metrics"
```

---

## Final Verification

- [ ] Run full backend tests:

```bash
go test ./... -count=1
```

Expected: PASS.

- [ ] Run frontend tests only if API type files or frontend files changed during implementation:

```bash
cd web && npm test
cd web && npm run build
```

Expected: PASS.

- [ ] Confirm there are no leaked secrets in command API responses:

```bash
go test ./internal/master/api -run 'TestGetCommandRedactsPayload|TestStorageTest' -count=1
```

Expected: PASS.

- [ ] Inspect changed files:

```bash
git status --short
git log --oneline -6
```

Expected: only intentional implementation changes remain, and each task has its own commit.

## Self-Review Notes

Spec coverage:

- Durable Agent commands: Tasks 1-4.
- Enhanced task records: Tasks 1-2 and Task 4.
- Offline queue and reconnect delivery: Tasks 2-3.
- Timeout lifecycle: Task 4.
- Storage connection test: Task 5.
- `/health`, `/ready`, `/metrics`: Task 6.
- API compatibility: Tasks 2, 4, and 6 keep existing routes and add fields instead of removing old responses.
- Secret handling: Tasks 1, 2, and 5 encrypt command payloads at rest and redact API/test errors.

Type consistency:

- Command statuses live in `internal/master/commands`.
- Task statuses preserve existing persisted values `success` and `failed`; command status maps `success` to `succeeded`.
- Command API never returns raw `AgentCommand.Payload`.

Implementation constraints:

- Keep WebSocket protocol message types unchanged.
- Keep `TaskHistory` table name unchanged.
- Do not introduce a Prometheus dependency for the first metrics endpoint.
- Do not add frontend work to this plan.
