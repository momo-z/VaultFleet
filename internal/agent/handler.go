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
type RestoreRunnerFunc func(context.Context, executor.ExecutorConfig, string, string, []string) error
type SnapshotListRunnerFunc func(context.Context, executor.ExecutorConfig) ([]executor.SnapshotInfo, error)
type SnapshotBrowseRunnerFunc func(context.Context, executor.ExecutorConfig, string) ([]executor.SnapshotFileEntry, error)

type policyScheduler interface {
	Validate(schedule string) error
	UpdateSchedule(agentID string, schedule string, fn func()) error
	RemoveJob(agentID string)
}

type HandlerConfig struct {
	PolicyStore          *policy.Store
	SendFunc             SendFunc
	BrowseFunc           BrowseFunc
	ConfigDir            string
	AgentID              string
	LogFile              string
	Scheduler            policyScheduler
	BackupRunner         BackupRunnerFunc
	RestoreRunner        RestoreRunnerFunc
	SnapshotListRunner   SnapshotListRunnerFunc
	SnapshotBrowseRunner SnapshotBrowseRunnerFunc
}

type Handler struct {
	policyStore          *policy.Store
	send                 SendFunc
	browse               BrowseFunc
	configDir            string
	agentID              string
	logFile              string
	scheduler            policyScheduler
	backupRunner         BackupRunnerFunc
	restoreRunner        RestoreRunnerFunc
	snapshotListRunner   SnapshotListRunnerFunc
	snapshotBrowseRunner SnapshotBrowseRunnerFunc
	backupMu             sync.Mutex
	backupRunning        bool
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
	restoreRunner := config.RestoreRunner
	if restoreRunner == nil {
		restoreRunner = runRestore
	}
	snapshotListRunner := config.SnapshotListRunner
	if snapshotListRunner == nil {
		snapshotListRunner = runSnapshotList
	}
	snapshotBrowseRunner := config.SnapshotBrowseRunner
	if snapshotBrowseRunner == nil {
		snapshotBrowseRunner = runSnapshotBrowse
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
		policyStore:          config.PolicyStore,
		send:                 config.SendFunc,
		browse:               browse,
		configDir:            configDir,
		agentID:              config.AgentID,
		logFile:              config.LogFile,
		scheduler:            policyScheduler,
		backupRunner:         runner,
		restoreRunner:        restoreRunner,
		snapshotListRunner:   snapshotListRunner,
		snapshotBrowseRunner: snapshotBrowseRunner,
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
	case protocol.TypeRestoreReq:
		h.handleRestoreReq(msg)
	case protocol.TypeSnapshotListReq:
		h.handleSnapshotListReq(msg)
	case protocol.TypeSnapshotBrowseReq:
		h.handleSnapshotBrowseReq(msg)
	case protocol.TypeCollectLogsReq:
		h.handleCollectLogsReq(msg)
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

	if h.scheduler != nil && pushedPolicy.Schedule != "" {
		if err := h.scheduler.Validate(pushedPolicy.Schedule); err != nil {
			log.Printf("validate backup schedule failed: %v", err)
			h.sendPolicyAck(msg.ID, pushedPolicy.AgentID, false, err.Error())
			return
		}
	}

	rollbackState, err := h.snapshotPolicyState()
	if err != nil {
		log.Printf("snapshot policy state failed: %v", err)
		h.sendPolicyAck(msg.ID, pushedPolicy.AgentID, false, err.Error())
		return
	}
	defer rollbackState.cleanup()

	stagedFiles, err := h.stagePolicyFiles(pushedPolicy)
	if err != nil {
		log.Printf("stage policy config failed: %v", err)
		h.sendPolicyAck(msg.ID, pushedPolicy.AgentID, false, err.Error())
		return
	}
	defer stagedFiles.cleanup()

	if err := stagedFiles.commit(h.configDir); err != nil {
		log.Printf("commit policy config failed: %v", err)
		rollbackState.restoreConfig()
		h.sendPolicyAck(msg.ID, pushedPolicy.AgentID, false, err.Error())
		return
	}

	if err := h.policyStore.SavePolicy(pushedPolicy); err != nil {
		log.Printf("save policy failed: %v", err)
		rollbackState.restoreConfig()
		rollbackState.restorePolicy()
		h.sendPolicyAck(msg.ID, pushedPolicy.AgentID, false, err.Error())
		return
	}

	if h.scheduler != nil {
		if pushedPolicy.Schedule == "" {
			h.scheduler.RemoveJob(pushedPolicy.AgentID)
		} else if err := h.scheduler.UpdateSchedule(pushedPolicy.AgentID, pushedPolicy.Schedule, func() {
			h.runBackupForPolicy("", pushedPolicy.AgentID, pushedPolicy)
		}); err != nil {
			log.Printf("update backup schedule failed: %v", err)
			rollbackState.restore()
			h.sendPolicyAck(msg.ID, pushedPolicy.AgentID, false, err.Error())
			return
		}
	}
	h.sendPolicyAck(msg.ID, pushedPolicy.AgentID, true, "")
}

type stagedPolicyFiles struct {
	rclonePath   string
	passwordPath string
}

type policyRollbackState struct {
	policyStore *policy.Store
	oldPolicy   *protocol.PolicyPushPayload
	rclone      fileSnapshot
	password    fileSnapshot
}

type fileSnapshot struct {
	target  string
	backup  string
	existed bool
}

func (h *Handler) snapshotPolicyState() (*policyRollbackState, error) {
	state := &policyRollbackState{policyStore: h.policyStore}
	oldPolicy, err := h.policyStore.LoadPolicy()
	if err == nil {
		state.oldPolicy = oldPolicy
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	rcloneSnapshot, err := snapshotFile(filepath.Join(h.configDir, "rclone.conf"), h.configDir, ".rclone.conf.rollback.*")
	if err != nil {
		state.cleanup()
		return nil, err
	}
	state.rclone = rcloneSnapshot

	passwordSnapshot, err := snapshotFile(filepath.Join(h.configDir, ".restic-password"), h.configDir, ".restic-password.rollback.*")
	if err != nil {
		state.cleanup()
		return nil, err
	}
	state.password = passwordSnapshot
	return state, nil
}

func (h *Handler) stagePolicyFiles(pushedPolicy *protocol.PolicyPushPayload) (*stagedPolicyFiles, error) {
	if err := os.MkdirAll(h.configDir, 0o700); err != nil {
		return nil, err
	}

	rclonePath, err := createSecureTempPath(h.configDir, "rclone.conf.*")
	if err != nil {
		return nil, err
	}
	staged := &stagedPolicyFiles{rclonePath: rclonePath}
	if err := executor.WriteRcloneConf(rclonePath, executor.RcloneConfig{
		Type:   pushedPolicy.Storage.RcloneType,
		Params: pushedPolicy.Storage.RcloneConfig,
	}); err != nil {
		staged.cleanup()
		return nil, err
	}

	passwordPath, err := writeSecureTempFile(h.configDir, ".restic-password.*", []byte(pushedPolicy.ResticPassword))
	if err != nil {
		staged.cleanup()
		return nil, err
	}
	staged.passwordPath = passwordPath
	return staged, nil
}

func snapshotFile(target string, dir string, pattern string) (fileSnapshot, error) {
	snapshot := fileSnapshot{target: target}
	info, err := os.Stat(target)
	if os.IsNotExist(err) {
		return snapshot, nil
	}
	if err != nil {
		return snapshot, err
	}
	if info.IsDir() {
		return snapshot, &os.PathError{Op: "snapshot", Path: target, Err: os.ErrInvalid}
	}

	data, err := os.ReadFile(target)
	if err != nil {
		return snapshot, err
	}
	backupPath, err := writeSecureTempFile(dir, pattern, data)
	if err != nil {
		return snapshot, err
	}
	snapshot.backup = backupPath
	snapshot.existed = true
	return snapshot, nil
}

func (s *stagedPolicyFiles) commit(configDir string) error {
	rcloneTarget := filepath.Join(configDir, "rclone.conf")
	passwordTarget := filepath.Join(configDir, ".restic-password")
	if err := validateReplaceTarget(rcloneTarget); err != nil {
		return err
	}
	if err := validateReplaceTarget(passwordTarget); err != nil {
		return err
	}

	if err := os.Rename(s.rclonePath, rcloneTarget); err != nil {
		return err
	}
	s.rclonePath = ""
	if err := os.Rename(s.passwordPath, passwordTarget); err != nil {
		return err
	}
	s.passwordPath = ""
	return nil
}

func (s *stagedPolicyFiles) cleanup() {
	if s.rclonePath != "" {
		if err := os.Remove(s.rclonePath); err != nil && !os.IsNotExist(err) {
			log.Printf("remove staged rclone config failed: %v", err)
		}
	}
	if s.passwordPath != "" {
		if err := os.Remove(s.passwordPath); err != nil && !os.IsNotExist(err) {
			log.Printf("remove staged restic password failed: %v", err)
		}
	}
}

func (s *policyRollbackState) restore() {
	s.restoreConfig()
	s.restorePolicy()
}

func (s *policyRollbackState) restoreConfig() {
	s.rclone.restore()
	s.password.restore()
}

func (s *policyRollbackState) restorePolicy() {
	if s.oldPolicy == nil {
		if err := s.policyStore.DeletePolicy(); err != nil {
			log.Printf("remove new policy failed: %v", err)
		}
		return
	}
	if err := s.policyStore.SavePolicy(s.oldPolicy); err != nil {
		log.Printf("restore previous policy failed: %v", err)
	}
}

func (s *policyRollbackState) cleanup() {
	s.rclone.cleanup()
	s.password.cleanup()
}

func (s *fileSnapshot) restore() {
	if s.target == "" {
		return
	}
	if !s.existed {
		if err := os.Remove(s.target); err != nil && !os.IsNotExist(err) {
			log.Printf("remove new policy config file failed: %v", err)
		}
		return
	}
	if s.backup == "" {
		return
	}
	if err := os.Rename(s.backup, s.target); err != nil {
		log.Printf("restore previous policy config file failed: %v", err)
		return
	}
	s.backup = ""
}

func (s *fileSnapshot) cleanup() {
	if s.backup == "" {
		return
	}
	if err := os.Remove(s.backup); err != nil && !os.IsNotExist(err) {
		log.Printf("remove policy config rollback file failed: %v", err)
	}
	s.backup = ""
}

func createSecureTempPath(dir string, pattern string) (string, error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func writeSecureTempFile(dir string, pattern string, data []byte) (string, error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	path := file.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(path)
		}
	}()

	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return "", err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	remove = false
	return path, nil
}

func validateReplaceTarget(path string) error {
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		return &os.PathError{Op: "replace", Path: path, Err: os.ErrExist}
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (h *Handler) handleBackupNow(msg protocol.Message) {
	backupNow, err := protocol.ParsePayload[protocol.BackupNowPayload](&msg)
	if err != nil {
		log.Printf("parse backup_now failed: %v", err)
		h.sendTaskResultWithID(msg.ID, h.failedTaskResult(h.agentID, "parse backup_now: "+err.Error(), time.Now()))
		return
	}

	agentID := backupNow.AgentID
	if agentID == "" {
		agentID = h.agentID
	}
	if h.policyStore == nil {
		h.sendTaskResultWithID(msg.ID, h.failedTaskResult(agentID, "policy store not configured", time.Now()))
		return
	}

	policyPayload, err := h.policyStore.LoadPolicy()
	if err != nil {
		log.Printf("load policy failed: %v", err)
		h.sendTaskResultWithID(msg.ID, h.failedTaskResult(agentID, "load policy: "+err.Error(), time.Now()))
		return
	}
	if agentID == "" {
		agentID = policyPayload.AgentID
	}
	h.runBackupForPolicy(msg.ID, agentID, policyPayload)
}

func (h *Handler) runBackupForPolicy(messageID string, agentID string, policyPayload *protocol.PolicyPushPayload) {
	if policyPayload == nil {
		return
	}
	if agentID == "" {
		agentID = policyPayload.AgentID
	}
	if !h.beginBackup() {
		h.sendTaskResultWithID(messageID, h.failedTaskResult(agentID, "backup already running", time.Now()))
		return
	}
	defer h.endBackup()

	startedAt := time.Now()
	result := h.backupRunner(context.Background(), executorConfigForPolicy(h.configDir, policyPayload))
	h.sendTaskResultWithID(messageID, result.ToProtocol(agentID, startedAt))
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
	h.sendTaskResultWithID("", payload)
}

func (h *Handler) sendTaskResultWithID(messageID string, payload protocol.TaskResultPayload) {
	msg, err := protocol.NewMessage(protocol.TypeTaskResult, payload)
	if err != nil {
		log.Printf("create task result failed: %v", err)
		return
	}
	if messageID != "" {
		msg.ID = messageID
	}
	if err := h.sendMessage(*msg); err != nil {
		log.Printf("send task result failed: %v", err)
		h.persistPendingResult(messageID, payload)
	}
}

func (h *Handler) FlushPendingResults() {
	if h.policyStore == nil {
		return
	}
	results, err := h.policyStore.LoadPendingResults()
	if err != nil {
		log.Printf("load pending results failed: %v", err)
		return
	}
	if len(results) == 0 {
		return
	}

	remaining := make([]policy.PendingTaskResult, 0)
	for _, result := range results {
		msg, err := protocol.NewMessage(protocol.TypeTaskResult, result.Payload)
		if err != nil {
			log.Printf("create pending task result failed: %v", err)
			remaining = append(remaining, result)
			continue
		}
		if result.MessageID != "" {
			msg.ID = result.MessageID
		}
		if err := h.sendMessage(*msg); err != nil {
			log.Printf("send pending task result failed: %v", err)
			remaining = append(remaining, result)
		}
	}

	if len(remaining) == 0 {
		if err := h.policyStore.ClearPendingResults(); err != nil {
			log.Printf("clear pending results failed: %v", err)
		}
		return
	}
	if err := h.policyStore.SavePendingResults(remaining); err != nil {
		log.Printf("save remaining pending results failed: %v", err)
	}
}

func (h *Handler) sendMessage(msg protocol.Message) error {
	if h.send == nil {
		return nil
	}
	return h.send(msg)
}

func (h *Handler) persistPendingResult(messageID string, result protocol.TaskResultPayload) {
	if h.policyStore == nil {
		return
	}
	results, err := h.policyStore.LoadPendingResults()
	if err != nil {
		log.Printf("load pending results failed: %v", err)
		results = nil
	}
	results = append(results, policy.PendingTaskResult{MessageID: messageID, Payload: result})
	if err := h.policyStore.SavePendingResults(results); err != nil {
		log.Printf("save pending result failed: %v", err)
	}
}

func (h *Handler) failedTaskResult(agentID string, errorText string, startedAt time.Time) protocol.TaskResultPayload {
	return h.failedTypedTaskResult(agentID, "backup", "", errorText, startedAt)
}

func (h *Handler) failedTypedTaskResult(agentID string, taskType string, snapshotID string, errorText string, startedAt time.Time) protocol.TaskResultPayload {
	return executor.TaskResult{
		Type:       taskType,
		Status:     "failed",
		DurationMs: 0,
		SnapshotID: snapshotID,
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

func executorConfigForPolicy(configDir string, policyPayload *protocol.PolicyPushPayload) executor.ExecutorConfig {
	return executor.ExecutorConfig{
		ConfigDir:  configDir,
		RepoPath:   policyPayload.Storage.RepoPath,
		BackupDirs: append([]string(nil), policyPayload.BackupDirs...),
		Excludes:   append([]string(nil), policyPayload.ExcludePatterns...),
		Retention:  toExecutorRetention(policyPayload.Retention),
	}
}

func runBackup(ctx context.Context, cfg executor.ExecutorConfig) executor.TaskResult {
	return executor.NewExecutor(cfg).RunBackupJob(ctx)
}

func runRestore(ctx context.Context, cfg executor.ExecutorConfig, snapshotID string, target string, includePaths []string) error {
	runner := executor.ResticRunner{
		RcloneConfPath: filepath.Join(cfg.ConfigDir, "rclone.conf"),
		PasswordFile:   filepath.Join(cfg.ConfigDir, ".restic-password"),
		RepoPath:       cfg.RepoPath,
	}
	return runner.RestoreSnapshot(ctx, snapshotID, target, includePaths)
}

func runSnapshotList(ctx context.Context, cfg executor.ExecutorConfig) ([]executor.SnapshotInfo, error) {
	runner := executor.ResticRunner{
		RcloneConfPath: filepath.Join(cfg.ConfigDir, "rclone.conf"),
		PasswordFile:   filepath.Join(cfg.ConfigDir, ".restic-password"),
		RepoPath:       cfg.RepoPath,
	}
	return runner.ListSnapshots(ctx)
}

func runSnapshotBrowse(ctx context.Context, cfg executor.ExecutorConfig, snapshotID string) ([]executor.SnapshotFileEntry, error) {
	runner := executor.ResticRunner{
		RcloneConfPath: filepath.Join(cfg.ConfigDir, "rclone.conf"),
		PasswordFile:   filepath.Join(cfg.ConfigDir, ".restic-password"),
		RepoPath:       cfg.RepoPath,
	}
	return runner.LsSnapshot(ctx, snapshotID)
}

func (h *Handler) handleRestoreReq(msg protocol.Message) {
	req, err := protocol.ParsePayload[protocol.RestoreReqPayload](&msg)
	startedAt := time.Now()
	if err != nil {
		log.Printf("parse restore request failed: %v", err)
		h.sendTaskResultWithID(msg.ID, h.failedTypedTaskResult(h.agentID, "restore", "", "parse restore: "+err.Error(), startedAt))
		return
	}
	if h.policyStore == nil {
		h.sendTaskResultWithID(msg.ID, h.failedTypedTaskResult(h.agentID, "restore", req.SnapshotID, "policy store not configured", startedAt))
		return
	}

	policyPayload, err := h.policyStore.LoadPolicy()
	if err != nil {
		log.Printf("load policy failed: %v", err)
		h.sendTaskResultWithID(msg.ID, h.failedTypedTaskResult(h.agentID, "restore", req.SnapshotID, "load policy: "+err.Error(), startedAt))
		return
	}
	agentID := policyPayload.AgentID
	if agentID == "" {
		agentID = h.agentID
	}

	h.sendRestoreProgress(msg.ID, agentID, req.SnapshotID)
	err = h.restoreRunner(context.Background(), executorConfigForPolicy(h.configDir, policyPayload), req.SnapshotID, req.Target, req.IncludePaths)
	finishedAt := time.Now()
	result := protocol.TaskResultPayload{
		AgentID:    agentID,
		TaskType:   "restore",
		Status:     "success",
		SnapshotID: req.SnapshotID,
		DurationMs: finishedAt.Sub(startedAt).Milliseconds(),
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	}
	if err != nil {
		result.Status = "failed"
		result.ErrorLog = err.Error()
	}
	h.sendTaskResultWithID(msg.ID, result)
}

func (h *Handler) sendRestoreProgress(messageID string, agentID string, snapshotID string) {
	payload := protocol.RestoreProgressPayload{
		AgentID:    agentID,
		SnapshotID: snapshotID,
		Percent:    0,
	}
	msg, err := protocol.NewMessage(protocol.TypeRestoreProgress, payload)
	if err != nil {
		log.Printf("create restore progress failed: %v", err)
		return
	}
	msg.ID = messageID
	if err := h.sendMessage(*msg); err != nil {
		log.Printf("send restore progress failed: %v", err)
	}
}

func (h *Handler) handleSnapshotListReq(msg protocol.Message) {
	req, err := protocol.ParsePayload[protocol.SnapshotListReqPayload](&msg)
	agentID := h.agentID
	if err != nil {
		log.Printf("parse snapshot list request failed: %v", err)
		h.sendSnapshotListResp(msg.ID, agentID, nil, "parse snapshot list: "+err.Error())
		return
	}
	if req.AgentID != "" {
		agentID = req.AgentID
	}
	if h.policyStore == nil {
		h.sendSnapshotListResp(msg.ID, agentID, nil, "policy store not configured")
		return
	}

	policyPayload, err := h.policyStore.LoadPolicy()
	if err != nil {
		log.Printf("load policy failed: %v", err)
		h.sendSnapshotListResp(msg.ID, agentID, nil, "load policy: "+err.Error())
		return
	}
	if agentID == "" {
		agentID = policyPayload.AgentID
	}

	snapshots, err := h.snapshotListRunner(context.Background(), executorConfigForPolicy(h.configDir, policyPayload))
	if err != nil {
		h.sendSnapshotListResp(msg.ID, agentID, nil, err.Error())
		return
	}
	h.sendSnapshotListResp(msg.ID, agentID, snapshotsToProtocol(snapshots), "")
}

func (h *Handler) sendSnapshotListResp(messageID string, agentID string, snapshots []protocol.SnapshotInfo, errorText string) {
	payload := protocol.SnapshotListRespPayload{
		AgentID:   agentID,
		Snapshots: snapshots,
		Error:     errorText,
	}
	msg, err := protocol.NewMessage(protocol.TypeSnapshotListResp, payload)
	if err != nil {
		log.Printf("create snapshot list response failed: %v", err)
		return
	}
	msg.ID = messageID
	if err := h.sendMessage(*msg); err != nil {
		log.Printf("send snapshot list response failed: %v", err)
	}
}

func (h *Handler) handleSnapshotBrowseReq(msg protocol.Message) {
	req, err := protocol.ParsePayload[protocol.SnapshotBrowseReqPayload](&msg)
	if err != nil {
		log.Printf("parse snapshot browse request failed: %v", err)
		h.sendSnapshotBrowseResp(msg.ID, "", nil, "parse snapshot browse: "+err.Error())
		return
	}
	if h.policyStore == nil {
		h.sendSnapshotBrowseResp(msg.ID, req.SnapshotID, nil, "policy store not configured")
		return
	}

	policyPayload, err := h.policyStore.LoadPolicy()
	if err != nil {
		log.Printf("load policy failed: %v", err)
		h.sendSnapshotBrowseResp(msg.ID, req.SnapshotID, nil, "load policy: "+err.Error())
		return
	}

	entries, err := h.snapshotBrowseRunner(context.Background(), executorConfigForPolicy(h.configDir, policyPayload), req.SnapshotID)
	if err != nil {
		h.sendSnapshotBrowseResp(msg.ID, req.SnapshotID, nil, err.Error())
		return
	}

	protoEntries := make([]protocol.SnapshotFileEntry, len(entries))
	for i, entry := range entries {
		protoEntries[i] = protocol.SnapshotFileEntry{
			Path:  entry.Path,
			Type:  entry.Type,
			Size:  entry.Size,
			Mtime: entry.Mtime,
		}
	}
	h.sendSnapshotBrowseResp(msg.ID, req.SnapshotID, protoEntries, "")
}

func (h *Handler) sendSnapshotBrowseResp(messageID string, snapshotID string, entries []protocol.SnapshotFileEntry, errorText string) {
	payload := protocol.SnapshotBrowseRespPayload{
		SnapshotID: snapshotID,
		Entries:    entries,
		Error:      errorText,
	}
	msg, err := protocol.NewMessage(protocol.TypeSnapshotBrowseResp, payload)
	if err != nil {
		log.Printf("create snapshot browse response failed: %v", err)
		return
	}
	msg.ID = messageID
	if err := h.sendMessage(*msg); err != nil {
		log.Printf("send snapshot browse response failed: %v", err)
	}
}

func (h *Handler) handleCollectLogsReq(msg protocol.Message) {
	req, err := protocol.ParsePayload[protocol.CollectLogsReqPayload](&msg)
	maxBytes := 5 * 1024 * 1024
	if err == nil && req.MaxBytes > 0 && req.MaxBytes < maxBytes {
		maxBytes = req.MaxBytes
	}

	logs := ""
	errorText := ""
	if h.logFile != "" {
		logs, err = collectLogsFromFile(h.logFile, maxBytes)
	} else {
		logs, err = collectLogs(defaultLogFile, maxBytes)
	}
	if err != nil {
		errorText = err.Error()
	}
	resp, err := protocol.NewMessage(protocol.TypeCollectLogsResp, protocol.CollectLogsRespPayload{
		Logs:  logs,
		Error: errorText,
	})
	if err != nil {
		log.Printf("create collect_logs response failed: %v", err)
		return
	}
	resp.ID = msg.ID
	if err := h.sendMessage(*resp); err != nil {
		log.Printf("send collect_logs response failed: %v", err)
	}
}

func snapshotsToProtocol(snapshots []executor.SnapshotInfo) []protocol.SnapshotInfo {
	result := make([]protocol.SnapshotInfo, 0, len(snapshots))
	for _, snapshot := range snapshots {
		result = append(result, protocol.SnapshotInfo{
			ID:    snapshot.ID,
			Time:  snapshot.Time,
			Paths: append([]string(nil), snapshot.Paths...),
			Size:  snapshot.Size,
		})
	}
	return result
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
