package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PlainRunner implements backup/restore using plain rclone copy/sync
// (no restic, no encryption). It is used when the user does not provide
// a restic password, producing plain files on the remote storage.
type PlainRunner struct {
	RcloneConfPath  string
	RepoPath        string
	CacheDir        string
	RcloneExtraArgs map[string]string
}

func (r PlainRunner) remoteArg(subPath string) string {
	if subPath == "" {
		return "vaultfleet:" + r.RepoPath
	}
	return "vaultfleet:" + filepath.Join(r.RepoPath, subPath)
}

func (r PlainRunner) baseEnv() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "RCLONE_CONFIG=") {
			env = append(env, entry)
		}
	}
	return append(env, "RCLONE_CONFIG="+r.RcloneConfPath)
}

func (r PlainRunner) rcloneBaseArgs() []string {
	args := []string{"--config", r.RcloneConfPath}
	for _, arg := range r.normalizedRcloneExtraArgs() {
		args = append(args, arg)
	}
	return args
}

func (r PlainRunner) normalizedRcloneExtraArgs() []string {
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

func (r PlainRunner) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "rclone", args...)
	cmd.Env = r.baseEnv()
	return cmd
}

// InitRepo pre-creates the remote directory for plain backup.
func (r PlainRunner) InitRepo(ctx context.Context) error {
	args := r.rcloneBaseArgs()
	args = append(args, "mkdir", r.remoteArg(""))
	cmd := r.command(ctx, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return commandError("create plain backup directory", stderr.String(), err)
	}
	return nil
}

// RunBackup copies each backup directory to the remote using rclone copy.
// A metadata marker file (.vaultfleet-backup-meta) is written with the
// current timestamp so that ListSnapshots can report a synthetic snapshot.
func (r PlainRunner) RunBackup(ctx context.Context, dirs []string, excludes []string) (string, error) {
	for _, dir := range dirs {
		if err := r.syncDir(ctx, dir, excludes); err != nil {
			return "", err
		}
	}
	return r.writeBackupMarker(ctx, dirs)
}

// RunBackupWithProgress is the same as RunBackup but reports progress
// via a callback. Since rclone copy does not provide structured JSON
// progress like restic, this implementation calls progressFn at the
// start and end of each directory.
func (r PlainRunner) RunBackupWithProgress(ctx context.Context, dirs []string, excludes []string, progressFn func(BackupProgress)) (string, error) {
	total := len(dirs)
	for i, dir := range dirs {
		if progressFn != nil {
			progressFn(BackupProgress{
				PercentDone: float64(i) / float64(total),
				TotalFiles:  int64(total),
				FilesDone:   int64(i),
				CurrentFile: dir,
			})
		}
		if err := r.syncDir(ctx, dir, excludes); err != nil {
			return "", err
		}
	}
	if progressFn != nil {
		progressFn(BackupProgress{
			PercentDone: 1.0,
			TotalFiles:  int64(total),
			FilesDone:   int64(total),
		})
	}
	return r.writeBackupMarker(ctx, dirs)
}

func (r PlainRunner) syncDir(ctx context.Context, dir string, excludes []string) error {
	args := r.rcloneBaseArgs()
	args = append(args, "sync", dir, r.remoteArg("data/"+filepath.Base(dir)))
	for _, exclude := range excludes {
		args = append(args, "--exclude", exclude)
	}
	cmd := r.command(ctx, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return commandError("rclone sync "+dir, stderr.String(), err)
	}
	return nil
}

func (r PlainRunner) writeBackupMarker(ctx context.Context, dirs []string) (string, error) {
	marker := fmt.Sprintf(`{"timestamp":"%s","dirs":%s}`,
		time.Now().UTC().Format(time.RFC3339),
		quoteJSONStrings(dirs))

	args := r.rcloneBaseArgs()
	args = append(args, "rcat", r.remoteArg(".vaultfleet-backup-meta"))
	cmd := r.command(ctx, args...)
	cmd.Stdin = strings.NewReader(marker)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", commandError("write backup marker", stderr.String(), err)
	}

	// Generate a deterministic snapshot ID from the timestamp.
	h := sha256.Sum256([]byte(marker))
	return hex.EncodeToString(h[:8]), nil
}

// RunForget is a no-op for plain backups (no snapshot retention).
func (r PlainRunner) RunForget(_ context.Context, _ RetentionPolicy) error {
	return nil
}

// Unlock is a no-op for plain backups (no restic repository, no locks).
func (r PlainRunner) Unlock(_ context.Context) error {
	return nil
}

// ListSnapshots returns a single synthetic snapshot representing the
// current state of the plain backup.
func (r PlainRunner) ListSnapshots(ctx context.Context) ([]SnapshotInfo, error) {
	args := r.rcloneBaseArgs()
	args = append(args, "cat", r.remoteArg(".vaultfleet-backup-meta"))
	cmd := r.command(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// If marker doesn't exist, return empty list (no backups yet).
		return nil, nil
	}

	var meta struct {
		Timestamp string   `json:"timestamp"`
		Dirs      []string `json:"dirs"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &meta); err != nil {
		return nil, fmt.Errorf("parse plain backup metadata: %w", err)
	}

	t, _ := time.Parse(time.RFC3339, meta.Timestamp)
	if t.IsZero() {
		t = time.Now().UTC()
	}

	h := sha256.Sum256([]byte(strings.TrimSpace(stdout.String())))
	snapID := hex.EncodeToString(h[:8])

	return []SnapshotInfo{{
		ID:       snapID,
		Time:     t,
		Paths:    meta.Dirs,
		Hostname: "",
	}}, nil
}

// RepositorySize returns the total size of the plain backup via rclone size.
func (r PlainRunner) RepositorySize(ctx context.Context) (int64, error) {
	args := r.rcloneBaseArgs()
	args = append(args, "size", r.remoteArg("data"), "--json")
	cmd := r.command(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return 0, commandError("rclone size", stderr.String(), err)
	}

	var sizeInfo struct {
		TotalBytes int64 `json:"bytes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &sizeInfo); err != nil {
		return 0, fmt.Errorf("parse rclone size JSON: %w", err)
	}
	return sizeInfo.TotalBytes, nil
}

// RestoreSnapshot copies all backed-up directories from the remote
// to the target path.
func (r PlainRunner) RestoreSnapshot(ctx context.Context, snapshotID, targetPath string, includePaths []string) error {
	// List remote data subdirectories.
	dataRemote := r.remoteArg("data")
	args := r.rcloneBaseArgs()
	args = append(args, "lsd", dataRemote)
	cmd := r.command(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return commandError("list plain backup dirs", stderr.String(), err)
	}

	var dirs []string
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// rclone lsd output format: "-1 2026-01-01 00:00:00        -1 dirname"
		parts := strings.Fields(line)
		if len(parts) > 0 {
			dirs = append(dirs, parts[len(parts)-1])
		}
	}

	if len(dirs) == 0 {
		// Fallback: try copying everything from data/
		dirs = []string{""}
	}

	for _, dir := range dirs {
		if len(includePaths) > 0 && !r.matchesIncludePaths(dir, includePaths) {
			continue
		}

		srcRemote := r.remoteArg("data/" + dir)
		destPath := filepath.Join(targetPath, dir)
		if dir == "" {
			srcRemote = dataRemote
			destPath = targetPath
		}

		copyArgs := r.rcloneBaseArgs()
		copyArgs = append(copyArgs, "copy", srcRemote, destPath)
		cpCmd := r.command(ctx, copyArgs...)
		var cpStderr bytes.Buffer
		cpCmd.Stderr = &cpStderr
		if err := cpCmd.Run(); err != nil {
			return commandError("rclone restore copy", cpStderr.String(), err)
		}
	}
	return nil
}

func (r PlainRunner) matchesIncludePaths(dirName string, includePaths []string) bool {
	for _, p := range includePaths {
		base := filepath.Base(p)
		if base == dirName {
			return true
		}
		// Also check if the include path is a prefix.
		if strings.HasPrefix(p, "/"+dirName) || strings.HasPrefix(p, dirName) {
			return true
		}
	}
	return false
}

// LsSnapshot lists files in the plain backup. Since plain mode has only
// one "snapshot", the snapshotID parameter is ignored.
func (r PlainRunner) LsSnapshot(ctx context.Context, snapshotID string, paths ...string) ([]SnapshotFileEntry, error) {
	subPath := ""
	if len(paths) > 0 {
		subPath = paths[0]
	}

	targetRemote := r.remoteArg("data/" + subPath)
	args := r.rcloneBaseArgs()
	args = append(args, "lsjson", targetRemote, "-R")
	cmd := r.command(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// If path doesn't exist, try the top-level data directory.
		if subPath != "" {
			targetRemote = r.remoteArg("data")
			args = r.rcloneBaseArgs()
			args = append(args, "lsjson", targetRemote, "-R")
			cmd = r.command(ctx, args...)
			stdout.Reset()
			stderr.Reset()
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				return nil, commandError("list plain backup files", stderr.String(), err)
			}
		} else {
			return nil, commandError("list plain backup files", stderr.String(), err)
		}
	}

	var items []struct {
		Path    string `json:"Path"`
		Size    int64  `json:"Size"`
		ModTime string `json:"ModTime"`
		IsDir   bool   `json:"IsDir"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &items); err != nil {
		return nil, fmt.Errorf("parse rclone lsjson: %w", err)
	}

	entries := make([]SnapshotFileEntry, 0, len(items))
	for _, item := range items {
		entryType := "file"
		if item.IsDir {
			entryType = "dir"
		}
		entries = append(entries, SnapshotFileEntry{
			Path:  item.Path,
			Type:  entryType,
			Size:  item.Size,
			Mtime: item.ModTime,
		})
	}
	return entries, nil
}

func quoteJSONStrings(strs []string) string {
	quoted := make([]string, len(strs))
	for i, s := range strs {
		quoted[i] = strconv.Quote(s)
	}
	return "[" + strings.Join(quoted, ",") + "]"
}
