package protocol

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessageMarshalUnmarshal(t *testing.T) {
	hb := HeartbeatPayload{
		CPUPercent:    45.5,
		MemoryPercent: 72.3,
		DiskPercent:   30.0,
		ResticVersion: "0.16.0",
		RcloneVersion: "1.65.0",
		Uptime:        86400,
	}

	msg, err := NewMessage(TypeHeartbeat, hb)
	require.NoError(t, err)
	assert.Equal(t, TypeHeartbeat, msg.Type)
	assert.NotEmpty(t, msg.ID)

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded Message
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, TypeHeartbeat, decoded.Type)
	assert.Equal(t, msg.ID, decoded.ID)

	parsed, err := ParsePayload[HeartbeatPayload](&decoded)
	require.NoError(t, err)
	assert.InDelta(t, 45.5, parsed.CPUPercent, 0.01)
	assert.Equal(t, "0.16.0", parsed.ResticVersion)
}

func TestPolicyPushPayload(t *testing.T) {
	policy := PolicyPushPayload{
		AgentID: "agent-001",
		Storage: StorageConfig{
			RcloneType: "s3",
			RcloneConfig: map[string]string{
				"provider":          "Cloudflare",
				"access_key_id":     "AKID",
				"secret_access_key": "SECRET",
				"endpoint":          "https://xxx.r2.cloudflarestorage.com",
				"bucket":            "backups",
			},
			RepoPath: "vaultfleet/agent-001",
		},
		ResticPassword:  "secure-password",
		BackupDirs:      []string{"/etc", "/home", "/opt/myapp/data"},
		ExcludePatterns: []string{"*.log", "*.tmp", "node_modules"},
		Schedule:        "0 3 * * *",
		Retention: RetentionPolicy{
			KeepLast:    3,
			KeepDaily:   7,
			KeepWeekly:  4,
			KeepMonthly: 6,
		},
	}

	_, parsed := roundTripPayload[PolicyPushPayload](t, TypePolicyPush, policy)
	assert.Equal(t, "agent-001", parsed.AgentID)
	assert.Equal(t, "s3", parsed.Storage.RcloneType)
	assert.Equal(t, "Cloudflare", parsed.Storage.RcloneConfig["provider"])
	assert.Equal(t, "AKID", parsed.Storage.RcloneConfig["access_key_id"])
	assert.Equal(t, "SECRET", parsed.Storage.RcloneConfig["secret_access_key"])
	assert.Equal(t, "https://xxx.r2.cloudflarestorage.com", parsed.Storage.RcloneConfig["endpoint"])
	assert.Equal(t, "backups", parsed.Storage.RcloneConfig["bucket"])
	assert.Equal(t, "vaultfleet/agent-001", parsed.Storage.RepoPath)
	assert.Equal(t, "secure-password", parsed.ResticPassword)
	assert.Equal(t, []string{"/etc", "/home", "/opt/myapp/data"}, parsed.BackupDirs)
	assert.Equal(t, []string{"*.log", "*.tmp", "node_modules"}, parsed.ExcludePatterns)
	assert.Equal(t, "0 3 * * *", parsed.Schedule)
	assert.Equal(t, 3, parsed.Retention.KeepLast)
	assert.Equal(t, 7, parsed.Retention.KeepDaily)
	assert.Equal(t, 4, parsed.Retention.KeepWeekly)
	assert.Equal(t, 6, parsed.Retention.KeepMonthly)
}

func TestTaskResultPayload(t *testing.T) {
	finishedAt := time.Date(2026, 5, 18, 9, 30, 45, 0, time.UTC)
	startedAt := finishedAt.Add(-45 * time.Second)
	result := TaskResultPayload{
		AgentID:    "agent-002",
		TaskType:   "backup",
		Status:     "success",
		SnapshotID: "abc123def456",
		DurationMs: 45000,
		RepoSize:   1073741824,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	}

	_, parsed := roundTripPayload[TaskResultPayload](t, TypeTaskResult, result)
	assert.Equal(t, "agent-002", parsed.AgentID)
	assert.Equal(t, "backup", parsed.TaskType)
	assert.Equal(t, "success", parsed.Status)
	assert.Equal(t, "abc123def456", parsed.SnapshotID)
	assert.Equal(t, int64(45000), parsed.DurationMs)
	assert.Equal(t, int64(1073741824), parsed.RepoSize)
	assert.True(t, parsed.StartedAt.Equal(startedAt))
	assert.True(t, parsed.FinishedAt.Equal(finishedAt))
}

func TestDirBrowseRoundTrip(t *testing.T) {
	reqPayload := DirBrowseReqPayload{Path: "/home", Depth: 2}

	_, parsedReq := roundTripPayload[DirBrowseReqPayload](t, TypeDirBrowseReq, reqPayload)
	assert.Equal(t, "/home", parsedReq.Path)
	assert.Equal(t, 2, parsedReq.Depth)

	resp := DirBrowseRespPayload{
		Path: "/home",
		Entries: []DirEntry{
			{Path: "/home/user1", Type: "dir", Size: 1048576},
			{Path: "/home/user2", Type: "dir", Size: 2097152},
		},
	}

	_, parsedResp := roundTripPayload[DirBrowseRespPayload](t, TypeDirBrowseResp, resp)
	require.Len(t, parsedResp.Entries, 2)
	assert.Equal(t, "/home", parsedResp.Path)
	assert.Equal(t, "/home/user1", parsedResp.Entries[0].Path)
	assert.Equal(t, "dir", parsedResp.Entries[0].Type)
	assert.Equal(t, int64(1048576), parsedResp.Entries[0].Size)
	assert.Equal(t, "/home/user2", parsedResp.Entries[1].Path)
	assert.Equal(t, "dir", parsedResp.Entries[1].Type)
	assert.Equal(t, int64(2097152), parsedResp.Entries[1].Size)
}

func TestSnapshotListRoundTrip(t *testing.T) {
	firstSnapshotAt := time.Date(2026, 5, 18, 8, 0, 0, 0, time.UTC)
	secondSnapshotAt := firstSnapshotAt.Add(-24 * time.Hour)
	resp := SnapshotListRespPayload{
		AgentID: "agent-003",
		Snapshots: []SnapshotInfo{
			{ID: "snap1", Time: firstSnapshotAt, Paths: []string{"/etc", "/home"}, Size: 500000},
			{ID: "snap2", Time: secondSnapshotAt, Paths: []string{"/etc"}, Size: 300000},
		},
	}

	_, parsed := roundTripPayload[SnapshotListRespPayload](t, TypeSnapshotListResp, resp)
	require.Len(t, parsed.Snapshots, 2)
	assert.Equal(t, "agent-003", parsed.AgentID)
	assert.Equal(t, "snap1", parsed.Snapshots[0].ID)
	assert.True(t, parsed.Snapshots[0].Time.Equal(firstSnapshotAt))
	assert.Equal(t, []string{"/etc", "/home"}, parsed.Snapshots[0].Paths)
	assert.Equal(t, int64(500000), parsed.Snapshots[0].Size)
	assert.Equal(t, "snap2", parsed.Snapshots[1].ID)
	assert.True(t, parsed.Snapshots[1].Time.Equal(secondSnapshotAt))
	assert.Equal(t, []string{"/etc"}, parsed.Snapshots[1].Paths)
	assert.Equal(t, int64(300000), parsed.Snapshots[1].Size)
}

func TestRestorePayloads(t *testing.T) {
	reqPayload := RestoreReqPayload{SnapshotID: "abc123", Target: "/restore/20260518"}

	_, parsedReq := roundTripPayload[RestoreReqPayload](t, TypeRestoreReq, reqPayload)
	assert.Equal(t, "abc123", parsedReq.SnapshotID)
	assert.Equal(t, "/restore/20260518", parsedReq.Target)

	progress := RestoreProgressPayload{
		AgentID:       "agent-001",
		SnapshotID:    "abc123",
		FilesRestored: 1500,
		BytesRestored: 104857600,
		Percent:       75.5,
	}

	_, parsedProgress := roundTripPayload[RestoreProgressPayload](t, TypeRestoreProgress, progress)
	assert.Equal(t, "agent-001", parsedProgress.AgentID)
	assert.Equal(t, "abc123", parsedProgress.SnapshotID)
	assert.Equal(t, int64(1500), parsedProgress.FilesRestored)
	assert.Equal(t, int64(104857600), parsedProgress.BytesRestored)
	assert.InDelta(t, 75.5, parsedProgress.Percent, 0.01)
}

func TestAllMessageTypeConstants(t *testing.T) {
	types := []string{
		TypeHeartbeat,
		TypeDirBrowseReq,
		TypeDirBrowseResp,
		TypePolicyPush,
		TypePolicyAck,
		TypeBackupNow,
		TypeTaskResult,
		TypeRestoreReq,
		TypeRestoreProgress,
		TypeSnapshotListReq,
		TypeSnapshotListResp,
	}
	expected := []string{
		"heartbeat",
		"dir_browse_req",
		"dir_browse_resp",
		"policy_push",
		"policy_ack",
		"backup_now",
		"task_result",
		"restore_req",
		"restore_progress",
		"snapshot_list_req",
		"snapshot_list_resp",
	}

	assert.Equal(t, expected, types)
	assert.Len(t, types, 11)
	seen := make(map[string]bool)
	for _, typ := range types {
		assert.NotEmpty(t, typ)
		assert.False(t, seen[typ], "duplicate type constant: %s", typ)
		seen[typ] = true
	}
}

func TestNewMessage_InvalidPayload(t *testing.T) {
	_, err := NewMessage(TypeHeartbeat, make(chan int))
	assert.Error(t, err)
}

func TestParsePayload_InvalidJSON(t *testing.T) {
	msg := &Message{
		Type:    TypeHeartbeat,
		ID:      "test",
		Payload: json.RawMessage(`{invalid json`),
	}
	_, err := ParsePayload[HeartbeatPayload](msg)
	assert.Error(t, err)
}

func roundTripPayload[T any](t *testing.T, msgType string, payload interface{}) (*Message, *T) {
	t.Helper()

	msg, err := NewMessage(msgType, payload)
	require.NoError(t, err)

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded Message
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)
	require.Equal(t, msgType, decoded.Type)
	require.Equal(t, msg.ID, decoded.ID)

	parsed, err := ParsePayload[T](&decoded)
	require.NoError(t, err)

	return &decoded, parsed
}
