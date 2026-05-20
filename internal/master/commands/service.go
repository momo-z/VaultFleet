package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
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

var dispatchMu sync.Mutex

type CreateCommandInput struct {
	AgentID         string
	Type            string
	Message         protocol.Message
	TaskType        string
	TaskState       string
	SnapshotID      string
	PolicyID        string
	PolicyUpdatedAt *time.Time
	StorageID       string
}

func NewService(database *db.Database, hub Hub) *Service {
	return &Service{DB: database, Hub: hub, Now: time.Now}
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
		AgentID:         input.AgentID,
		Type:            input.Type,
		Status:          CommandStatusPending,
		MessageID:       input.Message.ID,
		Payload:         encrypted,
		PolicyID:        input.PolicyID,
		PolicyUpdatedAt: input.PolicyUpdatedAt,
		StorageID:       input.StorageID,
		DeadlineAt:      &deadline,
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
	if err != nil {
		return db.AgentCommand{}, err
	}

	return command, nil
}

func (s *Service) DispatchPendingForAgent(ctx context.Context, agentID string, limit int) error {
	return s.dispatchForAgent(ctx, agentID, limit, []string{CommandStatusPending, CommandStatusDispatched})
}

func (s *Service) DispatchNewPendingForAgent(ctx context.Context, agentID string, limit int) error {
	return s.dispatchForAgent(ctx, agentID, limit, []string{CommandStatusPending})
}

func (s *Service) dispatchForAgent(ctx context.Context, agentID string, limit int, statuses []string) error {
	if s == nil || s.DB == nil || s.DB.DB == nil || s.Hub == nil || agentID == "" {
		return nil
	}
	if !s.Hub.IsOnline(agentID) {
		return nil
	}
	if limit <= 0 {
		limit = 100
	}

	dispatchMu.Lock()
	defer dispatchMu.Unlock()

	now := s.now()
	var commands []db.AgentCommand
	err := s.DB.DB.WithContext(ctx).
		Where("agent_id = ? AND status IN ? AND (deadline_at IS NULL OR deadline_at > ?)",
			agentID,
			statuses,
			now,
		).
		Order("created_at ASC").
		Limit(limit).
		Find(&commands).Error
	if err != nil {
		return err
	}

	for _, command := range commands {
		if err := s.dispatch(ctx, command); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) CompletePolicyAck(ctx context.Context, agentID string, messageID string, success bool, errorText string) error {
	if s == nil || s.DB == nil || s.DB.DB == nil || agentID == "" || messageID == "" {
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
	if success {
		updates["error_message"] = ""
	} else {
		updates["error_message"] = errorText
	}

	return s.DB.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&db.AgentCommand{}).
			Where(
				"agent_id = ? AND message_id = ? AND type = ? AND status IN ?",
				agentID,
				messageID,
				protocol.TypePolicyPush,
				[]string{CommandStatusPending, CommandStatusDispatched, CommandStatusRunning},
			).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			return nil
		}

		var command db.AgentCommand
		err := tx.First(&command, "agent_id = ? AND message_id = ? AND type = ?", agentID, messageID, protocol.TypePolicyPush).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("policy command not found: %s", messageID)
		}
		if err != nil {
			return err
		}
		if isTerminal(command.Status) {
			return nil
		}
		return fmt.Errorf("policy command %s is not active: %s", messageID, command.Status)
	})
}

func (s *Service) CompleteTaskResult(ctx context.Context, agentID string, messageID string, result protocol.TaskResultPayload) error {
	if s == nil || s.DB == nil || s.DB.DB == nil || messageID == "" {
		return nil
	}
	if agentID == "" {
		agentID = result.AgentID
	}

	rawResult, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal command result: %w", err)
	}
	now := s.now()
	commandStatus := CommandStatusFailed
	if result.Status == TaskStatusSuccess {
		commandStatus = CommandStatusSucceeded
	}

	errorMessage := result.ErrorLog
	if commandStatus == CommandStatusSucceeded {
		errorMessage = ""
	}
	startedAt := nullableTime(result.StartedAt)
	finishedAt := nullableTime(result.FinishedAt)
	if finishedAt == nil && isTaskTerminal(result.Status) {
		finishedAt = &now
	}

	return s.DB.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var command db.AgentCommand
		err := tx.
			Where("agent_id = ? AND message_id = ? AND status NOT IN ?", agentID, messageID, terminalStatuses()).
			First(&command).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}

		update := tx.Model(&db.AgentCommand{}).
			Where("id = ? AND status NOT IN ?", command.ID, terminalStatuses()).
			Updates(map[string]any{
				"status":        commandStatus,
				"result":        string(rawResult),
				"error_message": errorMessage,
				"completed_at":  &now,
				"updated_at":    now,
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected == 0 {
			return nil
		}

		taskUpdates := map[string]any{
			"status":      result.Status,
			"snapshot_id": result.SnapshotID,
			"duration_ms": result.DurationMs,
			"repo_size":   result.RepoSize,
			"error_log":   result.ErrorLog,
			"finished_at": finishedAt,
			"updated_at":  now,
		}
		if startedAt != nil {
			taskUpdates["started_at"] = startedAt
		}
		return tx.Model(&db.TaskHistory{}).
			Where("command_id = ?", command.ID).
			Updates(taskUpdates).Error
	})
}

func (s *Service) CompleteSnapshotList(ctx context.Context, agentID string, messageID string, result protocol.SnapshotListRespPayload) error {
	return s.completeCommand(ctx, agentID, messageID, CommandStatusSucceeded, result, "")
}

func (s *Service) FailCommand(ctx context.Context, agentID string, messageID string, errorText string) error {
	return s.completeCommand(ctx, agentID, messageID, CommandStatusFailed, nil, errorText)
}

func (s *Service) TimeoutCommand(ctx context.Context, agentID string, messageID string) error {
	return s.completeCommand(ctx, agentID, messageID, CommandStatusTimeout, nil, "command timeout")
}

func (s *Service) completeCommand(ctx context.Context, agentID string, messageID string, status string, result any, errorText string) error {
	if s == nil || s.DB == nil || s.DB.DB == nil || messageID == "" {
		return nil
	}

	now := s.now()
	updates := map[string]any{
		"status":        status,
		"error_message": errorText,
		"completed_at":  &now,
		"updated_at":    now,
	}
	if result != nil {
		rawResult, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("marshal command result: %w", err)
		}
		updates["result"] = string(rawResult)
	}

	query := s.DB.DB.WithContext(ctx).Model(&db.AgentCommand{}).
		Where("message_id = ? AND status NOT IN ?", messageID, terminalStatuses())
	if agentID != "" {
		query = query.Where("agent_id = ?", agentID)
	}
	return query.Updates(updates).Error
}

func (s *Service) TimeoutExpired(ctx context.Context) (int64, error) {
	if s == nil || s.DB == nil || s.DB.DB == nil {
		return 0, nil
	}

	now := s.now()
	var expired []db.AgentCommand
	if err := s.DB.DB.WithContext(ctx).
		Where("status IN ? AND deadline_at IS NOT NULL AND deadline_at <= ?",
			[]string{CommandStatusPending, CommandStatusDispatched, CommandStatusRunning},
			now,
		).
		Find(&expired).Error; err != nil {
		return 0, err
	}

	var count int64
	for _, command := range expired {
		updated, err := s.timeoutCommandAndTask(ctx, command, now)
		if err != nil {
			return count, err
		}
		count += updated
	}
	return count, nil
}

func (s *Service) timeoutCommandAndTask(ctx context.Context, command db.AgentCommand, now time.Time) (int64, error) {
	var rows int64
	err := s.DB.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		update := tx.Model(&db.AgentCommand{}).
			Where("id = ? AND status IN ?", command.ID, []string{CommandStatusPending, CommandStatusDispatched, CommandStatusRunning}).
			Updates(map[string]any{
				"status":        CommandStatusTimeout,
				"error_message": "command timeout",
				"completed_at":  &now,
				"updated_at":    now,
			})
		if update.Error != nil {
			return update.Error
		}
		rows = update.RowsAffected
		if rows == 0 {
			return nil
		}
		return tx.Model(&db.TaskHistory{}).
			Where("command_id = ? AND status IN ?", command.ID, []string{TaskStatusPending, TaskStatusRunning}).
			Updates(map[string]any{
				"status":      TaskStatusTimeout,
				"error_log":   "command timeout",
				"finished_at": &now,
				"updated_at":  now,
			}).Error
	})
	return rows, err
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

func (s *Service) messageFromCommand(command db.AgentCommand) (protocol.Message, error) {
	if s == nil || s.DB == nil {
		return protocol.Message{}, errors.New("command service database not configured")
	}
	plaintext, err := db.Decrypt(command.Payload, s.DB.MasterKey)
	if err != nil {
		return protocol.Message{}, fmt.Errorf("decrypt command payload: %w", err)
	}
	var message protocol.Message
	if err := json.Unmarshal([]byte(plaintext), &message); err != nil {
		return protocol.Message{}, fmt.Errorf("unmarshal command payload: %w", err)
	}
	return message, nil
}

func (s *Service) dispatch(ctx context.Context, command db.AgentCommand) error {
	message, err := s.messageFromCommand(command)
	if err != nil {
		return err
	}
	if err := s.Hub.Send(command.AgentID, message); err != nil {
		return s.recordDispatchFailure(ctx, command, err)
	}

	now := s.now()
	status := CommandStatusDispatched
	if isLongRunning(command.Type) {
		status = CommandStatusRunning
	}

	return s.DB.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&db.AgentCommand{}).
			Where("id = ? AND status NOT IN ?", command.ID, terminalStatuses()).
			Updates(map[string]any{
				"attempts":      gorm.Expr("attempts + ?", 1),
				"dispatched_at": &now,
				"error_message": "",
				"status":        status,
			}).Error; err != nil {
			return err
		}
		if !isLongRunning(command.Type) {
			return nil
		}
		return tx.Model(&db.TaskHistory{}).
			Where("command_id = ? AND status = ?", command.ID, TaskStatusPending).
			Update("status", TaskStatusRunning).Error
	})
}

func (s *Service) recordDispatchFailure(ctx context.Context, command db.AgentCommand, dispatchErr error) error {
	if s == nil || s.DB == nil || s.DB.DB == nil {
		return nil
	}
	return s.DB.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&db.AgentCommand{}).
			Where("id = ? AND status NOT IN ?", command.ID, terminalStatuses()).
			Updates(map[string]any{
				"attempts":      gorm.Expr("attempts + ?", 1),
				"dispatched_at": nil,
				"error_message": dispatchErr.Error(),
				"status":        CommandStatusPending,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		if !isLongRunning(command.Type) {
			return nil
		}
		return tx.Model(&db.TaskHistory{}).
			Where("command_id = ? AND status = ?", command.ID, TaskStatusRunning).
			Update("status", TaskStatusPending).Error
	})
}

func (s *Service) now() time.Time {
	if s == nil || s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func isLongRunning(commandType string) bool {
	return commandType == protocol.TypeBackupNow || commandType == protocol.TypeRestoreReq
}

func isTerminal(status string) bool {
	return status == CommandStatusSucceeded || status == CommandStatusFailed || status == CommandStatusTimeout
}

func terminalStatuses() []string {
	return []string{CommandStatusSucceeded, CommandStatusFailed, CommandStatusTimeout}
}

func isTaskTerminal(status string) bool {
	return status == TaskStatusSuccess || status == TaskStatusFailed || status == TaskStatusTimeout
}

func nullableTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}
