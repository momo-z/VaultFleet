package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type MaintenanceOp string

const (
	OpUnlock          MaintenanceOp = "unlock"
	OpCheck           MaintenanceOp = "check"
	OpRepairIndex     MaintenanceOp = "repair_index"
	OpRepairSnapshots MaintenanceOp = "repair_snapshots"
	OpPrune           MaintenanceOp = "prune"
)

type RetentionPolicy struct {
	KeepLast    int `json:"keep_last"`
	KeepDaily   int `json:"keep_daily"`
	KeepWeekly  int `json:"keep_weekly"`
	KeepMonthly int `json:"keep_monthly"`
}

type SnapshotInfo struct {
	ID       string    `json:"id"`
	Time     time.Time `json:"time"`
	Paths    []string  `json:"paths"`
	Hostname string    `json:"hostname"`
	Size     int64     `json:"size"`
}

type SnapshotFileEntry struct {
	Path  string `json:"path"`
	Type  string `json:"type"`
	Size  int64  `json:"size"`
	Mtime string `json:"mtime"`
}

type ResticRunner struct {
	RcloneConfPath  string
	PasswordFile    string
	RepoPath        string
	CacheDir        string
	RcloneExtraArgs map[string]string
}

func (r ResticRunner) repoArg() string {
	return "rclone:vaultfleet:" + r.RepoPath
}

func (r ResticRunner) baseEnv() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "RCLONE_CONFIG=") && !strings.HasPrefix(entry, "XDG_CACHE_HOME=") {
			env = append(env, entry)
		}
	}
	return append(env, "RCLONE_CONFIG="+r.RcloneConfPath, "XDG_CACHE_HOME="+r.cacheDir())
}

func (r ResticRunner) cacheDir() string {
	if r.CacheDir != "" {
		return r.CacheDir
	}
	return filepath.Join(filepath.Dir(r.RcloneConfPath), ".cache")
}

func (r ResticRunner) baseArgs() []string {
	args := []string{"-r", r.repoArg()}
	if r.hasPassword() {
		args = append(args, "--password-file", r.PasswordFile)
	} else {
		args = append(args, "--insecure-no-password")
	}
	args = append(args, "-o", "rclone.args="+r.rcloneServeArgs())
	return args
}

// Read-only repo access should not leave backend locks behind if the process is interrupted.
func (r ResticRunner) baseReadOnlyArgs() []string {
	args := []string{"--no-lock"}
	return append(args, r.baseArgs()...)
}

func (r ResticRunner) rcloneServeArgs() string {
	args := "serve restic --stdio --config " + r.RcloneConfPath
	extraArgs := r.normalizedRcloneExtraArgs()
	for _, arg := range extraArgs {
		args += " " + arg
	}
	return args
}

func (r ResticRunner) normalizedRcloneExtraArgs() []string {
	if len(r.RcloneExtraArgs) == 0 {
		return nil
	}

	keys := make([]string, 0, len(r.RcloneExtraArgs))
	values := make(map[string]string, len(r.RcloneExtraArgs))
	for key, value := range r.RcloneExtraArgs {
		normalized, ok := normalizeRcloneExtraArgValue(key, value)
		if isAllowedRcloneExtraArg(key) && ok {
			keys = append(keys, key)
			values[key] = normalized
		}
	}
	sort.Strings(keys)

	args := make([]string, 0, len(keys)*2)
	for _, key := range keys {
		args = append(args, "--"+key, values[key])
	}
	return args
}

func isAllowedRcloneExtraArg(key string) bool {
	switch key {
	case "transfers", "tpslimit", "retries", "retries-sleep", "low-level-retries", "timeout":
		return true
	default:
		return false
	}
}

func normalizeRcloneExtraArgValue(key string, value string) (string, bool) {
	normalized := strings.TrimSpace(value)
	if normalized == "" || strings.ContainsAny(normalized, " \t\r\n") {
		return "", false
	}

	switch key {
	case "transfers":
		parsed, err := strconv.Atoi(normalized)
		return normalized, err == nil && parsed > 0
	case "retries", "low-level-retries":
		parsed, err := strconv.Atoi(normalized)
		return normalized, err == nil && parsed >= 0
	case "tpslimit":
		parsed, err := strconv.ParseFloat(normalized, 64)
		return normalized, err == nil && parsed >= 0
	case "retries-sleep", "timeout":
		if normalized == "0" {
			return normalized, true
		}
		parsed, err := time.ParseDuration(normalized)
		return normalized, err == nil && parsed >= 0
	default:
		return "", false
	}
}

func (r ResticRunner) hasPassword() bool {
	data, err := os.ReadFile(r.PasswordFile)
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(data))) > 0
}

func (r ResticRunner) buildInitCmd() *exec.Cmd {
	return r.buildInitCmdContext(context.Background())
}

func (r ResticRunner) buildBackupCmd(dirs []string, excludes []string) *exec.Cmd {
	return r.buildBackupCmdContext(context.Background(), dirs, excludes)
}

func (r ResticRunner) buildBackupWithProgressCmd(dirs []string, excludes []string) *exec.Cmd {
	return r.buildBackupWithProgressCmdContext(context.Background(), dirs, excludes)
}

func (r ResticRunner) buildForgetCmd(retention RetentionPolicy) *exec.Cmd {
	return r.buildForgetCmdContext(context.Background(), retention)
}

func (r ResticRunner) buildSnapshotsCmd() *exec.Cmd {
	return r.buildSnapshotsCmdContext(context.Background())
}

func (r ResticRunner) buildLsSnapshotCmd(snapshotID string) *exec.Cmd {
	return r.buildLsSnapshotCmdContext(context.Background(), snapshotID)
}

func (r ResticRunner) buildStatsCmd() *exec.Cmd {
	return r.buildStatsCmdContext(context.Background())
}

func (r ResticRunner) buildRestoreCmdWithIncludes(snapshotID, targetPath string, includePaths []string) *exec.Cmd {
	cmd, _ := r.buildRestoreCmdWithIncludesChecked(context.Background(), snapshotID, targetPath, includePaths)
	return cmd
}

func (r ResticRunner) buildRestoreCmdWithIncludesChecked(ctx context.Context, snapshotID, targetPath string, includePaths []string) (*exec.Cmd, error) {
	if err := validateRestoreIncludePaths(includePaths); err != nil {
		return nil, err
	}
	return r.buildRestoreCmdWithIncludesContext(ctx, snapshotID, targetPath, includePaths), nil
}

func (r ResticRunner) buildInitCmdContext(ctx context.Context) *exec.Cmd {
	args := append([]string{"init"}, r.baseArgs()...)
	return r.command(ctx, args...)
}

func (r ResticRunner) buildUnlockCmdContext(ctx context.Context) *exec.Cmd {
	args := append([]string{"unlock"}, r.baseArgs()...)
	return r.command(ctx, args...)
}

func (r ResticRunner) buildBackupCmdContext(ctx context.Context, dirs []string, excludes []string) *exec.Cmd {
	args := append([]string{"backup"}, r.baseArgs()...)
	for _, exclude := range excludes {
		args = append(args, "--exclude="+exclude)
	}
	args = append(args, dirs...)
	return r.command(ctx, args...)
}

func (r ResticRunner) buildBackupWithProgressCmdContext(ctx context.Context, dirs []string, excludes []string) *exec.Cmd {
	args := append([]string{"backup", "--json"}, r.baseArgs()...)
	for _, exclude := range excludes {
		args = append(args, "--exclude="+exclude)
	}
	args = append(args, dirs...)
	return r.command(ctx, args...)
}

func (r ResticRunner) buildForgetCmdContext(ctx context.Context, retention RetentionPolicy) *exec.Cmd {
	args := append([]string{"forget"}, r.baseArgs()...)
	args = append(args, "--prune")
	if retention.KeepLast > 0 {
		args = append(args, "--keep-last="+strconv.Itoa(retention.KeepLast))
	}
	if retention.KeepDaily > 0 {
		args = append(args, "--keep-daily="+strconv.Itoa(retention.KeepDaily))
	}
	if retention.KeepWeekly > 0 {
		args = append(args, "--keep-weekly="+strconv.Itoa(retention.KeepWeekly))
	}
	if retention.KeepMonthly > 0 {
		args = append(args, "--keep-monthly="+strconv.Itoa(retention.KeepMonthly))
	}
	return r.command(ctx, args...)
}

func (r ResticRunner) buildCheckCmdContext(ctx context.Context) *exec.Cmd {
	args := append([]string{"check"}, r.baseArgs()...)
	return r.command(ctx, args...)
}

func (r ResticRunner) buildRepairIndexCmdContext(ctx context.Context) *exec.Cmd {
	args := append([]string{"repair", "index"}, r.baseArgs()...)
	return r.command(ctx, args...)
}

func (r ResticRunner) buildRepairSnapshotsCmdContext(ctx context.Context) *exec.Cmd {
	args := append([]string{"repair", "snapshots", "--forget"}, r.baseArgs()...)
	return r.command(ctx, args...)
}

func (r ResticRunner) buildPruneCmdContext(ctx context.Context) *exec.Cmd {
	args := append([]string{"prune"}, r.baseArgs()...)
	return r.command(ctx, args...)
}

func (r ResticRunner) buildSnapshotsCmdContext(ctx context.Context) *exec.Cmd {
	args := []string{"snapshots", "--json"}
	args = append(args, r.baseReadOnlyArgs()...)
	return r.command(ctx, args...)
}

func (r ResticRunner) buildLsSnapshotCmdContext(ctx context.Context, snapshotID string, paths ...string) *exec.Cmd {
	args := []string{"ls", snapshotID, "--json"}
	args = append(args, r.baseReadOnlyArgs()...)
	for _, p := range paths {
		if p != "" {
			args = append(args, p)
		}
	}
	return r.command(ctx, args...)
}

func (r ResticRunner) buildStatsCmdContext(ctx context.Context) *exec.Cmd {
	args := []string{"stats", "--mode", "raw-data", "--json"}
	args = append(args, r.baseReadOnlyArgs()...)
	return r.command(ctx, args...)
}

func (r ResticRunner) buildRestoreCmdWithIncludesContext(ctx context.Context, snapshotID, targetPath string, includePaths []string) *exec.Cmd {
	args := []string{"restore", snapshotID, "--target", targetPath}
	for _, p := range includePaths {
		args = append(args, "--include", p)
	}
	args = append(args, r.baseReadOnlyArgs()...)
	return r.command(ctx, args...)
}

func (r ResticRunner) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "restic", args...)
	cmd.Env = r.baseEnv()
	// On context cancellation, send SIGTERM (not the default SIGKILL) so restic
	// can stop at a safe point and clean up its own repository lock. If it does
	// not exit within WaitDelay, the process is force-killed to avoid hanging.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 30 * time.Second
	return cmd
}

func (r ResticRunner) InitRepo(ctx context.Context) error {
	if ok, err := r.repoExists(ctx); err != nil {
		return err
	} else if ok {
		return nil
	}

	if err := r.ensureRemoteDir(ctx); err != nil {
		return err
	}

	cmd := r.buildInitCmdContext(ctx)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if isAlreadyInitializedRepo(stderr.String()) {
			return nil
		}
		return commandError("initialize restic repository", stderr.String(), err)
	}
	return nil
}

// ensureRemoteDir pre-creates the remote directory via rclone mkdir so that
// restic init does not trigger Mkdir itself — some S3-compatible backends
// (e.g. Tianyi Cloud) return 409 Conflict when the parent directory already exists.
func (r ResticRunner) ensureRemoteDir(ctx context.Context) error {
	args := []string{"--config", r.RcloneConfPath}
	args = append(args, r.normalizedRcloneExtraArgs()...)
	args = append(args, "mkdir", "vaultfleet:"+r.RepoPath)
	cmd := exec.CommandContext(ctx, "rclone", args...)
	cmd.Env = r.baseEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return commandError("pre-create remote directory", stderr.String(), err)
	}
	return nil
}

func (r ResticRunner) repoExists(ctx context.Context) (bool, error) {
	cmd := r.buildSnapshotsCmdContext(ctx)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if isAlreadyInitializedRepo(stderr.String()) {
			return true, nil
		}
		if isMissingRepository(stderr.String()) {
			return false, nil
		}
		return false, commandError("check restic repository", stderr.String(), err)
	}
	return true, nil
}

func isAlreadyInitializedRepo(stderr string) bool {
	normalized := strings.ToLower(stderr)
	return strings.Contains(normalized, "already initialized") ||
		strings.Contains(normalized, "config file already exists")
}

func isMissingRepository(stderr string) bool {
	normalized := strings.ToLower(stderr)
	return strings.Contains(normalized, "is there a repository at the following location") ||
		strings.Contains(normalized, "repository does not exist") ||
		strings.Contains(normalized, "unable to open repository") ||
		strings.Contains(normalized, "config file does not exist")
}

func (r ResticRunner) RunBackup(ctx context.Context, dirs []string, excludes []string) (string, error) {
	cmd := r.buildBackupCmdContext(ctx, dirs, excludes)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", commandError("run restic backup", stderr.String(), err)
	}
	return stdout.String(), nil
}

func (r ResticRunner) RunBackupWithProgress(ctx context.Context, dirs []string, excludes []string, progressFn func(BackupProgress)) (string, error) {
	cmd := r.buildBackupWithProgressCmdContext(ctx, dirs, excludes)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("prepare restic backup stdout: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", commandError("run restic backup", stderr.String(), err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var snapshotID string
	var backupErrors []string
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var message struct {
			MessageType  string          `json:"message_type"`
			PercentDone  float64         `json:"percent_done"`
			TotalFiles   int64           `json:"total_files"`
			FilesDone    int64           `json:"files_done"`
			TotalBytes   int64           `json:"total_bytes"`
			BytesDone    int64           `json:"bytes_done"`
			CurrentFiles []string        `json:"current_files"`
			SnapshotID   string          `json:"snapshot_id"`
			Error        json.RawMessage `json:"error"`
			During       string          `json:"during"`
			Item         string          `json:"item"`
		}
		if err := json.Unmarshal(line, &message); err != nil {
			continue
		}

		switch message.MessageType {
		case "status":
			if progressFn != nil {
				progress := BackupProgress{
					PercentDone: message.PercentDone,
					TotalFiles:  message.TotalFiles,
					FilesDone:   message.FilesDone,
					TotalBytes:  message.TotalBytes,
					BytesDone:   message.BytesDone,
				}
				if len(message.CurrentFiles) > 0 {
					progress.CurrentFile = message.CurrentFiles[0]
				}
				progressFn(progress)
			}
		case "summary":
			snapshotID = message.SnapshotID
		case "error":
			if text := resticBackupJSONErrorText(message.Item, message.During, message.Error); text != "" {
				backupErrors = append(backupErrors, text)
			}
		}
	}

	scannerErr := scanner.Err()
	if scannerErr != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if scannerErr != nil {
		err := fmt.Errorf("scan restic backup JSON output: %w", scannerErr)
		if waitErr != nil {
			return "", fmt.Errorf("%w; %v", err, commandError("run restic backup", stderr.String(), waitErr))
		}
		return "", err
	}
	if waitErr != nil {
		return "", commandError("run restic backup", resticBackupErrorDetails(stderr.String(), backupErrors), waitErr)
	}
	return snapshotID, nil
}

func resticBackupJSONErrorText(item string, during string, rawError json.RawMessage) string {
	parts := make([]string, 0, 3)
	if item != "" {
		parts = append(parts, item)
	}
	if during != "" {
		parts = append(parts, during)
	}
	if len(rawError) > 0 {
		var message string
		if err := json.Unmarshal(rawError, &message); err == nil {
			if message != "" {
				parts = append(parts, message)
			}
		} else {
			parts = append(parts, strings.TrimSpace(string(rawError)))
		}
	}
	return strings.Join(parts, ": ")
}

func resticBackupErrorDetails(stderr string, backupErrors []string) string {
	details := strings.TrimSpace(stderr)
	if len(backupErrors) == 0 {
		return details
	}
	if details != "" {
		details += "\n"
	}
	return details + strings.Join(backupErrors, "\n")
}

// Unlock removes stale repository locks (locks whose creating process no
// longer exists or has timed out). It does not pass --remove-all, so active
// locks are left untouched. Used as a pre-backup preflight.
func (r ResticRunner) Unlock(ctx context.Context) error {
	cmd := r.buildUnlockCmdContext(ctx)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return commandError("run restic unlock", stderr.String(), err)
	}
	return nil
}

// RunMaintenance executes a repository maintenance operation and returns its
// combined output. unlock delegates to Unlock. Other ops run their builder.
func (r ResticRunner) RunMaintenance(ctx context.Context, op MaintenanceOp) (string, error) {
	if op == OpUnlock {
		return "", r.Unlock(ctx)
	}
	var cmd *exec.Cmd
	switch op {
	case OpCheck:
		cmd = r.buildCheckCmdContext(ctx)
	case OpRepairIndex:
		cmd = r.buildRepairIndexCmdContext(ctx)
	case OpRepairSnapshots:
		cmd = r.buildRepairSnapshotsCmdContext(ctx)
	case OpPrune:
		cmd = r.buildPruneCmdContext(ctx)
	default:
		return "", fmt.Errorf("unknown maintenance op: %q", op)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), commandError("run restic "+string(op), stderr.String(), err)
	}
	return stdout.String() + stderr.String(), nil
}

func (r ResticRunner) RunForget(ctx context.Context, retention RetentionPolicy) error {
	cmd := r.buildForgetCmdContext(ctx, retention)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return commandError("run restic forget", stderr.String(), err)
	}
	return nil
}

func (r ResticRunner) ListSnapshots(ctx context.Context) ([]SnapshotInfo, error) {
	cmd := r.buildSnapshotsCmdContext(ctx)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, commandError("list restic snapshots", stderr.String(), err)
	}

	var snapshots []SnapshotInfo
	if err := json.Unmarshal(stdout.Bytes(), &snapshots); err != nil {
		return nil, fmt.Errorf("parse restic snapshots JSON: %w", err)
	}
	return snapshots, nil
}

func (r ResticRunner) LsSnapshot(ctx context.Context, snapshotID string, paths ...string) ([]SnapshotFileEntry, error) {
	cmd := r.buildLsSnapshotCmdContext(ctx, snapshotID, paths...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, commandError("list snapshot contents", stderr.String(), err)
	}

	var entries []SnapshotFileEntry
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if line == "" {
			continue
		}
		var raw struct {
			StructType string `json:"struct_type"`
			Path       string `json:"path"`
			Type       string `json:"type"`
			Size       int64  `json:"size"`
			Mtime      string `json:"mtime"`
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		if raw.StructType != "node" {
			continue
		}
		entries = append(entries, SnapshotFileEntry{
			Path:  raw.Path,
			Type:  raw.Type,
			Size:  raw.Size,
			Mtime: raw.Mtime,
		})
	}
	return entries, nil
}

func (r ResticRunner) RepositorySize(ctx context.Context) (int64, error) {
	cmd := r.buildStatsCmdContext(ctx)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return 0, commandError("read restic repository stats", stderr.String(), err)
	}

	var stats struct {
		TotalSize int64 `json:"total_size"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &stats); err != nil {
		return 0, fmt.Errorf("parse restic stats JSON: %w", err)
	}
	return stats.TotalSize, nil
}

func (r ResticRunner) RestoreSnapshot(ctx context.Context, snapshotID, targetPath string, includePaths []string) error {
	cmd, err := r.buildRestoreCmdWithIncludesChecked(ctx, snapshotID, targetPath, includePaths)
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return commandError("restore restic snapshot", stderr.String(), err)
	}
	return nil
}

func validateRestoreIncludePaths(includePaths []string) error {
	for _, path := range includePaths {
		if strings.ContainsAny(path, "*?[") {
			return fmt.Errorf("include path contains unsupported pattern characters: %s", path)
		}
	}
	return nil
}

func commandError(action string, stderr string, err error) error {
	if stderr == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w: %s", action, err, strings.TrimSpace(stderr))
}
