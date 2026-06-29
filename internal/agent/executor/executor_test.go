package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vaultfleet/pkg/protocol"
)

func TestNewExecutorBuildsRunnerAndCopiesConfig(t *testing.T) {
	cfgDir := t.TempDir()
	os.MkdirAll(cfgDir, 0o700)
	os.WriteFile(filepath.Join(cfgDir, ".restic-password"), []byte("secret"), 0o600)

	cfg := ExecutorConfig{
		ConfigDir:  cfgDir,
		RepoPath:   "repo/agent-1",
		BackupDirs: []string{"/home/alice", "/etc"},
		Excludes:   []string{"*.tmp"},
		Retention: RetentionPolicy{
			KeepLast:  3,
			KeepDaily: 7,
		},
	}

	executor := NewExecutor(cfg)

	if executor.restic == nil {
		t.Fatal("NewExecutor() restic runner is nil")
	}
	runner, ok := executor.restic.(ResticRunner)
	if !ok {
		t.Fatalf("NewExecutor() restic runner type = %T, want ResticRunner", executor.restic)
	}
	if runner.RcloneConfPath != filepath.Join(cfg.ConfigDir, "rclone.conf") {
		t.Fatalf("RcloneConfPath = %q", runner.RcloneConfPath)
	}
	if runner.PasswordFile != filepath.Join(cfg.ConfigDir, ".restic-password") {
		t.Fatalf("PasswordFile = %q", runner.PasswordFile)
	}
	if runner.RepoPath != cfg.RepoPath {
		t.Fatalf("RepoPath = %q, want %q", runner.RepoPath, cfg.RepoPath)
	}

	cfg.BackupDirs[0] = "/mutated"
	cfg.Excludes[0] = "mutated"
	if executor.backupDirs[0] != "/home/alice" {
		t.Fatalf("backup dirs were not copied: %#v", executor.backupDirs)
	}
	if executor.excludes[0] != "*.tmp" {
		t.Fatalf("excludes were not copied: %#v", executor.excludes)
	}
	if executor.retention.KeepLast != 3 || executor.retention.KeepDaily != 7 {
		t.Fatalf("retention = %+v", executor.retention)
	}
}

func TestNewExecutorPassesRcloneArgsToRunner(t *testing.T) {
	cfgDir := t.TempDir()
	os.MkdirAll(cfgDir, 0o700)
	os.WriteFile(filepath.Join(cfgDir, ".restic-password"), []byte("secret"), 0o600)

	cfg := ExecutorConfig{
		ConfigDir: cfgDir,
		RepoPath:  "repo/agent-1",
		RcloneArgs: map[string]string{
			"transfers": "2",
			"tpslimit":  "4",
		},
	}

	executor := NewExecutor(cfg)

	runner, ok := executor.restic.(ResticRunner)
	if !ok {
		t.Fatalf("NewExecutor() restic runner type = %T, want ResticRunner", executor.restic)
	}
	if runner.RcloneExtraArgs["transfers"] != "2" {
		t.Fatalf("RcloneExtraArgs[transfers] = %q, want 2", runner.RcloneExtraArgs["transfers"])
	}
	if runner.RcloneExtraArgs["tpslimit"] != "4" {
		t.Fatalf("RcloneExtraArgs[tpslimit] = %q, want 4", runner.RcloneExtraArgs["tpslimit"])
	}

	cfg.RcloneArgs["transfers"] = "99"
	if runner.RcloneExtraArgs["transfers"] != "2" {
		t.Fatalf("RcloneExtraArgs were not copied: %#v", runner.RcloneExtraArgs)
	}
}

func TestNewExecutorUsesPlainRunnerWhenNoPasswordFile(t *testing.T) {
	cfg := ExecutorConfig{
		ConfigDir:  t.TempDir(),
		RepoPath:   "repo/agent-1",
		BackupDirs: []string{"/data"},
	}

	executor := NewExecutor(cfg)

	if executor.restic == nil {
		t.Fatal("NewExecutor() runner is nil")
	}
	runner, ok := executor.restic.(PlainRunner)
	if !ok {
		t.Fatalf("NewExecutor() runner type = %T, want PlainRunner", executor.restic)
	}
	if runner.RepoPath != cfg.RepoPath {
		t.Fatalf("RepoPath = %q, want %q", runner.RepoPath, cfg.RepoPath)
	}
}

func TestNewExecutorUsesPlainRunnerWhenPasswordFileIsEmpty(t *testing.T) {
	cfgDir := t.TempDir()
	os.WriteFile(filepath.Join(cfgDir, ".restic-password"), []byte("  \n  "), 0o600)

	cfg := ExecutorConfig{
		ConfigDir: cfgDir,
		RepoPath:  "repo/agent-1",
	}

	executor := NewExecutor(cfg)

	if _, ok := executor.restic.(PlainRunner); !ok {
		t.Fatalf("NewExecutor() runner type = %T, want PlainRunner (empty password)", executor.restic)
	}
}

func TestRunBackupJobSuccessReturnsLatestSnapshotAndSnapshots(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	runner := &recordingRunner{
		backupDelay: 10 * time.Millisecond,
		repoSize:    4096,
		snapshots: []SnapshotInfo{
			{ID: "old", Time: now.Add(-time.Hour)},
			{ID: "new", Time: now},
		},
	}
	executor := &Executor{
		restic:     runner,
		backupDirs: []string{"/data"},
		excludes:   []string{"*.tmp"},
		retention:  RetentionPolicy{KeepLast: 2},
	}

	result := executor.RunBackupJob(context.Background())

	if result.Type != "backup" {
		t.Fatalf("Type = %q, want backup", result.Type)
	}
	if result.Status != "success" {
		t.Fatalf("Status = %q, want success; error log: %q", result.Status, result.ErrorLog)
	}
	if result.SnapshotID != "new" {
		t.Fatalf("SnapshotID = %q, want new", result.SnapshotID)
	}
	if result.RepoSize != 4096 {
		t.Fatalf("RepoSize = %d, want 4096", result.RepoSize)
	}
	if len(result.Snapshots) != 2 {
		t.Fatalf("Snapshots length = %d, want 2", len(result.Snapshots))
	}
	if result.DurationMs <= 0 {
		t.Fatalf("DurationMs = %d, want positive duration", result.DurationMs)
	}
	assertRunnerCalls(t, runner.calls, []string{"init", "unlock", "backup", "forget", "snapshots", "stats"})
}

func TestRunBackupJobFailsWhenRepositorySizeCannotBeRead(t *testing.T) {
	runner := &recordingRunner{
		snapshots: []SnapshotInfo{{ID: "snap-1", Time: time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)}},
		statsErr:  errors.New("repository locked"),
	}
	executor := &Executor{
		restic:     runner,
		backupDirs: []string{"/data"},
		retention:  RetentionPolicy{KeepLast: 1},
	}

	result := executor.RunBackupJob(context.Background())

	if result.Status != "failed" {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if !strings.Contains(result.ErrorLog, "stats: repository locked") {
		t.Fatalf("ErrorLog = %q, want stats stage and error", result.ErrorLog)
	}
	assertRunnerCalls(t, runner.calls, []string{"init", "unlock", "backup", "forget", "snapshots", "stats"})
}

func TestRunBackupJobFailureStopsAtStageAndReturnsErrorLog(t *testing.T) {
	runner := &recordingRunner{backupErr: errors.New("disk read failed")}
	executor := &Executor{
		restic:     runner,
		backupDirs: []string{"/data"},
		retention:  RetentionPolicy{KeepLast: 1},
	}

	result := executor.RunBackupJob(context.Background())

	if result.Status != "failed" {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if !strings.Contains(result.ErrorLog, "backup: disk read failed") {
		t.Fatalf("ErrorLog = %q, want backup stage and error", result.ErrorLog)
	}
	assertRunnerCalls(t, runner.calls, []string{"init", "unlock", "backup"})
}

func TestRunBackupJobWithProgressReportsPhasesAndUsesProgressRunner(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	runner := &recordingRunner{
		repoSize: 2048,
		snapshots: []SnapshotInfo{
			{ID: "snap-1", Time: now},
		},
	}
	executor := &Executor{
		restic:     runner,
		backupDirs: []string{"/data"},
		excludes:   []string{"*.tmp"},
		retention:  RetentionPolicy{KeepLast: 1},
	}
	var phases []string
	var updates []BackupProgress

	result := executor.RunBackupJobWithProgress(context.Background(), func(phase string, progress *BackupProgress) {
		phases = append(phases, phase)
		if progress != nil {
			updates = append(updates, *progress)
		}
	})

	if result.Status != "success" {
		t.Fatalf("Status = %q, want success; error log: %q", result.Status, result.ErrorLog)
	}
	assertRunnerCalls(t, runner.calls, []string{"init", "unlock", "backup_with_progress", "forget", "snapshots", "stats"})
	wantPhases := []string{"init", "backup", "backup", "backup", "forget", "stats"}
	if strings.Join(phases, ",") != strings.Join(wantPhases, ",") {
		t.Fatalf("progress phases = %#v, want %#v", phases, wantPhases)
	}
	if len(updates) != 2 {
		t.Fatalf("progress updates = %#v, want 2 updates", updates)
	}
	if updates[0].PercentDone != 0.5 || updates[0].FilesDone != 1 || updates[0].CurrentFile != "/data/a.txt" {
		t.Fatalf("first progress update = %+v", updates[0])
	}
	if updates[1].PercentDone != 1 || updates[1].FilesDone != 2 || updates[1].CurrentFile != "/data/b.txt" {
		t.Fatalf("second progress update = %+v", updates[1])
	}
}

func TestRunBackupJobWithProgressFallsBackToPlainBackupRunner(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	runner := &plainRecordingRunner{
		repoSize:  1024,
		snapshots: []SnapshotInfo{{ID: "snap-plain", Time: now}},
	}
	executor := &Executor{
		restic:     runner,
		backupDirs: []string{"/data"},
		retention:  RetentionPolicy{KeepLast: 1},
	}

	result := executor.RunBackupJobWithProgress(context.Background(), nil)

	if result.Status != "success" {
		t.Fatalf("Status = %q, want success; error log: %q", result.Status, result.ErrorLog)
	}
	if result.SnapshotID != "snap-plain" {
		t.Fatalf("SnapshotID = %q, want snap-plain", result.SnapshotID)
	}
	assertRunnerCalls(t, runner.calls, []string{"init", "unlock", "backup", "forget", "snapshots", "stats"})
}

func TestTaskResultJSONUsesSnakeCaseProtocolKeys(t *testing.T) {
	result := TaskResult{
		Type:       "backup",
		Status:     "success",
		DurationMs: 123,
		SnapshotID: "abc123",
		RepoSize:   4096,
		Snapshots: []SnapshotInfo{
			{ID: "abc123", Hostname: "agent-1", Paths: []string{"/data"}},
		},
		ErrorLog: "",
	}

	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal(TaskResult) error = %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("Unmarshal(TaskResult JSON) error = %v", err)
	}

	wantKeys := []string{"type", "status", "duration_ms", "snapshot_id", "repo_size", "snapshots"}
	for _, key := range wantKeys {
		if _, ok := got[key]; !ok {
			t.Fatalf("TaskResult JSON missing key %q: %s", key, payload)
		}
	}
	disallowedKeys := []string{"Type", "Status", "DurationMs", "SnapshotID", "RepoSize", "Snapshots", "ErrorLog"}
	for _, key := range disallowedKeys {
		if _, ok := got[key]; ok {
			t.Fatalf("TaskResult JSON uses Go field key %q: %s", key, payload)
		}
	}
	if _, ok := got["error_log"]; ok {
		t.Fatalf("TaskResult JSON included empty error_log despite omitempty: %s", payload)
	}
}

func TestTaskResultJSONOmitsEmptyOptionalFields(t *testing.T) {
	result := TaskResult{
		Type:       "backup",
		Status:     "failed",
		DurationMs: 123,
	}

	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal(TaskResult) error = %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("Unmarshal(TaskResult JSON) error = %v", err)
	}

	requiredKeys := []string{"type", "status", "duration_ms"}
	for _, key := range requiredKeys {
		if _, ok := got[key]; !ok {
			t.Fatalf("TaskResult JSON missing required key %q: %s", key, payload)
		}
	}
	optionalKeys := []string{"snapshot_id", "repo_size", "snapshots", "error_log"}
	for _, key := range optionalKeys {
		if _, ok := got[key]; ok {
			t.Fatalf("TaskResult JSON included empty optional key %q: %s", key, payload)
		}
	}
}

func TestTaskResultToProtocolMarshalsProtocolShape(t *testing.T) {
	startedAt := time.Date(2026, 5, 18, 9, 30, 0, 0, time.UTC)
	result := TaskResult{
		Type:       "backup",
		Status:     "failed",
		DurationMs: 2500,
		ErrorLog:   "backup: permission denied",
	}

	payload := result.ToProtocol("agent-007", startedAt)

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal(protocol payload) error = %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal(protocol payload JSON) error = %v", err)
	}

	for _, key := range []string{"agent_id", "task_type", "status", "duration_ms", "error_log", "started_at", "finished_at"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("protocol payload JSON missing key %q: %s", key, data)
		}
	}
	if _, ok := got["type"]; ok {
		t.Fatalf("protocol payload JSON used local type key instead of task_type: %s", data)
	}

	assertProtocolResult(t, payload, protocol.TaskResultPayload{
		AgentID:    "agent-007",
		TaskType:   "backup",
		Status:     "failed",
		DurationMs: 2500,
		ErrorLog:   "backup: permission denied",
		StartedAt:  startedAt,
		FinishedAt: startedAt.Add(2500 * time.Millisecond),
	})
}

func TestTaskResultToProtocolCopiesSuccessMetadata(t *testing.T) {
	startedAt := time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC)
	result := TaskResult{
		Type:       "backup",
		Status:     "success",
		DurationMs: 45000,
		SnapshotID: "abc123def456",
		RepoSize:   1073741824,
	}

	payload := result.ToProtocol("agent-002", startedAt)

	assertProtocolResult(t, payload, protocol.TaskResultPayload{
		AgentID:    "agent-002",
		TaskType:   "backup",
		Status:     "success",
		SnapshotID: "abc123def456",
		DurationMs: 45000,
		RepoSize:   1073741824,
		StartedAt:  startedAt,
		FinishedAt: startedAt.Add(45000 * time.Millisecond),
	})
}

type recordingRunner struct {
	calls       []string
	initErr     error
	backupOut   string
	backupErr   error
	backupDelay time.Duration
	forgetErr   error
	snapshots   []SnapshotInfo
	snapshotErr error
	repoSize    int64
	statsErr    error
	restoreErr  error
	unlockErr   error
	maintErr    error
	maintOut    string
}

func (r *recordingRunner) InitRepo(context.Context) error {
	r.calls = append(r.calls, "init")
	return r.initErr
}

func (r *recordingRunner) Unlock(context.Context) error {
	r.calls = append(r.calls, "unlock")
	return r.unlockErr
}

func (r *recordingRunner) RunBackup(_ context.Context, dirs []string, excludes []string) (string, error) {
	r.calls = append(r.calls, "backup")
	if r.backupDelay > 0 {
		time.Sleep(r.backupDelay)
	}
	return r.backupOut, r.backupErr
}

func (r *recordingRunner) RunBackupWithProgress(_ context.Context, dirs []string, excludes []string, progressFn func(BackupProgress)) (string, error) {
	r.calls = append(r.calls, "backup_with_progress")
	if r.backupDelay > 0 {
		time.Sleep(r.backupDelay)
	}
	if r.backupErr == nil && progressFn != nil {
		progressFn(BackupProgress{
			PercentDone: 0.5,
			TotalFiles:  2,
			FilesDone:   1,
			TotalBytes:  200,
			BytesDone:   100,
			CurrentFile: "/data/a.txt",
		})
		progressFn(BackupProgress{
			PercentDone: 1,
			TotalFiles:  2,
			FilesDone:   2,
			TotalBytes:  200,
			BytesDone:   200,
			CurrentFile: "/data/b.txt",
		})
	}
	return r.backupOut, r.backupErr
}

func (r *recordingRunner) RunForget(ctx context.Context, retention RetentionPolicy) error {
	select {
	case <-ctx.Done():
		r.calls = append(r.calls, "forget_cancelled")
	default:
		r.calls = append(r.calls, "forget")
	}
	return r.forgetErr
}

func (r *recordingRunner) ListSnapshots(context.Context) ([]SnapshotInfo, error) {
	r.calls = append(r.calls, "snapshots")
	return r.snapshots, r.snapshotErr
}

func (r *recordingRunner) RepositorySize(context.Context) (int64, error) {
	r.calls = append(r.calls, "stats")
	return r.repoSize, r.statsErr
}

func (r *recordingRunner) RestoreSnapshot(context.Context, string, string, []string) error {
	r.calls = append(r.calls, "restore")
	return r.restoreErr
}

func (r *recordingRunner) RunMaintenance(ctx context.Context, op MaintenanceOp) (string, error) {
	select {
	case <-ctx.Done():
		r.calls = append(r.calls, "maint:"+string(op)+":cancelled")
	default:
		r.calls = append(r.calls, "maint:"+string(op))
	}
	return r.maintOut, r.maintErr
}

type plainRecordingRunner struct {
	calls     []string
	backupErr error
	forgetErr error
	snapshots []SnapshotInfo
	repoSize  int64
}

func (r *plainRecordingRunner) InitRepo(context.Context) error {
	r.calls = append(r.calls, "init")
	return nil
}

func (r *plainRecordingRunner) Unlock(context.Context) error {
	r.calls = append(r.calls, "unlock")
	return nil
}

func (r *plainRecordingRunner) RunBackup(context.Context, []string, []string) (string, error) {
	r.calls = append(r.calls, "backup")
	return "", r.backupErr
}

func (r *plainRecordingRunner) RunForget(context.Context, RetentionPolicy) error {
	r.calls = append(r.calls, "forget")
	return r.forgetErr
}

func (r *plainRecordingRunner) ListSnapshots(context.Context) ([]SnapshotInfo, error) {
	r.calls = append(r.calls, "snapshots")
	return r.snapshots, nil
}

func (r *plainRecordingRunner) RepositorySize(context.Context) (int64, error) {
	r.calls = append(r.calls, "stats")
	return r.repoSize, nil
}

func (r *plainRecordingRunner) RestoreSnapshot(context.Context, string, string, []string) error {
	r.calls = append(r.calls, "restore")
	return nil
}

func (r *plainRecordingRunner) RunMaintenance(context.Context, MaintenanceOp) (string, error) {
	return "", nil
}

func TestRunMaintenanceJobPruneUsesIndependentCtx(t *testing.T) {
	runner := &recordingRunner{maintOut: "pruned"}
	executor := &Executor{restic: runner}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := executor.RunMaintenanceJob(ctx, OpPrune)

	if result.Status != "success" {
		t.Fatalf("status = %q, want success (err=%q)", result.Status, result.ErrorLog)
	}
	for _, c := range runner.calls {
		if c == "maint:prune:cancelled" {
			t.Fatal("prune received cancelled ctx; must use independent ctx")
		}
	}
}

func TestRunMaintenanceJobCheckReturnsOutput(t *testing.T) {
	runner := &recordingRunner{maintOut: "no errors were found"}
	executor := &Executor{restic: runner}
	result := executor.RunMaintenanceJob(context.Background(), OpCheck)
	if result.Status != "success" || result.Output != "no errors were found" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRecordingRunnerImplementsUnlock(t *testing.T) {
	var _ resticExecutor = (*recordingRunner)(nil)
	r := &recordingRunner{}
	if err := r.Unlock(context.Background()); err != nil {
		t.Fatalf("Unlock() error = %v, want nil", err)
	}
	if len(r.calls) != 1 || r.calls[0] != "unlock" {
		t.Fatalf("calls = %#v, want [unlock]", r.calls)
	}
}

func assertRunnerCalls(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("runner calls = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("runner calls = %#v, want %#v", got, want)
		}
	}
}

func assertProtocolResult(t *testing.T, got, want protocol.TaskResultPayload) {
	t.Helper()
	if got.AgentID != want.AgentID {
		t.Fatalf("AgentID = %q, want %q", got.AgentID, want.AgentID)
	}
	if got.TaskType != want.TaskType {
		t.Fatalf("TaskType = %q, want %q", got.TaskType, want.TaskType)
	}
	if got.Status != want.Status {
		t.Fatalf("Status = %q, want %q", got.Status, want.Status)
	}
	if got.SnapshotID != want.SnapshotID {
		t.Fatalf("SnapshotID = %q, want %q", got.SnapshotID, want.SnapshotID)
	}
	if got.DurationMs != want.DurationMs {
		t.Fatalf("DurationMs = %d, want %d", got.DurationMs, want.DurationMs)
	}
	if got.RepoSize != want.RepoSize {
		t.Fatalf("RepoSize = %d, want %d", got.RepoSize, want.RepoSize)
	}
	if got.ErrorLog != want.ErrorLog {
		t.Fatalf("ErrorLog = %q, want %q", got.ErrorLog, want.ErrorLog)
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Fatalf("StartedAt = %s, want %s", got.StartedAt, want.StartedAt)
	}
	if !got.FinishedAt.Equal(want.FinishedAt) {
		t.Fatalf("FinishedAt = %s, want %s", got.FinishedAt, want.FinishedAt)
	}
}

func TestRunBackupJobCallsUnlockBeforeBackup(t *testing.T) {
	runner := &recordingRunner{
		snapshots: []SnapshotInfo{{ID: "s1", Time: time.Now()}},
		repoSize:  100,
	}
	executor := &Executor{restic: runner}

	result := executor.RunBackupJob(context.Background())

	if result.Status != "success" {
		t.Fatalf("status = %q, want success (err=%q)", result.Status, result.ErrorLog)
	}
	assertRunnerCalls(t, runner.calls, []string{"init", "unlock", "backup", "forget", "snapshots", "stats"})
}

func TestRunBackupJobContinuesWhenUnlockFails(t *testing.T) {
	runner := &recordingRunner{
		unlockErr: errors.New("unlock boom"),
		snapshots: []SnapshotInfo{{ID: "s1", Time: time.Now()}},
		repoSize:  100,
	}
	executor := &Executor{restic: runner}

	result := executor.RunBackupJob(context.Background())

	if result.Status != "success" {
		t.Fatalf("status = %q, want success despite unlock failure (err=%q)", result.Status, result.ErrorLog)
	}
	assertRunnerCalls(t, runner.calls, []string{"init", "unlock", "backup", "forget", "snapshots", "stats"})
}

func TestForgetUsesIndependentContextNotCancelledByTaskCtx(t *testing.T) {
	runner := &recordingRunner{
		snapshots: []SnapshotInfo{{ID: "s1", Time: time.Now()}},
		repoSize:  100,
	}
	executor := &Executor{restic: runner}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 任务 ctx 立即取消

	_ = executor.RunBackupJob(ctx)

	for _, c := range runner.calls {
		if c == "forget_cancelled" {
			t.Fatal("forget received a cancelled context; it must use an independent ctx")
		}
	}
	foundForget := false
	for _, c := range runner.calls {
		if c == "forget" {
			foundForget = true
		}
	}
	if !foundForget {
		t.Fatalf("forget was not called; calls = %#v", runner.calls)
	}
}
