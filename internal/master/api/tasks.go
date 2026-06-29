package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"vaultfleet/internal/master/commands"
	"vaultfleet/internal/master/db"
	"vaultfleet/pkg/protocol"
)

const defaultTaskListLimit = 50
const maxTaskListLimit = 200

type TaskHandler struct {
	DB             *db.Database
	Hub            CommandHub
	Commands       *commands.Service
	ProgressGetter func(agentID string, messageID string) *protocol.BackupProgressPayload
}

type CommandHub interface {
	IsOnline(agentID string) bool
	Send(agentID string, msg interface{}) error
}

type taskResponse struct {
	ID            string                          `json:"id"`
	AgentID       string                          `json:"agent_id"`
	Type          string                          `json:"type"`
	Status        string                          `json:"status"`
	SnapshotID    string                          `json:"snapshot_id"`
	MessageID     string                          `json:"message_id,omitempty"`
	CommandID     string                          `json:"command_id,omitempty"`
	PolicyID      string                          `json:"policy_id,omitempty"`
	StorageID     string                          `json:"storage_id,omitempty"`
	StartedAt     *time.Time                      `json:"started_at"`
	FinishedAt    *time.Time                      `json:"finished_at"`
	DurationMs    int64                           `json:"duration_ms"`
	RepoSize      int64                           `json:"repo_size"`
	RepoSizeBytes int64                           `json:"repository_size_bytes"`
	ErrorLog      string                          `json:"error_log,omitempty"`
	Error         string                          `json:"error,omitempty"`
	Progress      *protocol.BackupProgressPayload `json:"progress,omitempty"`
	CreatedAt     time.Time                       `json:"created_at"`
	UpdatedAt     time.Time                       `json:"updated_at"`
}

func NewTaskHandler(database *db.Database, hub CommandHub) *TaskHandler {
	return &TaskHandler{DB: database, Hub: hub}
}

func RegisterTaskRoutes(rg *gin.RouterGroup, h *TaskHandler) {
	rg.GET("/tasks", h.List)
	rg.POST("/tasks/:id/cancel", h.CancelTask)
	rg.POST("/agents/:id/backup-now", h.BackupNow)
	rg.POST("/agents/:id/maintenance", h.Maintenance)
}

var validMaintenanceOps = map[string]bool{
	"unlock":           true,
	"check":            true,
	"repair_index":     true,
	"repair_snapshots": true,
	"prune":            true,
}

func (h *TaskHandler) Maintenance(c *gin.Context) {
	agentID := c.Param("id")
	if !agentExistsByID(c, h.DB, agentID) {
		return
	}

	var req struct {
		Operation string `json:"operation"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "invalid request body"})
		return
	}
	if !validMaintenanceOps[req.Operation] {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "invalid maintenance operation"})
		return
	}

	msg, err := protocol.NewMessage(protocol.TypeMaintenance, protocol.MaintenancePayload{AgentID: agentID, Operation: req.Operation})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "encode maintenance request"})
		return
	}
	commandService := h.commandService()

	command, err := commandService.CreateCommand(contextFromGin(c), commands.CreateCommandInput{
		AgentID:   agentID,
		Type:      protocol.TypeMaintenance,
		Message:   *msg,
		TaskType:  "maintenance",
		TaskState: commands.TaskStatusPending,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "database error"})
		return
	}

	if h.Hub != nil && h.Hub.IsOnline(agentID) {
		if err := commandService.DispatchNewPendingForAgent(contextFromGin(c), agentID, 100); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "database error"})
			return
		}
	}

	writeDataResponse(c, http.StatusAccepted, gin.H{
		"command_id": command.ID,
		"message_id": msg.ID,
	})
}

func (h *TaskHandler) BackupNow(c *gin.Context) {
	agentID := c.Param("id")
	if !agentExistsByID(c, h.DB, agentID) {
		return
	}

	msg, err := protocol.NewMessage(protocol.TypeBackupNow, protocol.BackupNowPayload{AgentID: agentID})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "encode backup request"})
		return
	}
	commandService := h.commandService()

	var timeoutHours int
	var policyID, storageID string
	var policy db.BackupPolicy
	if err := h.DB.DB.Where("agent_id = ?", agentID).First(&policy).Error; err == nil {
		timeoutHours = normalizedPolicyTimeoutHours(policy.TimeoutHours)
		policyID = policy.ID
		storageID = policy.StorageID
	}

	command, err := commandService.CreateCommand(contextFromGin(c), commands.CreateCommandInput{
		AgentID:      agentID,
		Type:         protocol.TypeBackupNow,
		Message:      *msg,
		TaskType:     "backup",
		TaskState:    commands.TaskStatusPending,
		PolicyID:     policyID,
		StorageID:    storageID,
		TimeoutHours: timeoutHours,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "database error"})
		return
	}

	if h.Hub != nil && h.Hub.IsOnline(agentID) {
		if err := commandService.DispatchNewPendingForAgent(contextFromGin(c), agentID, 100); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "database error"})
			return
		}
	}

	writeDataResponse(c, http.StatusAccepted, gin.H{
		"command_id": command.ID,
		"message_id": msg.ID,
	})
}

func (h *TaskHandler) CancelTask(c *gin.Context) {
	taskID := c.Param("id")

	var history db.TaskHistory
	if err := h.DB.DB.First(&history, "id = ?", taskID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"ok": false, "error": "task not found"})
		return
	}

	if history.Status != commands.TaskStatusRunning && history.Status != commands.TaskStatusPending {
		c.JSON(http.StatusConflict, gin.H{"ok": false, "error": "task is not running or pending"})
		return
	}

	if history.CommandID == "" {
		c.JSON(http.StatusConflict, gin.H{"ok": false, "error": "task has no associated command"})
		return
	}

	commandService := h.commandService()
	result, err := commandService.CancelCommand(contextFromGin(c), history.CommandID)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"ok": false, "error": err.Error()})
		return
	}

	if result.NeedsWS {
		if h.Hub == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "error": "agent websocket unavailable"})
			return
		}
		if !h.Hub.IsOnline(result.AgentID) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "error": "agent is offline"})
			return
		}
		cancelMsg, err := protocol.NewMessage(protocol.TypeCancelTask, protocol.CancelTaskPayload{
			AgentID:   result.AgentID,
			MessageID: result.MessageID,
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "encode cancel command"})
			return
		}
		if err := h.Hub.Send(result.AgentID, *cancelMsg); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "error": "send cancel command failed"})
			return
		}
	}

	c.JSON(http.StatusAccepted, gin.H{"ok": true, "message": "cancel requested"})
}

func (h *TaskHandler) commandService() *commands.Service {
	if h.Commands != nil {
		return h.Commands
	}
	return commands.NewService(h.DB, h.Hub)
}

func contextFromGin(c *gin.Context) context.Context {
	if c == nil || c.Request == nil {
		return context.Background()
	}
	return c.Request.Context()
}

func (h *TaskHandler) List(c *gin.Context) {
	limit := parseTaskLimit(c.Query("limit"))
	query := h.DB.DB.Order("created_at DESC").Limit(limit)
	if agentID := c.Query("agent_id"); agentID != "" {
		query = query.Where("agent_id = ?", agentID)
	}
	if taskType := c.Query("type"); taskType != "" {
		query = query.Where("type = ?", taskType)
	}
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	var histories []db.TaskHistory
	if err := query.Find(&histories).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusOK, gin.H{"ok": true, "data": []taskResponse{}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": "database error"})
		return
	}

	responses := make([]taskResponse, 0, len(histories))
	for _, history := range histories {
		response := newTaskResponse(history)
		if h.ProgressGetter != nil && taskCanIncludeProgress(history) {
			response.Progress = h.ProgressGetter(history.AgentID, history.MessageID)
		}
		responses = append(responses, response)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": responses})
}

func taskCanIncludeProgress(history db.TaskHistory) bool {
	if history.Type != "backup" {
		return false
	}
	if history.MessageID == "" {
		return false
	}
	return history.Status == commands.TaskStatusRunning || history.Status == commands.TaskStatusPending
}

func parseTaskLimit(raw string) int {
	if raw == "" {
		return defaultTaskListLimit
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return defaultTaskListLimit
	}
	if limit > maxTaskListLimit {
		return maxTaskListLimit
	}
	return limit
}

func newTaskResponse(history db.TaskHistory) taskResponse {
	return taskResponse{
		ID:            history.ID,
		AgentID:       history.AgentID,
		Type:          history.Type,
		Status:        history.Status,
		SnapshotID:    history.SnapshotID,
		MessageID:     history.MessageID,
		CommandID:     history.CommandID,
		PolicyID:      history.PolicyID,
		StorageID:     history.StorageID,
		StartedAt:     history.StartedAt,
		FinishedAt:    history.FinishedAt,
		DurationMs:    history.DurationMs,
		RepoSize:      history.RepoSize,
		RepoSizeBytes: history.RepoSize,
		ErrorLog:      history.ErrorLog,
		Error:         history.ErrorLog,
		CreatedAt:     history.CreatedAt,
		UpdatedAt:     history.UpdatedAt,
	}
}
