package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"vaultfleet/internal/master/commands"
	"vaultfleet/internal/master/db"
	"vaultfleet/pkg/protocol"
)

const snapshotRequestTimeout = 30 * time.Second

type SnapshotHandler struct {
	DB       *db.Database
	Hub      SnapshotHub
	Commands *commands.Service

	timeout     time.Duration
	sendAndWait func(agentID string, msg protocol.Message, timeout time.Duration) (<-chan protocol.Message, error)
}

type SnapshotHub interface {
	IsOnline(agentID string) bool
	SendAndWait(agentID string, msg protocol.Message, timeout time.Duration) (<-chan protocol.Message, error)
}

type TaskResultCommandCompleter interface {
	CompleteTaskResult(ctx context.Context, agentID string, messageID string, result protocol.TaskResultPayload) error
}

type SnapshotListCommandCompleter interface {
	CompleteSnapshotList(ctx context.Context, agentID string, messageID string, result protocol.SnapshotListRespPayload) error
	FailCommand(ctx context.Context, agentID string, messageID string, errorText string) error
}

type snapshotResponse struct {
	ID         string    `json:"id"`
	SnapshotID string    `json:"snapshot_id"`
	Timestamp  time.Time `json:"timestamp"`
	Paths      []string  `json:"paths"`
	Size       int64     `json:"size"`
}

type snapshotRefreshResponse struct {
	Count     int                `json:"count"`
	Snapshots []snapshotResponse `json:"snapshots"`
}

func NewSnapshotHandler(database *db.Database, hub SnapshotHub) *SnapshotHandler {
	handler := &SnapshotHandler{
		DB:      database,
		Hub:     hub,
		timeout: snapshotRequestTimeout,
	}
	handler.sendAndWait = func(agentID string, msg protocol.Message, timeout time.Duration) (<-chan protocol.Message, error) {
		return handler.Hub.SendAndWait(agentID, msg, timeout)
	}
	return handler
}

func RegisterSnapshotRoutes(rg *gin.RouterGroup, h *SnapshotHandler) {
	rg.GET("/agents/:id/snapshots", h.ListSnapshots)
	rg.POST("/agents/:id/snapshots/refresh", h.RefreshSnapshots)
}

func (h *SnapshotHandler) ListSnapshots(c *gin.Context) {
	agentID := c.Param("id")
	if !agentExistsByID(c, h.DB, agentID) {
		return
	}

	snapshots, ok := h.loadSnapshotResponses(c, agentID)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": snapshots})
}

func (h *SnapshotHandler) RefreshSnapshots(c *gin.Context) {
	agentID := c.Param("id")
	if !agentExistsByID(c, h.DB, agentID) {
		return
	}
	commandService := h.Commands
	if commandService == nil {
		writeErrorResponse(c, http.StatusInternalServerError, "command service not configured")
		return
	}

	msg, err := protocol.NewMessage(protocol.TypeSnapshotListReq, protocol.SnapshotListReqPayload{AgentID: agentID})
	if err != nil {
		writeErrorResponse(c, http.StatusInternalServerError, "encode snapshot list request")
		return
	}
	command, err := commandService.CreateCommand(contextFromGin(c), commands.CreateCommandInput{
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
			"command_id": command.ID,
			"message_id": msg.ID,
		})
		return
	}

	wait := h.sendAndWait
	if wait == nil && h.Hub != nil {
		wait = h.Hub.SendAndWait
	}
	if wait == nil {
		_ = commandService.FailCommand(contextFromGin(c), agentID, msg.ID, "agent offline")
		writeErrorResponse(c, http.StatusBadGateway, "agent offline")
		return
	}

	respCh, err := wait(agentID, *msg, h.timeout)
	if err != nil {
		_ = commandService.FailCommand(contextFromGin(c), agentID, msg.ID, "agent offline")
		writeErrorResponse(c, http.StatusBadGateway, "agent offline")
		return
	}

	select {
	case resp, ok := <-respCh:
		if !ok {
			_ = commandService.TimeoutCommand(contextFromGin(c), agentID, msg.ID)
			writeErrorResponse(c, http.StatusGatewayTimeout, "timeout waiting for agent response")
			return
		}
		if resp.Type != protocol.TypeSnapshotListResp {
			_ = commandService.FailCommand(contextFromGin(c), agentID, msg.ID, "invalid agent response")
			writeErrorResponse(c, http.StatusBadGateway, "invalid agent response")
			return
		}
		payload, err := protocol.ParsePayload[protocol.SnapshotListRespPayload](&resp)
		if err != nil {
			_ = commandService.FailCommand(contextFromGin(c), agentID, msg.ID, "invalid agent response")
			writeErrorResponse(c, http.StatusBadGateway, "invalid agent response")
			return
		}
		if payload.Error != "" {
			_ = commandService.FailCommand(contextFromGin(c), agentID, msg.ID, payload.Error)
			writeErrorResponse(c, http.StatusBadGateway, payload.Error)
			return
		}
		if err := upsertSnapshots(h.DB, agentID, payload.Snapshots); err != nil {
			_ = commandService.FailCommand(contextFromGin(c), agentID, msg.ID, "database error")
			writeErrorResponse(c, http.StatusInternalServerError, "database error")
			return
		}
		if err := commandService.CompleteSnapshotList(contextFromGin(c), agentID, msg.ID, *payload); err != nil {
			writeErrorResponse(c, http.StatusInternalServerError, "database error")
			return
		}
		snapshots, ok := h.loadSnapshotResponses(c, agentID)
		if !ok {
			return
		}
		writeDataResponse(c, http.StatusOK, snapshotRefreshResponse{Count: len(payload.Snapshots), Snapshots: snapshots})
	case <-c.Request.Context().Done():
		_ = commandService.TimeoutCommand(contextFromGin(c), agentID, msg.ID)
		writeErrorResponse(c, http.StatusGatewayTimeout, "request cancelled")
	}
}

func (h *SnapshotHandler) loadSnapshotResponses(c *gin.Context, agentID string) ([]snapshotResponse, bool) {
	var snapshots []db.Snapshot
	if err := h.DB.DB.Where("agent_id = ?", agentID).Order("timestamp DESC").Find(&snapshots).Error; err != nil {
		writeErrorResponse(c, http.StatusInternalServerError, "database error")
		return nil, false
	}

	responses := make([]snapshotResponse, 0, len(snapshots))
	for _, snapshot := range snapshots {
		response, err := newSnapshotResponse(snapshot)
		if err != nil {
			writeErrorResponse(c, http.StatusInternalServerError, "decode snapshot")
			return nil, false
		}
		responses = append(responses, response)
	}
	return responses, true
}

func newSnapshotResponse(snapshot db.Snapshot) (snapshotResponse, error) {
	var paths []string
	if snapshot.Paths != "" {
		if err := json.Unmarshal([]byte(snapshot.Paths), &paths); err != nil {
			return snapshotResponse{}, err
		}
	}
	return snapshotResponse{
		ID:         snapshot.ID,
		SnapshotID: snapshot.SnapshotID,
		Timestamp:  snapshot.Timestamp,
		Paths:      paths,
		Size:       snapshot.Size,
	}, nil
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

func NewSnapshotListResponseProcessor(database *db.Database, completer SnapshotListCommandCompleter) func(agentID string, msg protocol.Message) error {
	return func(agentID string, msg protocol.Message) error {
		payload, err := protocol.ParsePayload[protocol.SnapshotListRespPayload](&msg)
		if err != nil {
			return err
		}
		if payload.Error != "" {
			if completer == nil {
				return nil
			}
			return completer.FailCommand(context.Background(), agentID, msg.ID, payload.Error)
		}
		if err := upsertSnapshots(database, agentID, payload.Snapshots); err != nil {
			if completer != nil {
				_ = completer.FailCommand(context.Background(), agentID, msg.ID, "database error")
			}
			return err
		}
		if completer == nil {
			return nil
		}
		return completer.CompleteSnapshotList(context.Background(), agentID, msg.ID, *payload)
	}
}

func recordTaskResult(database *db.Database, agentID string, messageID string, result protocol.TaskResultPayload) error {
	if database == nil || database.DB == nil {
		return errors.New("database not configured")
	}
	if agentID == "" {
		agentID = result.AgentID
	}
	if messageID != "" {
		var history db.TaskHistory
		err := database.DB.
			Where("agent_id = ? AND message_id = ? AND command_id <> ?", agentID, messageID, "").
			First(&history).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	if result.TaskType == "restore" {
		return completeRestoreTaskResult(database, agentID, messageID, result)
	}
	if result.TaskType == "backup" && result.Status == "success" && len(result.Snapshots) > 0 {
		return database.DB.Transaction(func(tx *gorm.DB) error {
			if err := createTaskHistory(tx, agentID, messageID, result); err != nil {
				return err
			}
			return upsertSnapshotsDB(tx, agentID, result.Snapshots)
		})
	}
	return createTaskHistory(database.DB, agentID, messageID, result)
}

func createTaskHistory(gormDB *gorm.DB, agentID string, messageID string, result protocol.TaskResultPayload) error {
	startedAt := result.StartedAt
	finishedAt := result.FinishedAt
	history := db.TaskHistory{
		AgentID:    agentID,
		Type:       result.TaskType,
		Status:     result.Status,
		SnapshotID: result.SnapshotID,
		MessageID:  messageID,
		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
		DurationMs: result.DurationMs,
		RepoSize:   result.RepoSize,
		ErrorLog:   result.ErrorLog,
	}
	if result.StartedAt.IsZero() {
		history.StartedAt = nil
	}
	if result.FinishedAt.IsZero() {
		history.FinishedAt = nil
	}
	return gormDB.Create(&history).Error
}

func completeRestoreTaskResult(database *db.Database, agentID string, messageID string, result protocol.TaskResultPayload) error {
	startedAt := result.StartedAt
	finishedAt := result.FinishedAt
	if messageID != "" {
		var history db.TaskHistory
		err := database.DB.
			Where("agent_id = ? AND type = ? AND status = ? AND message_id = ?", agentID, "restore", "running", messageID).
			First(&history).Error
		if err == nil {
			history.Status = result.Status
			history.DurationMs = result.DurationMs
			history.RepoSize = result.RepoSize
			history.ErrorLog = result.ErrorLog
			if result.SnapshotID != "" {
				history.SnapshotID = result.SnapshotID
			}
			if !result.StartedAt.IsZero() {
				history.StartedAt = &startedAt
			}
			if !result.FinishedAt.IsZero() {
				history.FinishedAt = &finishedAt
			}
			return database.DB.Save(&history).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}

	history := db.TaskHistory{
		AgentID:    agentID,
		Type:       result.TaskType,
		Status:     result.Status,
		SnapshotID: result.SnapshotID,
		MessageID:  messageID,
		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
		DurationMs: result.DurationMs,
		RepoSize:   result.RepoSize,
		ErrorLog:   result.ErrorLog,
	}
	if result.StartedAt.IsZero() {
		history.StartedAt = nil
	}
	if result.FinishedAt.IsZero() {
		history.FinishedAt = nil
	}
	return database.DB.Create(&history).Error
}

func upsertSnapshots(database *db.Database, agentID string, snapshots []protocol.SnapshotInfo) error {
	if database == nil || database.DB == nil {
		return errors.New("database not configured")
	}
	return upsertSnapshotsDB(database.DB, agentID, snapshots)
}

func upsertSnapshotsDB(gormDB *gorm.DB, agentID string, snapshots []protocol.SnapshotInfo) error {
	for _, snapshotInfo := range snapshots {
		if snapshotInfo.ID == "" {
			continue
		}
		paths, err := json.Marshal(snapshotInfo.Paths)
		if err != nil {
			return err
		}

		snapshot := db.Snapshot{
			AgentID:    agentID,
			SnapshotID: snapshotInfo.ID,
			Timestamp:  snapshotInfo.Time,
			Paths:      string(paths),
			Size:       snapshotInfo.Size,
		}
		err = gormDB.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "agent_id"},
				{Name: "snapshot_id"},
			},
			DoUpdates: clause.AssignmentColumns([]string{"timestamp", "paths", "size"}),
		}).Create(&snapshot).Error
		if err != nil {
			return err
		}
	}
	return nil
}

func agentExistsByID(c *gin.Context, database *db.Database, agentID string) bool {
	var agent db.Agent
	if err := database.DB.First(&agent, "id = ?", agentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeErrorResponse(c, http.StatusNotFound, "agent not found")
			return false
		}
		writeErrorResponse(c, http.StatusInternalServerError, "database error")
		return false
	}
	return true
}
