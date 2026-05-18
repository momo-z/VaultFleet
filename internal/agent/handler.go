package agent

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"vaultfleet/internal/agent/executor"
	"vaultfleet/internal/agent/filebrowse"
	"vaultfleet/internal/agent/policy"
	"vaultfleet/internal/agent/scheduler"
	"vaultfleet/pkg/protocol"
)

type SendFunc func(protocol.Message) error

type BrowseFunc func(fsRoot string, scanPath string, maxDepth int) ([]protocol.DirEntry, error)

type BackupRunnerFunc func(context.Context, executor.ExecutorConfig) executor.TaskResult

type policyScheduler interface {
	UpdateSchedule(agentID string, schedule string, fn func()) error
	RemoveJob(agentID string)
}

type HandlerConfig struct {
	PolicyStore  *policy.Store
	SendFunc     SendFunc
	BrowseFunc   BrowseFunc
	ConfigDir    string
	AgentID      string
	Scheduler    policyScheduler
	BackupRunner BackupRunnerFunc
}

type Handler struct {
	policyStore   *policy.Store
	send          SendFunc
	browse        BrowseFunc
	configDir     string
	agentID       string
	scheduler     policyScheduler
	backupRunner  BackupRunnerFunc
	backupMu      sync.Mutex
	backupRunning bool
}

func NewHandler(config HandlerConfig) *Handler {
	browse := config.BrowseFunc
	if browse == nil {
		browse = filebrowse.Browse
	}
	configDir := config.ConfigDir
	if configDir == "" {
		configDir = policy.DefaultDir
	}
	runner := config.BackupRunner
	if runner == nil {
		runner = runBackup
	}
	policyScheduler := config.Scheduler
	if policyScheduler == nil {
		defaultScheduler := scheduler.New()
		if err := defaultScheduler.Start(); err != nil {
			log.Printf("start scheduler failed: %v", err)
		}
		policyScheduler = defaultScheduler
	}
	return &Handler{
		policyStore:  config.PolicyStore,
		send:         config.SendFunc,
		browse:       browse,
		configDir:    configDir,
		agentID:      config.AgentID,
		scheduler:    policyScheduler,
		backupRunner: runner,
	}
}

func (h *Handler) Handle(msg protocol.Message) {
	switch msg.Type {
	case protocol.TypePolicyPush:
		h.handlePolicyPush(msg)
	case protocol.TypeBackupNow:
		h.handleBackupNow(msg)
	case protocol.TypeDirBrowseReq:
		h.handleDirBrowseReq(msg)
	}
}

func (h *Handler) handlePolicyPush(msg protocol.Message) {
	pushedPolicy, err := protocol.ParsePayload[protocol.PolicyPushPayload](&msg)
	if err != nil {
		log.Printf("parse policy push failed: %v", err)
		h.sendPolicyAck(msg.ID, "", false, err.Error())
		return
	}
	if h.policyStore == nil {
		h.sendPolicyAck(msg.ID, pushedPolicy.AgentID, false, "policy store not configured")
		return
	}

	if err := h.policyStore.SavePolicy(pushedPolicy); err != nil {
		log.Printf("save policy failed: %v", err)
		h.sendPolicyAck(msg.ID, pushedPolicy.AgentID, false, err.Error())
		return
	}
	if err := os.MkdirAll(h.configDir, 0o700); err != nil {
		log.Printf("create config dir failed: %v", err)
		h.sendPolicyAck(msg.ID, pushedPolicy.AgentID, false, err.Error())
		return
	}
	if err := executor.WriteRcloneConf(filepath.Join(h.configDir, "rclone.conf"), executor.RcloneConfig{
		Type:   pushedPolicy.Storage.RcloneType,
		Params: pushedPolicy.Storage.RcloneConfig,
	}); err != nil {
		log.Printf("write rclone config failed: %v", err)
		h.sendPolicyAck(msg.ID, pushedPolicy.AgentID, false, err.Error())
		return
	}
	if err := os.WriteFile(filepath.Join(h.configDir, ".restic-password"), []byte(pushedPolicy.ResticPassword), 0o600); err != nil {
		log.Printf("write restic password failed: %v", err)
		h.sendPolicyAck(msg.ID, pushedPolicy.AgentID, false, err.Error())
		return
	}
	if err := os.Chmod(filepath.Join(h.configDir, ".restic-password"), 0o600); err != nil {
		log.Printf("chmod restic password failed: %v", err)
		h.sendPolicyAck(msg.ID, pushedPolicy.AgentID, false, err.Error())
		return
	}
	if h.scheduler != nil {
		if pushedPolicy.Schedule == "" {
			h.scheduler.RemoveJob(pushedPolicy.AgentID)
		} else if err := h.scheduler.UpdateSchedule(pushedPolicy.AgentID, pushedPolicy.Schedule, func() {
			h.runBackupForPolicy(pushedPolicy.AgentID, pushedPolicy)
		}); err != nil {
			log.Printf("update backup schedule failed: %v", err)
			h.sendPolicyAck(msg.ID, pushedPolicy.AgentID, false, err.Error())
			return
		}
	}
	h.sendPolicyAck(msg.ID, pushedPolicy.AgentID, true, "")
}

func (h *Handler) handleBackupNow(msg protocol.Message) {
	backupNow, err := protocol.ParsePayload[protocol.BackupNowPayload](&msg)
	if err != nil {
		log.Printf("parse backup_now failed: %v", err)
		h.sendTaskResult(h.failedTaskResult(h.agentID, "parse backup_now: "+err.Error(), time.Now()))
		return
	}

	agentID := backupNow.AgentID
	if agentID == "" {
		agentID = h.agentID
	}
	if h.policyStore == nil {
		h.sendTaskResult(h.failedTaskResult(agentID, "policy store not configured", time.Now()))
		return
	}

	policyPayload, err := h.policyStore.LoadPolicy()
	if err != nil {
		log.Printf("load policy failed: %v", err)
		h.sendTaskResult(h.failedTaskResult(agentID, "load policy: "+err.Error(), time.Now()))
		return
	}
	if agentID == "" {
		agentID = policyPayload.AgentID
	}
	h.runBackupForPolicy(agentID, policyPayload)
}

func (h *Handler) runBackupForPolicy(agentID string, policyPayload *protocol.PolicyPushPayload) {
	if policyPayload == nil {
		return
	}
	if agentID == "" {
		agentID = policyPayload.AgentID
	}
	if !h.beginBackup() {
		h.sendTaskResult(h.failedTaskResult(agentID, "backup already running", time.Now()))
		return
	}
	defer h.endBackup()

	startedAt := time.Now()
	result := h.backupRunner(context.Background(), executor.ExecutorConfig{
		ConfigDir:  h.configDir,
		RepoPath:   policyPayload.Storage.RepoPath,
		BackupDirs: append([]string(nil), policyPayload.BackupDirs...),
		Excludes:   append([]string(nil), policyPayload.ExcludePatterns...),
		Retention:  toExecutorRetention(policyPayload.Retention),
	})
	h.sendTaskResult(result.ToProtocol(agentID, startedAt))
}

func (h *Handler) beginBackup() bool {
	h.backupMu.Lock()
	defer h.backupMu.Unlock()
	if h.backupRunning {
		return false
	}
	h.backupRunning = true
	return true
}

func (h *Handler) endBackup() {
	h.backupMu.Lock()
	defer h.backupMu.Unlock()
	h.backupRunning = false
}

func (h *Handler) sendPolicyAck(messageID string, agentID string, success bool, errorText string) {
	payload := protocol.PolicyAckPayload{
		AgentID: agentID,
		Success: success,
		Error:   errorText,
	}
	msg, err := protocol.NewMessage(protocol.TypePolicyAck, payload)
	if err != nil {
		log.Printf("create policy ack failed: %v", err)
		return
	}
	msg.ID = messageID
	h.sendMessage(*msg)
}

func (h *Handler) sendTaskResult(payload protocol.TaskResultPayload) {
	msg, err := protocol.NewMessage(protocol.TypeTaskResult, payload)
	if err != nil {
		log.Printf("create task result failed: %v", err)
		return
	}
	if err := h.sendMessage(*msg); err != nil {
		log.Printf("send task result failed: %v", err)
		h.persistPendingResult(payload)
	}
}

func (h *Handler) sendMessage(msg protocol.Message) error {
	if h.send == nil {
		return nil
	}
	return h.send(msg)
}

func (h *Handler) persistPendingResult(result protocol.TaskResultPayload) {
	if h.policyStore == nil {
		return
	}
	results, err := h.policyStore.LoadPendingResults()
	if err != nil {
		log.Printf("load pending results failed: %v", err)
		results = nil
	}
	results = append(results, result)
	if err := h.policyStore.SavePendingResults(results); err != nil {
		log.Printf("save pending result failed: %v", err)
	}
}

func (h *Handler) failedTaskResult(agentID string, errorText string, startedAt time.Time) protocol.TaskResultPayload {
	return executor.TaskResult{
		Type:       "backup",
		Status:     "failed",
		DurationMs: 0,
		ErrorLog:   errorText,
	}.ToProtocol(agentID, startedAt)
}

func toExecutorRetention(retention protocol.RetentionPolicy) executor.RetentionPolicy {
	return executor.RetentionPolicy{
		KeepLast:    retention.KeepLast,
		KeepDaily:   retention.KeepDaily,
		KeepWeekly:  retention.KeepWeekly,
		KeepMonthly: retention.KeepMonthly,
	}
}

func runBackup(ctx context.Context, cfg executor.ExecutorConfig) executor.TaskResult {
	return executor.NewExecutor(cfg).RunBackupJob(ctx)
}

func (h *Handler) handleDirBrowseReq(msg protocol.Message) {
	req, err := protocol.ParsePayload[protocol.DirBrowseReqPayload](&msg)
	if err != nil {
		log.Printf("parse directory browse request failed: %v", err)
		return
	}

	if req.Depth <= 0 || req.Depth > 3 {
		req.Depth = 2
	}

	entries, browseErr := h.browse("/", req.Path, req.Depth)
	payload := protocol.DirBrowseRespPayload{
		Path:    req.Path,
		Entries: entries,
	}
	if browseErr != nil {
		payload.Error = browseErr.Error()
		payload.Entries = nil
	}

	resp, err := protocol.NewMessage(protocol.TypeDirBrowseResp, payload)
	if err != nil {
		log.Printf("create directory browse response failed: %v", err)
		return
	}
	resp.ID = msg.ID

	if h.send == nil {
		return
	}
	if err := h.send(*resp); err != nil {
		log.Printf("send directory browse response failed: %v", err)
	}
}
