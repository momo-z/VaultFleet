package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vaultfleet/internal/agent/executor"
	"vaultfleet/internal/agent/policy"
	agentscheduler "vaultfleet/internal/agent/scheduler"
	"vaultfleet/pkg/protocol"
)

func TestHandlePolicyPushSavesPolicyWritesConfigSchedulesBackupAndAcks(t *testing.T) {
	store := policy.NewStore(t.TempDir())
	configDir := filepath.Join(t.TempDir(), "config")
	scheduler := &recordingScheduler{}
	sent := &sentMessages{}
	var runnerCalls atomic.Int32
	var runnerConfig executor.ExecutorConfig
	handler := NewHandler(HandlerConfig{
		PolicyStore: store,
		ConfigDir:   configDir,
		Scheduler:   scheduler,
		SendFunc:    sent.send,
		BackupRunner: func(_ context.Context, cfg executor.ExecutorConfig) executor.TaskResult {
			runnerCalls.Add(1)
			runnerConfig = cfg
			return executor.TaskResult{Type: "backup", Status: "success", DurationMs: 25, SnapshotID: "snap-1"}
		},
	})
	msg, err := protocol.NewMessage(protocol.TypePolicyPush, protocol.PolicyPushPayload{
		AgentID: "agent-1",
		Storage: protocol.StorageConfig{
			RcloneType: "s3",
			RcloneConfig: map[string]string{
				"access_key_id": "key",
				"provider":      "Other",
			},
			RepoPath: "bucket/agent-1",
		},
		ResticPassword:  "secret-password",
		BackupDirs:      []string{"/srv"},
		ExcludePatterns: []string{"*.tmp"},
		Schedule:        "0 4 * * *",
		Retention:       protocol.RetentionPolicy{KeepLast: 3, KeepDaily: 7},
	})
	require.NoError(t, err)

	handler.Handle(*msg)

	stored, err := store.LoadPolicy()
	require.NoError(t, err)
	assert.Equal(t, "agent-1", stored.AgentID)
	assert.Equal(t, []string{"/srv"}, stored.BackupDirs)
	assert.Equal(t, "0 4 * * *", stored.Schedule)

	rcloneConf, err := os.ReadFile(filepath.Join(configDir, "rclone.conf"))
	require.NoError(t, err)
	assert.Contains(t, string(rcloneConf), "[vaultfleet]\n")
	assert.Contains(t, string(rcloneConf), "type = s3\n")
	assert.Contains(t, string(rcloneConf), "provider = Other\n")
	assertFileMode(t, filepath.Join(configDir, "rclone.conf"), 0o600)

	password, err := os.ReadFile(filepath.Join(configDir, ".restic-password"))
	require.NoError(t, err)
	assert.Equal(t, "secret-password", string(password))
	assertFileMode(t, filepath.Join(configDir, ".restic-password"), 0o600)

	require.Len(t, scheduler.updates, 1)
	assert.Equal(t, "agent-1", scheduler.updates[0].agentID)
	assert.Equal(t, "0 4 * * *", scheduler.updates[0].schedule)
	require.NotNil(t, scheduler.updates[0].fn)

	messages := sent.snapshot()
	require.Len(t, messages, 1)
	assert.Equal(t, protocol.TypePolicyAck, messages[0].Type)
	assert.Equal(t, msg.ID, messages[0].ID)
	ack, err := protocol.ParsePayload[protocol.PolicyAckPayload](&messages[0])
	require.NoError(t, err)
	assert.Equal(t, protocol.PolicyAckPayload{AgentID: "agent-1", Success: true}, *ack)

	scheduler.updates[0].fn()
	assert.Equal(t, int32(1), runnerCalls.Load())
	assert.Equal(t, executor.ExecutorConfig{
		ConfigDir:  configDir,
		RepoPath:   "bucket/agent-1",
		BackupDirs: []string{"/srv"},
		Excludes:   []string{"*.tmp"},
		Retention:  executor.RetentionPolicy{KeepLast: 3, KeepDaily: 7},
	}, runnerConfig)
	require.Len(t, sent.snapshot(), 2)
}

func TestHandlePolicyPushSendsFailureAckAndDoesNotScheduleWhenConfigWriteFails(t *testing.T) {
	store := policy.NewStore(t.TempDir())
	configDir := filepath.Join(t.TempDir(), "config-file")
	require.NoError(t, os.WriteFile(configDir, []byte("not a directory"), 0o600))
	scheduler := &recordingScheduler{}
	sent := &sentMessages{}
	handler := NewHandler(HandlerConfig{
		PolicyStore: store,
		ConfigDir:   configDir,
		Scheduler:   scheduler,
		SendFunc:    sent.send,
	})
	msg, err := protocol.NewMessage(protocol.TypePolicyPush, protocol.PolicyPushPayload{
		AgentID:        "agent-1",
		Storage:        protocol.StorageConfig{RcloneType: "s3", RcloneConfig: map[string]string{"provider": "Other"}},
		ResticPassword: "secret-password",
		Schedule:       "0 4 * * *",
	})
	require.NoError(t, err)

	handler.Handle(*msg)

	require.Empty(t, scheduler.updates)
	messages := sent.snapshot()
	require.Len(t, messages, 1)
	assert.Equal(t, protocol.TypePolicyAck, messages[0].Type)
	assert.Equal(t, msg.ID, messages[0].ID)
	ack, err := protocol.ParsePayload[protocol.PolicyAckPayload](&messages[0])
	require.NoError(t, err)
	assert.Equal(t, "agent-1", ack.AgentID)
	assert.False(t, ack.Success)
	assert.NotEmpty(t, ack.Error)
	_, err = store.LoadPolicy()
	require.Error(t, err)
	assert.True(t, os.IsNotExist(err))
}

func TestHandlePolicyPushSendsFailureAckWhenScheduleUpdateFails(t *testing.T) {
	store := policy.NewStore(t.TempDir())
	configDir := t.TempDir()
	scheduler := &recordingScheduler{err: errors.New("invalid cron")}
	sent := &sentMessages{}
	handler := NewHandler(HandlerConfig{
		PolicyStore: store,
		ConfigDir:   configDir,
		Scheduler:   scheduler,
		SendFunc:    sent.send,
	})
	msg, err := protocol.NewMessage(protocol.TypePolicyPush, protocol.PolicyPushPayload{
		AgentID:        "agent-1",
		Storage:        protocol.StorageConfig{RcloneType: "s3", RcloneConfig: map[string]string{"provider": "Other"}},
		ResticPassword: "secret-password",
		Schedule:       "not a cron",
	})
	require.NoError(t, err)

	handler.Handle(*msg)

	require.Empty(t, scheduler.updates)
	messages := sent.snapshot()
	require.Len(t, messages, 1)
	assert.Equal(t, protocol.TypePolicyAck, messages[0].Type)
	assert.Equal(t, msg.ID, messages[0].ID)
	ack, err := protocol.ParsePayload[protocol.PolicyAckPayload](&messages[0])
	require.NoError(t, err)
	assert.Equal(t, "agent-1", ack.AgentID)
	assert.False(t, ack.Success)
	assert.Equal(t, "invalid cron", ack.Error)
}

func TestHandlePolicyPushScheduleUpdateFailureRestoresExistingPolicyAndConfig(t *testing.T) {
	store := policy.NewStore(t.TempDir())
	configDir := t.TempDir()
	oldPolicy := &protocol.PolicyPushPayload{
		AgentID:        "agent-1",
		Storage:        protocol.StorageConfig{RcloneType: "s3", RcloneConfig: map[string]string{"provider": "Old"}, RepoPath: "old-repo"},
		ResticPassword: "old-secret",
		BackupDirs:     []string{"/old"},
		Schedule:       "0 1 * * *",
	}
	require.NoError(t, store.SavePolicy(oldPolicy))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "rclone.conf"), []byte("old rclone config"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, ".restic-password"), []byte("old-secret"), 0o600))
	scheduler := &recordingScheduler{updateErr: errors.New("schedule unavailable")}
	sent := &sentMessages{}
	handler := NewHandler(HandlerConfig{
		PolicyStore: store,
		ConfigDir:   configDir,
		Scheduler:   scheduler,
		SendFunc:    sent.send,
	})
	msg, err := protocol.NewMessage(protocol.TypePolicyPush, protocol.PolicyPushPayload{
		AgentID:        "agent-1",
		Storage:        protocol.StorageConfig{RcloneType: "s3", RcloneConfig: map[string]string{"provider": "New"}, RepoPath: "new-repo"},
		ResticPassword: "new-secret",
		BackupDirs:     []string{"/new"},
		Schedule:       "0 4 * * *",
	})
	require.NoError(t, err)

	handler.Handle(*msg)

	messages := sent.snapshot()
	require.Len(t, messages, 1)
	ack, err := protocol.ParsePayload[protocol.PolicyAckPayload](&messages[0])
	require.NoError(t, err)
	assert.Equal(t, "agent-1", ack.AgentID)
	assert.False(t, ack.Success)
	assert.Equal(t, "schedule unavailable", ack.Error)

	stored, err := store.LoadPolicy()
	require.NoError(t, err)
	assert.Equal(t, oldPolicy, stored)
	rcloneConf, err := os.ReadFile(filepath.Join(configDir, "rclone.conf"))
	require.NoError(t, err)
	assert.Equal(t, "old rclone config", string(rcloneConf))
	password, err := os.ReadFile(filepath.Join(configDir, ".restic-password"))
	require.NoError(t, err)
	assert.Equal(t, "old-secret", string(password))
}

func TestHandlePolicyPushScheduleUpdateFailureRemovesNewPolicyAndConfig(t *testing.T) {
	store := policy.NewStore(t.TempDir())
	configDir := t.TempDir()
	scheduler := &recordingScheduler{updateErr: errors.New("schedule unavailable")}
	sent := &sentMessages{}
	handler := NewHandler(HandlerConfig{
		PolicyStore: store,
		ConfigDir:   configDir,
		Scheduler:   scheduler,
		SendFunc:    sent.send,
	})
	msg, err := protocol.NewMessage(protocol.TypePolicyPush, protocol.PolicyPushPayload{
		AgentID:        "agent-1",
		Storage:        protocol.StorageConfig{RcloneType: "s3", RcloneConfig: map[string]string{"provider": "New"}, RepoPath: "new-repo"},
		ResticPassword: "new-secret",
		Schedule:       "0 4 * * *",
	})
	require.NoError(t, err)

	handler.Handle(*msg)

	messages := sent.snapshot()
	require.Len(t, messages, 1)
	ack, err := protocol.ParsePayload[protocol.PolicyAckPayload](&messages[0])
	require.NoError(t, err)
	assert.False(t, ack.Success)
	assert.Equal(t, "schedule unavailable", ack.Error)

	_, err = store.LoadPolicy()
	require.Error(t, err)
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(configDir, "rclone.conf"))
	require.Error(t, err)
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(configDir, ".restic-password"))
	require.Error(t, err)
	assert.True(t, os.IsNotExist(err))
}

func TestHandlePolicyPushInvalidSchedulePreservesExistingPolicyAndConfig(t *testing.T) {
	store := policy.NewStore(t.TempDir())
	configDir := t.TempDir()
	oldPolicy := &protocol.PolicyPushPayload{
		AgentID:        "agent-1",
		Storage:        protocol.StorageConfig{RcloneType: "s3", RcloneConfig: map[string]string{"provider": "Old"}, RepoPath: "old-repo"},
		ResticPassword: "old-secret",
		BackupDirs:     []string{"/old"},
		Schedule:       "0 1 * * *",
	}
	require.NoError(t, store.SavePolicy(oldPolicy))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "rclone.conf"), []byte("old rclone config"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, ".restic-password"), []byte("old-secret"), 0o600))
	sent := &sentMessages{}
	handler := NewHandler(HandlerConfig{
		PolicyStore: store,
		ConfigDir:   configDir,
		Scheduler:   agentscheduler.New(),
		SendFunc:    sent.send,
	})
	msg, err := protocol.NewMessage(protocol.TypePolicyPush, protocol.PolicyPushPayload{
		AgentID:        "agent-1",
		Storage:        protocol.StorageConfig{RcloneType: "s3", RcloneConfig: map[string]string{"provider": "New"}, RepoPath: "new-repo"},
		ResticPassword: "new-secret",
		BackupDirs:     []string{"/new"},
		Schedule:       "not a cron",
	})
	require.NoError(t, err)

	handler.Handle(*msg)

	messages := sent.snapshot()
	require.Len(t, messages, 1)
	ack, err := protocol.ParsePayload[protocol.PolicyAckPayload](&messages[0])
	require.NoError(t, err)
	assert.Equal(t, "agent-1", ack.AgentID)
	assert.False(t, ack.Success)
	assert.NotEmpty(t, ack.Error)

	stored, err := store.LoadPolicy()
	require.NoError(t, err)
	assert.Equal(t, oldPolicy, stored)
	rcloneConf, err := os.ReadFile(filepath.Join(configDir, "rclone.conf"))
	require.NoError(t, err)
	assert.Equal(t, "old rclone config", string(rcloneConf))
	password, err := os.ReadFile(filepath.Join(configDir, ".restic-password"))
	require.NoError(t, err)
	assert.Equal(t, "old-secret", string(password))
}

func TestHandlePolicyPushReplaceFailurePreservesExistingPolicyAndConfig(t *testing.T) {
	store := policy.NewStore(t.TempDir())
	configDir := t.TempDir()
	oldPolicy := &protocol.PolicyPushPayload{
		AgentID:        "agent-1",
		Storage:        protocol.StorageConfig{RcloneType: "s3", RcloneConfig: map[string]string{"provider": "Old"}, RepoPath: "old-repo"},
		ResticPassword: "old-secret",
		Schedule:       "0 1 * * *",
	}
	require.NoError(t, store.SavePolicy(oldPolicy))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "rclone.conf"), []byte("old rclone config"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(configDir, ".restic-password"), 0o700))
	scheduler := &recordingScheduler{}
	sent := &sentMessages{}
	handler := NewHandler(HandlerConfig{
		PolicyStore: store,
		ConfigDir:   configDir,
		Scheduler:   scheduler,
		SendFunc:    sent.send,
	})
	msg, err := protocol.NewMessage(protocol.TypePolicyPush, protocol.PolicyPushPayload{
		AgentID:        "agent-1",
		Storage:        protocol.StorageConfig{RcloneType: "s3", RcloneConfig: map[string]string{"provider": "New"}, RepoPath: "new-repo"},
		ResticPassword: "new-secret",
		Schedule:       "0 4 * * *",
	})
	require.NoError(t, err)

	handler.Handle(*msg)

	messages := sent.snapshot()
	require.Len(t, messages, 1)
	ack, err := protocol.ParsePayload[protocol.PolicyAckPayload](&messages[0])
	require.NoError(t, err)
	assert.False(t, ack.Success)
	assert.NotEmpty(t, ack.Error)
	assert.Empty(t, scheduler.updates)

	stored, err := store.LoadPolicy()
	require.NoError(t, err)
	assert.Equal(t, oldPolicy, stored)
	rcloneConf, err := os.ReadFile(filepath.Join(configDir, "rclone.conf"))
	require.NoError(t, err)
	assert.Equal(t, "old rclone config", string(rcloneConf))
}

func TestHandlePolicyPushReplacesLooseResticPasswordWithSecureFile(t *testing.T) {
	store := policy.NewStore(t.TempDir())
	configDir := t.TempDir()
	passwordPath := filepath.Join(configDir, ".restic-password")
	require.NoError(t, os.WriteFile(passwordPath, []byte("old-secret"), 0o644))
	oldInfo := fileInfo(t, passwordPath)
	sent := &sentMessages{}
	handler := NewHandler(HandlerConfig{
		PolicyStore: store,
		ConfigDir:   configDir,
		Scheduler:   &recordingScheduler{},
		SendFunc:    sent.send,
	})
	msg, err := protocol.NewMessage(protocol.TypePolicyPush, protocol.PolicyPushPayload{
		AgentID:        "agent-1",
		Storage:        protocol.StorageConfig{RcloneType: "s3", RcloneConfig: map[string]string{"provider": "Other"}},
		ResticPassword: "new-secret",
	})
	require.NoError(t, err)

	handler.Handle(*msg)

	messages := sent.snapshot()
	require.Len(t, messages, 1)
	ack, err := protocol.ParsePayload[protocol.PolicyAckPayload](&messages[0])
	require.NoError(t, err)
	require.True(t, ack.Success, ack.Error)
	assert.False(t, os.SameFile(oldInfo, fileInfo(t, passwordPath)))
	password, err := os.ReadFile(passwordPath)
	require.NoError(t, err)
	assert.Equal(t, "new-secret", string(password))
	assertFileMode(t, passwordPath, 0o600)
}

func TestHandleBackupNowLoadsPolicyRunsBackupAndSendsTaskResult(t *testing.T) {
	store := policy.NewStore(t.TempDir())
	configDir := t.TempDir()
	require.NoError(t, store.SavePolicy(&protocol.PolicyPushPayload{
		AgentID: "agent-1",
		Storage: protocol.StorageConfig{
			RepoPath: "repo/agent-1",
		},
		BackupDirs:      []string{"/srv", "/home"},
		ExcludePatterns: []string{"*.tmp"},
		Retention:       protocol.RetentionPolicy{KeepLast: 4, KeepWeekly: 2},
	}))
	sent := &sentMessages{}
	var runnerConfig executor.ExecutorConfig
	handler := NewHandler(HandlerConfig{
		PolicyStore: store,
		ConfigDir:   configDir,
		SendFunc:    sent.send,
		BackupRunner: func(_ context.Context, cfg executor.ExecutorConfig) executor.TaskResult {
			runnerConfig = cfg
			return executor.TaskResult{
				Type:       "backup",
				Status:     "success",
				SnapshotID: "snap-1",
				DurationMs: 1500,
				RepoSize:   4096,
			}
		},
	})
	msg, err := protocol.NewMessage(protocol.TypeBackupNow, protocol.BackupNowPayload{AgentID: "agent-1"})
	require.NoError(t, err)

	handler.Handle(*msg)

	assert.Equal(t, executor.ExecutorConfig{
		ConfigDir:  configDir,
		RepoPath:   "repo/agent-1",
		BackupDirs: []string{"/srv", "/home"},
		Excludes:   []string{"*.tmp"},
		Retention:  executor.RetentionPolicy{KeepLast: 4, KeepWeekly: 2},
	}, runnerConfig)
	messages := sent.snapshot()
	require.Len(t, messages, 1)
	assert.Equal(t, protocol.TypeTaskResult, messages[0].Type)
	result, err := protocol.ParsePayload[protocol.TaskResultPayload](&messages[0])
	require.NoError(t, err)
	assert.Equal(t, "agent-1", result.AgentID)
	assert.Equal(t, "backup", result.TaskType)
	assert.Equal(t, "success", result.Status)
	assert.Equal(t, "snap-1", result.SnapshotID)
	assert.Equal(t, int64(1500), result.DurationMs)
	assert.Equal(t, int64(4096), result.RepoSize)
	assert.False(t, result.StartedAt.IsZero())
	assert.Equal(t, result.StartedAt.Add(1500*time.Millisecond), result.FinishedAt)
}

func TestHandleBackupNowUsesConfiguredAgentIDWhenRequestAndPolicyOmitIt(t *testing.T) {
	store := policy.NewStore(t.TempDir())
	require.NoError(t, store.SavePolicy(&protocol.PolicyPushPayload{
		Storage: protocol.StorageConfig{RepoPath: "repo/agent-1"},
	}))
	sent := &sentMessages{}
	handler := NewHandler(HandlerConfig{
		PolicyStore: store,
		ConfigDir:   t.TempDir(),
		AgentID:     "agent-1",
		SendFunc:    sent.send,
		BackupRunner: func(context.Context, executor.ExecutorConfig) executor.TaskResult {
			return executor.TaskResult{Type: "backup", Status: "success", DurationMs: 10}
		},
	})
	msg, err := protocol.NewMessage(protocol.TypeBackupNow, protocol.BackupNowPayload{})
	require.NoError(t, err)

	handler.Handle(*msg)

	messages := sent.snapshot()
	require.Len(t, messages, 1)
	result, err := protocol.ParsePayload[protocol.TaskResultPayload](&messages[0])
	require.NoError(t, err)
	assert.Equal(t, "agent-1", result.AgentID)
}

func TestHandleBackupNowPreventsOverlappingRuns(t *testing.T) {
	store := policy.NewStore(t.TempDir())
	require.NoError(t, store.SavePolicy(&protocol.PolicyPushPayload{
		AgentID: "agent-1",
		Storage: protocol.StorageConfig{
			RepoPath: "repo/agent-1",
		},
	}))
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	done := make(chan struct{})
	sent := &sentMessages{}
	var calls atomic.Int32
	handler := NewHandler(HandlerConfig{
		PolicyStore: store,
		ConfigDir:   t.TempDir(),
		SendFunc:    sent.send,
		BackupRunner: func(context.Context, executor.ExecutorConfig) executor.TaskResult {
			calls.Add(1)
			started <- struct{}{}
			<-release
			return executor.TaskResult{Type: "backup", Status: "success", DurationMs: 10}
		},
	})
	msg, err := protocol.NewMessage(protocol.TypeBackupNow, protocol.BackupNowPayload{AgentID: "agent-1"})
	require.NoError(t, err)

	go func() {
		defer close(done)
		handler.Handle(*msg)
	}()
	require.Eventually(t, func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)

	handler.Handle(*msg)

	messages := sent.snapshot()
	require.Len(t, messages, 1)
	result, err := protocol.ParsePayload[protocol.TaskResultPayload](&messages[0])
	require.NoError(t, err)
	assert.Equal(t, "failed", result.Status)
	assert.Equal(t, "backup already running", result.ErrorLog)
	assert.Equal(t, int32(1), calls.Load())

	close(release)
	<-done

	messages = sent.snapshot()
	require.Len(t, messages, 2)
	result, err = protocol.ParsePayload[protocol.TaskResultPayload](&messages[1])
	require.NoError(t, err)
	assert.Equal(t, "success", result.Status)
	assert.Equal(t, int32(1), calls.Load())
}

func TestHandleBackupNowPersistsPendingResultWhenSendFails(t *testing.T) {
	store := policy.NewStore(t.TempDir())
	require.NoError(t, store.SavePolicy(&protocol.PolicyPushPayload{
		AgentID: "agent-1",
		Storage: protocol.StorageConfig{
			RepoPath: "repo/agent-1",
		},
	}))
	handler := NewHandler(HandlerConfig{
		PolicyStore: store,
		ConfigDir:   t.TempDir(),
		SendFunc: func(protocol.Message) error {
			return errors.New("offline")
		},
		BackupRunner: func(context.Context, executor.ExecutorConfig) executor.TaskResult {
			return executor.TaskResult{Type: "backup", Status: "success", DurationMs: 10, SnapshotID: "snap-1"}
		},
	})
	msg, err := protocol.NewMessage(protocol.TypeBackupNow, protocol.BackupNowPayload{AgentID: "agent-1"})
	require.NoError(t, err)

	handler.Handle(*msg)

	pending, err := store.LoadPendingResults()
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "agent-1", pending[0].AgentID)
	assert.Equal(t, "backup", pending[0].TaskType)
	assert.Equal(t, "success", pending[0].Status)
	assert.Equal(t, "snap-1", pending[0].SnapshotID)
}

func TestHandleBackupNowMissingPolicySendsFailureResult(t *testing.T) {
	store := policy.NewStore(t.TempDir())
	sent := &sentMessages{}
	handler := NewHandler(HandlerConfig{
		PolicyStore: store,
		ConfigDir:   t.TempDir(),
		AgentID:     "agent-1",
		SendFunc:    sent.send,
	})
	msg, err := protocol.NewMessage(protocol.TypeBackupNow, protocol.BackupNowPayload{AgentID: "agent-1"})
	require.NoError(t, err)

	handler.Handle(*msg)

	messages := sent.snapshot()
	require.Len(t, messages, 1)
	assert.Equal(t, protocol.TypeTaskResult, messages[0].Type)
	result, err := protocol.ParsePayload[protocol.TaskResultPayload](&messages[0])
	require.NoError(t, err)
	assert.Equal(t, "agent-1", result.AgentID)
	assert.Equal(t, "backup", result.TaskType)
	assert.Equal(t, "failed", result.Status)
	assert.True(t, strings.Contains(result.ErrorLog, "load policy"))
}

func TestHandleRestoreInvokesRunnerAndSendsSuccessTaskResult(t *testing.T) {
	store := policy.NewStore(t.TempDir())
	configDir := t.TempDir()
	require.NoError(t, store.SavePolicy(&protocol.PolicyPushPayload{
		AgentID: "agent-1",
		Storage: protocol.StorageConfig{
			RepoPath: "repo/agent-1",
		},
	}))
	sent := &sentMessages{}
	var runnerConfig executor.ExecutorConfig
	var runnerSnapshotID string
	var runnerTarget string
	handler := NewHandler(HandlerConfig{
		PolicyStore: store,
		ConfigDir:   configDir,
		SendFunc:    sent.send,
		RestoreRunner: func(_ context.Context, cfg executor.ExecutorConfig, snapshotID string, target string) error {
			runnerConfig = cfg
			runnerSnapshotID = snapshotID
			runnerTarget = target
			return nil
		},
	})
	msg, err := protocol.NewMessage(protocol.TypeRestoreReq, protocol.RestoreReqPayload{
		SnapshotID: "snap-1",
		Target:     "/restore/target",
	})
	require.NoError(t, err)

	handler.Handle(*msg)

	assert.Equal(t, executor.ExecutorConfig{ConfigDir: configDir, RepoPath: "repo/agent-1"}, runnerConfig)
	assert.Equal(t, "snap-1", runnerSnapshotID)
	assert.Equal(t, "/restore/target", runnerTarget)
	messages := sent.snapshot()
	require.Len(t, messages, 2)
	assert.Equal(t, protocol.TypeRestoreProgress, messages[0].Type)
	assert.Equal(t, msg.ID, messages[0].ID)
	progress, err := protocol.ParsePayload[protocol.RestoreProgressPayload](&messages[0])
	require.NoError(t, err)
	assert.Equal(t, "agent-1", progress.AgentID)
	assert.Equal(t, "snap-1", progress.SnapshotID)
	assert.Equal(t, float64(0), progress.Percent)

	assert.Equal(t, protocol.TypeTaskResult, messages[1].Type)
	result, err := protocol.ParsePayload[protocol.TaskResultPayload](&messages[1])
	require.NoError(t, err)
	assert.Equal(t, "agent-1", result.AgentID)
	assert.Equal(t, "restore", result.TaskType)
	assert.Equal(t, "success", result.Status)
	assert.Equal(t, "snap-1", result.SnapshotID)
	assert.Empty(t, result.ErrorLog)
	assert.False(t, result.StartedAt.IsZero())
	assert.False(t, result.FinishedAt.Before(result.StartedAt))
}

func TestHandleRestoreRunnerFailureSendsFailedTaskResult(t *testing.T) {
	store := policy.NewStore(t.TempDir())
	require.NoError(t, store.SavePolicy(&protocol.PolicyPushPayload{
		AgentID: "agent-1",
		Storage: protocol.StorageConfig{
			RepoPath: "repo/agent-1",
		},
	}))
	sent := &sentMessages{}
	handler := NewHandler(HandlerConfig{
		PolicyStore: store,
		ConfigDir:   t.TempDir(),
		SendFunc:    sent.send,
		RestoreRunner: func(context.Context, executor.ExecutorConfig, string, string) error {
			return errors.New("restore failed")
		},
	})
	msg, err := protocol.NewMessage(protocol.TypeRestoreReq, protocol.RestoreReqPayload{
		SnapshotID: "snap-1",
		Target:     "/restore/target",
	})
	require.NoError(t, err)

	handler.Handle(*msg)

	messages := sent.snapshot()
	require.Len(t, messages, 2)
	assert.Equal(t, protocol.TypeRestoreProgress, messages[0].Type)
	assert.Equal(t, msg.ID, messages[0].ID)
	progress, err := protocol.ParsePayload[protocol.RestoreProgressPayload](&messages[0])
	require.NoError(t, err)
	assert.Equal(t, "agent-1", progress.AgentID)
	assert.Equal(t, "snap-1", progress.SnapshotID)
	assert.Equal(t, float64(0), progress.Percent)

	assert.Equal(t, protocol.TypeTaskResult, messages[1].Type)
	result, err := protocol.ParsePayload[protocol.TaskResultPayload](&messages[1])
	require.NoError(t, err)
	assert.Equal(t, "restore", result.TaskType)
	assert.Equal(t, "failed", result.Status)
	assert.Equal(t, "snap-1", result.SnapshotID)
	assert.Contains(t, result.ErrorLog, "restore failed")
}

func TestHandleRestoreMissingPolicySendsFailedTaskResult(t *testing.T) {
	store := policy.NewStore(t.TempDir())
	sent := &sentMessages{}
	handler := NewHandler(HandlerConfig{
		PolicyStore: store,
		ConfigDir:   t.TempDir(),
		AgentID:     "agent-1",
		SendFunc:    sent.send,
	})
	msg, err := protocol.NewMessage(protocol.TypeRestoreReq, protocol.RestoreReqPayload{
		SnapshotID: "snap-1",
		Target:     "/restore/target",
	})
	require.NoError(t, err)

	handler.Handle(*msg)

	messages := sent.snapshot()
	require.Len(t, messages, 1)
	result, err := protocol.ParsePayload[protocol.TaskResultPayload](&messages[0])
	require.NoError(t, err)
	assert.Equal(t, "agent-1", result.AgentID)
	assert.Equal(t, "restore", result.TaskType)
	assert.Equal(t, "failed", result.Status)
	assert.Contains(t, result.ErrorLog, "load policy")
}

func TestHandleSnapshotListInvokesRunnerAndSendsResponseWithSameID(t *testing.T) {
	store := policy.NewStore(t.TempDir())
	configDir := t.TempDir()
	snapshotTime := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.SavePolicy(&protocol.PolicyPushPayload{
		AgentID: "agent-1",
		Storage: protocol.StorageConfig{
			RepoPath: "repo/agent-1",
		},
	}))
	sent := &sentMessages{}
	var runnerConfig executor.ExecutorConfig
	handler := NewHandler(HandlerConfig{
		PolicyStore: store,
		ConfigDir:   configDir,
		SendFunc:    sent.send,
		SnapshotListRunner: func(_ context.Context, cfg executor.ExecutorConfig) ([]executor.SnapshotInfo, error) {
			runnerConfig = cfg
			return []executor.SnapshotInfo{
				{ID: "snap-1", Time: snapshotTime, Paths: []string{"/etc"}, Size: 512},
			}, nil
		},
	})
	msg, err := protocol.NewMessage(protocol.TypeSnapshotListReq, protocol.SnapshotListReqPayload{AgentID: "agent-1"})
	require.NoError(t, err)

	handler.Handle(*msg)

	assert.Equal(t, executor.ExecutorConfig{ConfigDir: configDir, RepoPath: "repo/agent-1"}, runnerConfig)
	messages := sent.snapshot()
	require.Len(t, messages, 1)
	assert.Equal(t, protocol.TypeSnapshotListResp, messages[0].Type)
	assert.Equal(t, msg.ID, messages[0].ID)
	payload, err := protocol.ParsePayload[protocol.SnapshotListRespPayload](&messages[0])
	require.NoError(t, err)
	assert.Equal(t, "agent-1", payload.AgentID)
	assert.Empty(t, payload.Error)
	require.Len(t, payload.Snapshots, 1)
	assert.Equal(t, "snap-1", payload.Snapshots[0].ID)
	assert.True(t, payload.Snapshots[0].Time.Equal(snapshotTime))
	assert.Equal(t, []string{"/etc"}, payload.Snapshots[0].Paths)
	assert.Equal(t, int64(512), payload.Snapshots[0].Size)
}

func TestHandleSnapshotListMissingPolicySendsErrorPayload(t *testing.T) {
	store := policy.NewStore(t.TempDir())
	sent := &sentMessages{}
	handler := NewHandler(HandlerConfig{
		PolicyStore: store,
		ConfigDir:   t.TempDir(),
		AgentID:     "agent-1",
		SendFunc:    sent.send,
	})
	msg, err := protocol.NewMessage(protocol.TypeSnapshotListReq, protocol.SnapshotListReqPayload{AgentID: "agent-1"})
	require.NoError(t, err)

	handler.Handle(*msg)

	messages := sent.snapshot()
	require.Len(t, messages, 1)
	assert.Equal(t, protocol.TypeSnapshotListResp, messages[0].Type)
	assert.Equal(t, msg.ID, messages[0].ID)
	payload, err := protocol.ParsePayload[protocol.SnapshotListRespPayload](&messages[0])
	require.NoError(t, err)
	assert.Equal(t, "agent-1", payload.AgentID)
	assert.Contains(t, payload.Error, "load policy")
	assert.Nil(t, payload.Snapshots)
}

func TestHandlerDirBrowseReqSendsResponseWithSameID(t *testing.T) {
	var browsedPath string
	var browsedDepth int
	sent := make(chan protocol.Message, 1)
	handler := NewHandler(HandlerConfig{
		PolicyStore: policy.NewStore(""),
		BrowseFunc: func(fsRoot string, scanPath string, maxDepth int) ([]protocol.DirEntry, error) {
			assert.Equal(t, "/", fsRoot)
			browsedPath = scanPath
			browsedDepth = maxDepth
			return []protocol.DirEntry{{Path: "/etc", Type: "dir", Size: 4096}}, nil
		},
		SendFunc: func(msg protocol.Message) error {
			sent <- msg
			return nil
		},
	})
	req, err := protocol.NewMessage(protocol.TypeDirBrowseReq, protocol.DirBrowseReqPayload{Path: "/etc", Depth: 3})
	require.NoError(t, err)

	handler.Handle(*req)

	assert.Equal(t, "/etc", browsedPath)
	assert.Equal(t, 3, browsedDepth)
	resp := <-sent
	assert.Equal(t, protocol.TypeDirBrowseResp, resp.Type)
	assert.Equal(t, req.ID, resp.ID)
	payload, err := protocol.ParsePayload[protocol.DirBrowseRespPayload](&resp)
	require.NoError(t, err)
	assert.Equal(t, "/etc", payload.Path)
	assert.Empty(t, payload.Error)
	assert.Equal(t, []protocol.DirEntry{{Path: "/etc", Type: "dir", Size: 4096}}, payload.Entries)
}

func TestHandlerDirBrowseReqNormalizesInvalidDepth(t *testing.T) {
	var browsedDepth int
	handler := NewHandler(HandlerConfig{
		PolicyStore: policy.NewStore(""),
		BrowseFunc: func(_ string, _ string, maxDepth int) ([]protocol.DirEntry, error) {
			browsedDepth = maxDepth
			return nil, nil
		},
		SendFunc: func(protocol.Message) error {
			return nil
		},
	})
	rawPayload, err := json.Marshal(protocol.DirBrowseReqPayload{Path: "/var", Depth: 99})
	require.NoError(t, err)

	handler.Handle(protocol.Message{Type: protocol.TypeDirBrowseReq, ID: "browse-1", Payload: rawPayload})

	assert.Equal(t, 2, browsedDepth)
}

func TestHandlerDirBrowseReqSendsErrorPayload(t *testing.T) {
	sent := make(chan protocol.Message, 1)
	handler := NewHandler(HandlerConfig{
		PolicyStore: policy.NewStore(""),
		BrowseFunc: func(string, string, int) ([]protocol.DirEntry, error) {
			return nil, errors.New("permission denied")
		},
		SendFunc: func(msg protocol.Message) error {
			sent <- msg
			return nil
		},
	})
	req, err := protocol.NewMessage(protocol.TypeDirBrowseReq, protocol.DirBrowseReqPayload{Path: "/root", Depth: 2})
	require.NoError(t, err)

	handler.Handle(*req)

	resp := <-sent
	payload, err := protocol.ParsePayload[protocol.DirBrowseRespPayload](&resp)
	require.NoError(t, err)
	assert.Equal(t, "/root", payload.Path)
	assert.Equal(t, "permission denied", payload.Error)
	assert.Nil(t, payload.Entries)
}

type scheduledUpdate struct {
	agentID  string
	schedule string
	fn       func()
}

type recordingScheduler struct {
	updates     []scheduledUpdate
	removed     []string
	err         error
	validateErr error
	updateErr   error
}

func (s *recordingScheduler) Validate(string) error {
	if s.validateErr != nil {
		return s.validateErr
	}
	return s.err
}

func (s *recordingScheduler) UpdateSchedule(agentID string, schedule string, fn func()) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	if s.err != nil {
		return s.err
	}
	s.updates = append(s.updates, scheduledUpdate{agentID: agentID, schedule: schedule, fn: fn})
	return nil
}

func (s *recordingScheduler) RemoveJob(agentID string) {
	s.removed = append(s.removed, agentID)
}

type sentMessages struct {
	mu       sync.Mutex
	messages []protocol.Message
}

func (s *sentMessages) send(msg protocol.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, msg)
	return nil
}

func (s *sentMessages) snapshot() []protocol.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]protocol.Message(nil), s.messages...)
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, want, info.Mode().Perm())
}

func fileInfo(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info
}
