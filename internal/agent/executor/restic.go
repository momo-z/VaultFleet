package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
	RcloneConfPath string
	PasswordFile   string
	RepoPath       string
	CacheDir       string
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
	return args
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

func (r ResticRunner) buildBackupCmdContext(ctx context.Context, dirs []string, excludes []string) *exec.Cmd {
	args := append([]string{"backup"}, r.baseArgs()...)
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

func (r ResticRunner) buildSnapshotsCmdContext(ctx context.Context) *exec.Cmd {
	args := []string{"snapshots", "--json"}
	args = append(args, r.baseArgs()...)
	return r.command(ctx, args...)
}

func (r ResticRunner) buildLsSnapshotCmdContext(ctx context.Context, snapshotID string, paths ...string) *exec.Cmd {
	args := []string{"ls", snapshotID, "--json"}
	args = append(args, r.baseArgs()...)
	for _, p := range paths {
		if p != "" {
			args = append(args, p)
		}
	}
	return r.command(ctx, args...)
}

func (r ResticRunner) buildStatsCmdContext(ctx context.Context) *exec.Cmd {
	args := []string{"stats", "--mode", "raw-data", "--json"}
	args = append(args, r.baseArgs()...)
	return r.command(ctx, args...)
}

func (r ResticRunner) buildRestoreCmdWithIncludesContext(ctx context.Context, snapshotID, targetPath string, includePaths []string) *exec.Cmd {
	args := []string{"restore", snapshotID, "--target", targetPath}
	for _, p := range includePaths {
		args = append(args, "--include", p)
	}
	args = append(args, r.baseArgs()...)
	return r.command(ctx, args...)
}

func (r ResticRunner) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "restic", args...)
	cmd.Env = r.baseEnv()
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
	cmd := exec.CommandContext(ctx, "rclone", "mkdir", "vaultfleet:"+r.RepoPath)
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
