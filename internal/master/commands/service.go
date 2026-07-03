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
	CommandStatusCancelled  = "cancelled"

	TaskStatusPending   = "pending"
	TaskStatusRunning   = "running"
	TaskStatusSuccess   = "success"
	TaskStatusFailed    = "failed"
	TaskStatusTimeout   = "timeout"
	TaskStatusCancelled = "cancelled"
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

var errCommandNotActive = errors.New("command is not active")

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
	TimeoutHours    int
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
	case protocol.TypeBackupNow, protocol.TypeRestoreReq, protocol.TypeSelectiveRestoreReq:
		return now.Add(6 * time.Hour)
	case protocol.TypeMaintenance:
		return now.Add(2 * time.Hour)
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

	now := s.now()
	deadline := DeadlineForType(input.Type, now)
	if input.TimeoutHours > 0 {
		deadline = now.Add(time.Duration(input.TimeoutHours) * time.Hour)
	}
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
	_, err := s.CompleteTaskResultWith(ctx, agentID, messageID, result, nil)
	return err
}

func (s *Service) CompleteTaskResultWith(ctx context.Context, agentID string, messageID string, result protocol.TaskResultPayload, apply func(*gorm.DB) error) (bool, error) {
	if s == nil || s.DB == nil || s.DB.DB == nil || messageID == "" {
		return false, nil
	}
	if agentID == "" {
		agentID = result.AgentID
	}

	rawResult, err := json.Marshal(result)
	if err != nil {
		return false, fmt.Errorf("marshal command result: %w", err)
	}
	now := s.now()
	commandStatus := CommandStatusFailed
	switch result.Status {
	case TaskStatusSuccess:
		commandStatus = CommandStatusSucceeded
	case TaskStatusCancelled:
		commandStatus = CommandStatusCancelled
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

	completed := false
	err = s.DB.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var command db.AgentCommand
		err := tx.
			Where(
				"agent_id = ? AND message_id = ? AND type IN ? AND status NOT IN ?",
				agentID,
				messageID,
				[]string{protocol.TypeBackupNow, protocol.TypeRestoreReq, protocol.TypeSelectiveRestoreReq, protocol.TypeMaintenance},
				terminalStatuses(),
			).
			First(&command).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}

		expectedTaskType, ok := taskTypeForCommand(command.Type)
		if !ok || result.TaskType != expectedTaskType {
			return nil
		}

		if apply != nil {
			if err := apply(tx); err != nil {
				return err
			}
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
			return errCommandNotActive
		}

		taskUpdates := map[string]any{
			"status":      result.Status,
			"snapshot_id": result.SnapshotID,
			"duration_ms": result.DurationMs,
			"repo_size":   result.RepoSize,
			"error_log":   result.ErrorLog,
			"output":      result.Output,
			"finished_at": finishedAt,
			"updated_at":  now,
		}
		if startedAt != nil {
			taskUpdates["started_at"] = startedAt
		}
		if err := tx.Model(&db.TaskHistory{}).
			Where("command_id = ?", command.ID).
			Updates(taskUpdates).Error; err != nil {
			return err
		}
		completed = true
		return nil
	})
	if errors.Is(err, errCommandNotActive) {
		return false, nil
	}
	return completed, err
}

func (s *Service) CompleteSnapshotList(ctx context.Context, agentID string, messageID string, result protocol.SnapshotListRespPayload) error {
	_, err := s.CompleteSnapshotListWith(ctx, agentID, messageID, result, nil)
	return err
}

func (s *Service) CompleteSnapshotListWith(ctx context.Context, agentID string, messageID string, result protocol.SnapshotListRespPayload, apply func(*gorm.DB) error) (bool, error) {
	if s == nil || s.DB == nil || s.DB.DB == nil || messageID == "" {
		return false, nil
	}
	if agentID == "" {
		agentID = result.AgentID
	}

	rawResult, err := json.Marshal(result)
	if err != nil {
		return false, fmt.Errorf("marshal command result: %w", err)
	}
	now := s.now()

	completed := false
	err = s.DB.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var command db.AgentCommand
		err := tx.
			Where(
				"agent_id = ? AND message_id = ? AND type = ? AND status NOT IN ?",
				agentID,
				messageID,
				protocol.TypeSnapshotListReq,
				terminalStatuses(),
			).
			First(&command).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}

		if apply != nil {
			if err := apply(tx); err != nil {
				return err
			}
		}

		update := tx.Model(&db.AgentCommand{}).
			Where("id = ? AND status NOT IN ?", command.ID, terminalStatuses()).
			Updates(map[string]any{
				"status":        CommandStatusSucceeded,
				"result":        string(rawResult),
				"error_message": "",
				"completed_at":  &now,
				"updated_at":    now,
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected == 0 {
			return errCommandNotActive
		}
		completed = true
		return nil
	})
	if errors.Is(err, errCommandNotActive) {
		return false, nil
	}
	return completed, err
}

func (s *Service) FailCommand(ctx context.Context, agentID string, messageID string, errorText string) error {
	return s.completeCommand(ctx, agentID, messageID, "", CommandStatusFailed, nil, errorText)
}

func (s *Service) FailCommandOfType(ctx context.Context, agentID string, messageID string, commandType string, errorText string) error {
	return s.completeCommand(ctx, agentID, messageID, commandType, CommandStatusFailed, nil, errorText)
}

func (s *Service) TimeoutCommand(ctx context.Context, agentID string, messageID string) error {
	return s.completeCommand(ctx, agentID, messageID, "", CommandStatusTimeout, nil, "command timeout")
}

type CancelCommandResult struct {
	NeedsWS   bool
	AgentID   string
	MessageID string
}

func (s *Service) CancelCommand(ctx context.Context, commandID string) (CancelCommandResult, error) {
	if s == nil || s.DB == nil || s.DB.DB == nil {
		return CancelCommandResult{}, errors.New("command service database not configured")
	}

	var command db.AgentCommand
	if err := s.DB.DB.WithContext(ctx).First(&command, "id = ?", commandID).Error; err != nil {
		return CancelCommandResult{}, err
	}
	if isTerminal(command.Status) {
		return CancelCommandResult{}, errors.New("command already in terminal state")
	}

	if command.Status != CommandStatusPending {
		return CancelCommandResult{
			NeedsWS:   true,
			AgentID:   command.AgentID,
			MessageID: command.MessageID,
		}, nil
	}

	now := s.now()
	err := s.DB.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&db.AgentCommand{}).
			Where("id = ? AND status = ?", command.ID, CommandStatusPending).
			Updates(map[string]any{
				"status":        CommandStatusCancelled,
				"error_message": "cancelled by user",
				"completed_at":  &now,
				"updated_at":    now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errCommandNotActive
		}
		return tx.Model(&db.TaskHistory{}).
			Where("command_id = ? AND status IN ?", command.ID, []string{TaskStatusPending, TaskStatusRunning}).
			Updates(map[string]any{
				"status":      TaskStatusCancelled,
				"error_log":   "cancelled by user",
				"finished_at": &now,
				"updated_at":  now,
			}).Error
	})
	if errors.Is(err, errCommandNotActive) {
		return CancelCommandResult{}, errors.New("command is not active")
	}
	return CancelCommandResult{NeedsWS: false}, err
}

func (s *Service) completeCommand(ctx context.Context, agentID string, messageID string, commandType string, status string, result any, errorText string) error {
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
	if commandType != "" {
		query = query.Where("type = ?", commandType)
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
		if updated > 0 {
			s.sendTimeoutCancel(command)
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

func (s *Service) sendTimeoutCancel(command db.AgentCommand) {
	if s == nil || s.Hub == nil || command.AgentID == "" || command.MessageID == "" {
		return
	}
	if command.Status != CommandStatusDispatched && command.Status != CommandStatusRunning {
		return
	}
	if !isCancelableOnTimeout(command.Type) || !s.Hub.IsOnline(command.AgentID) {
		return
	}

	cancelMsg, err := protocol.NewMessage(protocol.TypeCancelTask, protocol.CancelTaskPayload{
		AgentID:   command.AgentID,
		MessageID: command.MessageID,
	})
	if err != nil {
		return
	}
	_ = s.Hub.Send(command.AgentID, *cancelMsg)
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

func (s *Service) prepareMessageForDispatch(ctx context.Context, command db.AgentCommand) (protocol.Message, error) {
	message, err := s.messageFromCommand(command)
	if err != nil {
		return protocol.Message{}, err
	}
	if command.Type != protocol.TypeRestoreReq || message.Type != protocol.TypeRestoreReq {
		return message, nil
	}

	payload, err := protocol.ParsePayload[protocol.RestoreReqPayload](&message)
	if err != nil {
		return protocol.Message{}, fmt.Errorf("parse restore command payload: %w", err)
	}
	if len(payload.IncludePaths) == 0 {
		return message, nil
	}

	supportsSelectiveRestore, err := s.agentHasCapability(ctx, command.AgentID, protocol.CapabilityRestoreIncludePaths)
	if err != nil {
		return protocol.Message{}, err
	}
	if !supportsSelectiveRestore {
		return protocol.Message{}, errSelectiveRestoreUnsupported
	}

	message.Type = protocol.TypeSelectiveRestoreReq
	if err := s.persistPreparedMessage(ctx, command, message, protocol.TypeSelectiveRestoreReq); err != nil {
		return protocol.Message{}, err
	}
	return message, nil
}

var errSelectiveRestoreUnsupported = errors.New("agent does not support selective restore")
var errAgentCapabilitiesUnknown = errors.New("agent capabilities unknown")

func (s *Service) agentHasCapability(ctx context.Context, agentID string, capability string) (bool, error) {
	if s == nil || s.DB == nil || s.DB.DB == nil || agentID == "" || capability == "" {
		return false, errAgentCapabilitiesUnknown
	}
	var agent db.Agent
	if err := s.DB.DB.WithContext(ctx).Select("system_info").First(&agent, "id = ?", agentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, errAgentCapabilitiesUnknown
		}
		return false, err
	}
	var info map[string]json.RawMessage
	if err := json.Unmarshal([]byte(agent.SystemInfo), &info); err != nil {
		return false, errAgentCapabilitiesUnknown
	}
	rawCapabilities, ok := info["capabilities"]
	if !ok {
		return false, errAgentCapabilitiesUnknown
	}
	var capabilities []string
	if err := json.Unmarshal(rawCapabilities, &capabilities); err != nil {
		return false, errAgentCapabilitiesUnknown
	}
	for _, supported := range capabilities {
		if supported == capability {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) persistPreparedMessage(ctx context.Context, command db.AgentCommand, message protocol.Message, commandType string) error {
	raw, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("marshal prepared command payload: %w", err)
	}
	encrypted, err := db.Encrypt(string(raw), s.DB.MasterKey)
	if err != nil {
		return fmt.Errorf("encrypt prepared command payload: %w", err)
	}
	return s.DB.DB.WithContext(ctx).Model(&db.AgentCommand{}).
		Where("id = ? AND status NOT IN ?", command.ID, terminalStatuses()).
		Updates(map[string]any{
			"type":       commandType,
			"payload":    encrypted,
			"updated_at": s.now(),
		}).Error
}

func (s *Service) dispatch(ctx context.Context, command db.AgentCommand) error {
	message, err := s.prepareMessageForDispatch(ctx, command)
	if err != nil {
		if errors.Is(err, errAgentCapabilitiesUnknown) {
			return nil
		}
		if errors.Is(err, errSelectiveRestoreUnsupported) {
			return s.failCommandAndTask(ctx, command, err.Error())
		}
		return err
	}
	dispatched, err := s.recordDispatchSuccessForCommand(ctx, command)
	if err != nil || !dispatched {
		return err
	}
	if err := s.Hub.Send(command.AgentID, message); err != nil {
		return s.rollbackDispatchFailure(ctx, command, err)
	}
	return nil
}

func (s *Service) failCommandAndTask(ctx context.Context, command db.AgentCommand, errorText string) error {
	if s == nil || s.DB == nil || s.DB.DB == nil {
		return nil
	}
	now := s.now()
	return s.DB.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&db.AgentCommand{}).
			Where("id = ? AND status NOT IN ?", command.ID, terminalStatuses()).
			Updates(map[string]any{
				"status":        CommandStatusFailed,
				"error_message": errorText,
				"completed_at":  &now,
				"updated_at":    now,
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
			Where("command_id = ? AND status IN ?", command.ID, []string{TaskStatusPending, TaskStatusRunning}).
			Updates(map[string]any{
				"status":      TaskStatusFailed,
				"error_log":   errorText,
				"finished_at": &now,
				"updated_at":  now,
			}).Error
	})
}

func (s *Service) RecordDispatchSuccess(ctx context.Context, commandID string) error {
	command, ok, err := s.findCommandByID(ctx, commandID)
	if err != nil || !ok {
		return err
	}
	_, err = s.recordDispatchSuccessForCommand(ctx, command)
	return err
}

func (s *Service) RecordDispatchFailure(ctx context.Context, commandID string, dispatchErr error) error {
	command, ok, err := s.findCommandByID(ctx, commandID)
	if err != nil || !ok {
		return err
	}
	return s.recordDispatchFailure(ctx, command, dispatchErr, true)
}

func (s *Service) findCommandByID(ctx context.Context, commandID string) (db.AgentCommand, bool, error) {
	if s == nil || s.DB == nil || s.DB.DB == nil || commandID == "" {
		return db.AgentCommand{}, false, nil
	}
	var command db.AgentCommand
	err := s.DB.DB.WithContext(ctx).First(&command, "id = ?", commandID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return db.AgentCommand{}, false, nil
	}
	if err != nil {
		return db.AgentCommand{}, false, err
	}
	return command, true, nil
}

func (s *Service) recordDispatchSuccessForCommand(ctx context.Context, command db.AgentCommand) (bool, error) {
	if s == nil || s.DB == nil || s.DB.DB == nil {
		return false, nil
	}
	now := s.now()
	status := CommandStatusDispatched
	if isLongRunning(command.Type) {
		status = CommandStatusRunning
	}
	var rows int64
	err := s.DB.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&db.AgentCommand{}).
			Where("id = ? AND status NOT IN ?", command.ID, terminalStatuses()).
			Updates(map[string]any{
				"attempts":      gorm.Expr("attempts + ?", 1),
				"dispatched_at": &now,
				"error_message": "",
				"status":        status,
				"updated_at":    now,
			})
		if result.Error != nil {
			return result.Error
		}
		rows = result.RowsAffected
		if rows == 0 {
			return nil
		}
		if !isLongRunning(command.Type) {
			return nil
		}
		return tx.Model(&db.TaskHistory{}).
			Where("command_id = ? AND status = ?", command.ID, TaskStatusPending).
			Updates(map[string]any{
				"status":     TaskStatusRunning,
				"started_at": &now,
				"updated_at": now,
			}).Error
	})
	return rows > 0, err
}

func (s *Service) rollbackDispatchFailure(ctx context.Context, command db.AgentCommand, dispatchErr error) error {
	return s.recordDispatchFailure(ctx, command, dispatchErr, false)
}

func (s *Service) recordDispatchFailure(ctx context.Context, command db.AgentCommand, dispatchErr error, incrementAttempt bool) error {
	if s == nil || s.DB == nil || s.DB.DB == nil {
		return nil
	}
	now := s.now()
	updates := map[string]any{
		"dispatched_at": nil,
		"error_message": dispatchErr.Error(),
		"status":        CommandStatusPending,
		"updated_at":    now,
	}
	if incrementAttempt {
		updates["attempts"] = gorm.Expr("attempts + ?", 1)
	}
	return s.DB.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&db.AgentCommand{}).
			Where("id = ? AND status NOT IN ?", command.ID, terminalStatuses()).
			Updates(updates)
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
			Updates(map[string]any{
				"status":     TaskStatusPending,
				"started_at": nil,
				"updated_at": now,
			}).Error
	})
}

func (s *Service) now() time.Time {
	if s == nil || s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func isLongRunning(commandType string) bool {
	return commandType == protocol.TypeBackupNow || commandType == protocol.TypeRestoreReq || commandType == protocol.TypeSelectiveRestoreReq
}

func isCancelableOnTimeout(commandType string) bool {
	switch commandType {
	case protocol.TypeBackupNow, protocol.TypeRestoreReq, protocol.TypeSelectiveRestoreReq, protocol.TypeSnapshotListReq, protocol.TypeSnapshotBrowseReq:
		return true
	default:
		return false
	}
}

func taskTypeForCommand(commandType string) (string, bool) {
	switch commandType {
	case protocol.TypeBackupNow:
		return "backup", true
	case protocol.TypeRestoreReq, protocol.TypeSelectiveRestoreReq:
		return "restore", true
	case protocol.TypeMaintenance:
		return "maintenance", true
	default:
		return "", false
	}
}

func isTerminal(status string) bool {
	return status == CommandStatusSucceeded || status == CommandStatusFailed || status == CommandStatusTimeout || status == CommandStatusCancelled
}

func terminalStatuses() []string {
	return []string{CommandStatusSucceeded, CommandStatusFailed, CommandStatusTimeout, CommandStatusCancelled}
}

func isTaskTerminal(status string) bool {
	return status == TaskStatusSuccess || status == TaskStatusFailed || status == TaskStatusTimeout || status == TaskStatusCancelled
}

func nullableTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}
