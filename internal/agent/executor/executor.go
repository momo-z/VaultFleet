package executor

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"vaultfleet/pkg/protocol"
)

// pruneHardTimeout caps how long forget --prune may run. The prune phase uses
// an independent context (not tied to task cancellation) so it is never killed
// mid-operation, which would corrupt the repository. This timeout only guards
// against an indefinitely hung prune.
const pruneHardTimeout = 2 * time.Hour

type TaskResult struct {
	Type       string         `json:"type"`
	Status     string         `json:"status"`
	DurationMs int64          `json:"duration_ms"`
	SnapshotID string         `json:"snapshot_id,omitempty"`
	RepoSize   int64          `json:"repo_size,omitempty"`
	Snapshots  []SnapshotInfo `json:"snapshots,omitempty"`
	ErrorLog   string         `json:"error_log,omitempty"`
}

func (r TaskResult) ToProtocol(agentID string, startedAt time.Time) protocol.TaskResultPayload {
	snapshots := make([]protocol.SnapshotInfo, 0, len(r.Snapshots))
	for _, snapshot := range r.Snapshots {
		snapshots = append(snapshots, protocol.SnapshotInfo{
			ID:    snapshot.ID,
			Time:  snapshot.Time,
			Paths: append([]string(nil), snapshot.Paths...),
			Size:  snapshot.Size,
		})
	}

	return protocol.TaskResultPayload{
		AgentID:    agentID,
		TaskType:   r.Type,
		Status:     r.Status,
		SnapshotID: r.SnapshotID,
		DurationMs: r.DurationMs,
		RepoSize:   r.RepoSize,
		ErrorLog:   r.ErrorLog,
		StartedAt:  startedAt,
		FinishedAt: startedAt.Add(time.Duration(r.DurationMs) * time.Millisecond),
		Snapshots:  snapshots,
	}
}

type ExecutorConfig struct {
	ConfigDir   string
	RepoPath    string
	BackupDirs  []string
	Excludes    []string
	Retention   RetentionPolicy
	RcloneArgs  map[string]string
	PlainBackup bool
}

type BackupProgress struct {
	PercentDone float64
	TotalFiles  int64
	FilesDone   int64
	TotalBytes  int64
	BytesDone   int64
	CurrentFile string
}

type ProgressCallback func(phase string, progress *BackupProgress)

type resticExecutor interface {
	InitRepo(ctx context.Context) error
	Unlock(ctx context.Context) error
	RunBackup(ctx context.Context, dirs []string, excludes []string) (string, error)
	RunForget(ctx context.Context, retention RetentionPolicy) error
	ListSnapshots(ctx context.Context) ([]SnapshotInfo, error)
	RepositorySize(ctx context.Context) (int64, error)
	RestoreSnapshot(ctx context.Context, snapshotID, targetPath string, includePaths []string) error
}

type resticExecutorWithProgress interface {
	RunBackupWithProgress(ctx context.Context, dirs []string, excludes []string, progressFn func(BackupProgress)) (string, error)
}

type Executor struct {
	restic     resticExecutor
	backupDirs []string
	excludes   []string
	retention  RetentionPolicy
}

func NewExecutor(cfg ExecutorConfig) *Executor {
	rcloneConfPath := filepath.Join(cfg.ConfigDir, "rclone.conf")
	passwordFile := filepath.Join(cfg.ConfigDir, ".restic-password")
	rcloneArgs := copyStringMap(cfg.RcloneArgs)

	// Use explicit PlainBackup flag from the policy if set.
	// Otherwise fall back to checking the password file on disk.
	usePlain := cfg.PlainBackup || !HasPasswordFile(passwordFile)

	var runner resticExecutor
	if usePlain {
		log.Printf("using plain rclone backup (no encryption)")
		runner = PlainRunner{
			RcloneConfPath:  rcloneConfPath,
			RepoPath:        cfg.RepoPath,
			RcloneExtraArgs: rcloneArgs,
		}
	} else {
		log.Printf("using encrypted restic backup")
		runner = ResticRunner{
			RcloneConfPath:  rcloneConfPath,
			PasswordFile:    passwordFile,
			RepoPath:        cfg.RepoPath,
			RcloneExtraArgs: rcloneArgs,
		}
	}

	return &Executor{
		restic:     runner,
		backupDirs: append([]string(nil), cfg.BackupDirs...),
		excludes:   append([]string(nil), cfg.Excludes...),
		retention:  cfg.Retention,
	}
}

// HasPasswordFile returns true if the restic password file exists and
// contains a non-empty password. This is used to decide between
// encrypted restic backups and plain rclone backups.
func HasPasswordFile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(data))) > 0
}

func copyStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}

func (e *Executor) RunBackupJob(ctx context.Context) (result TaskResult) {
	start := time.Now()
	result = TaskResult{
		Type:   "backup",
		Status: "failed",
	}
	defer func() {
		result.DurationMs = time.Since(start).Milliseconds()
	}()

	if err := e.restic.InitRepo(ctx); err != nil {
		result.ErrorLog = "init: " + err.Error()
		return result
	}
	// Preflight: clear stale locks before backup. Non-fatal — if the repo is
	// genuinely locked by an active process, the backup below will report it.
	if err := e.restic.Unlock(ctx); err != nil {
		log.Printf("pre-backup unlock failed (continuing): %v", err)
	}
	if _, err := e.restic.RunBackup(ctx, e.backupDirs, e.excludes); err != nil {
		result.ErrorLog = "backup: " + err.Error()
		return result
	}
	forgetCtx, cancelForget := context.WithTimeout(context.Background(), pruneHardTimeout)
	err := e.restic.RunForget(forgetCtx, e.retention)
	cancelForget()
	if err != nil {
		result.ErrorLog = "forget: " + err.Error()
		return result
	}

	snapshots, err := e.restic.ListSnapshots(ctx)
	if err != nil {
		result.ErrorLog = "snapshots: " + err.Error()
		return result
	}

	repoSize, err := e.restic.RepositorySize(ctx)
	if err != nil {
		result.ErrorLog = "stats: " + err.Error()
		return result
	}

	result.Status = "success"
	result.ErrorLog = ""
	result.Snapshots = append([]SnapshotInfo(nil), snapshots...)
	result.RepoSize = repoSize
	if len(snapshots) > 0 {
		latest := snapshots[0]
		for _, snapshot := range snapshots[1:] {
			if snapshot.Time.After(latest.Time) {
				latest = snapshot
			}
		}
		result.SnapshotID = latest.ID
	}
	return result
}

func (e *Executor) RunBackupJobWithProgress(ctx context.Context, progressFn ProgressCallback) (result TaskResult) {
	start := time.Now()
	result = TaskResult{
		Type:   "backup",
		Status: "failed",
	}
	defer func() {
		result.DurationMs = time.Since(start).Milliseconds()
	}()

	emitProgress(progressFn, "init", nil)
	if err := e.restic.InitRepo(ctx); err != nil {
		result.ErrorLog = "init: " + err.Error()
		return result
	}
	// Preflight: clear stale locks before backup (non-fatal).
	if err := e.restic.Unlock(ctx); err != nil {
		log.Printf("pre-backup unlock failed (continuing): %v", err)
	}

	emitProgress(progressFn, "backup", nil)
	if progressRunner, ok := e.restic.(resticExecutorWithProgress); ok {
		var resticProgressFn func(BackupProgress)
		if progressFn != nil {
			resticProgressFn = func(progress BackupProgress) {
				progressCopy := progress
				emitProgress(progressFn, "backup", &progressCopy)
			}
		}
		if _, err := progressRunner.RunBackupWithProgress(ctx, e.backupDirs, e.excludes, resticProgressFn); err != nil {
			result.ErrorLog = "backup: " + err.Error()
			return result
		}
	} else if _, err := e.restic.RunBackup(ctx, e.backupDirs, e.excludes); err != nil {
		result.ErrorLog = "backup: " + err.Error()
		return result
	}

	emitProgress(progressFn, "forget", nil)
	forgetCtx, cancelForget := context.WithTimeout(context.Background(), pruneHardTimeout)
	forgetErr := e.restic.RunForget(forgetCtx, e.retention)
	cancelForget()
	if forgetErr != nil {
		result.ErrorLog = "forget: " + forgetErr.Error()
		return result
	}

	emitProgress(progressFn, "stats", nil)
	snapshots, err := e.restic.ListSnapshots(ctx)
	if err != nil {
		result.ErrorLog = "snapshots: " + err.Error()
		return result
	}

	repoSize, err := e.restic.RepositorySize(ctx)
	if err != nil {
		result.ErrorLog = "stats: " + err.Error()
		return result
	}

	result.Status = "success"
	result.ErrorLog = ""
	result.Snapshots = append([]SnapshotInfo(nil), snapshots...)
	result.RepoSize = repoSize
	if len(snapshots) > 0 {
		latest := snapshots[0]
		for _, snapshot := range snapshots[1:] {
			if snapshot.Time.After(latest.Time) {
				latest = snapshot
			}
		}
		result.SnapshotID = latest.ID
	}
	return result
}

func emitProgress(progressFn ProgressCallback, phase string, progress *BackupProgress) {
	if progressFn != nil {
		progressFn(phase, progress)
	}
}
