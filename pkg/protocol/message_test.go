package protocol

import (
	"encoding/json"
	"testing"
	"time"
	"unicode"

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
		Capabilities:  []string{CapabilitySnapshotBrowse, CapabilityRestoreIncludePaths},
		Uptime:        86400,
	}

	msg, err := NewMessage(TypeHeartbeat, hb)
	require.NoError(t, err)
	assert.Equal(t, TypeHeartbeat, msg.Type)
	assertHexMessageID(t, msg.ID)

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
	assert.Equal(t, []string{CapabilitySnapshotBrowse, CapabilityRestoreIncludePaths}, parsed.Capabilities)
}

func TestNewMessage_GeneratesUniqueHexIDs(t *testing.T) {
	const count = 16
	seen := make(map[string]bool, count)

	for i := 0; i < count; i++ {
		msg, err := NewMessage(TypeHeartbeat, HeartbeatPayload{})
		require.NoError(t, err)
		assertHexMessageID(t, msg.ID)
		assert.False(t, seen[msg.ID], "duplicate message ID: %s", msg.ID)
		seen[msg.ID] = true
	}
}

func TestPolicyPushPayload(t *testing.T) {
	policy := PolicyPushPayload{
		AgentID: "agent-001",
		Storage: StorageConfig{
			RcloneType:         "s3",
			RclonePassObscured: true,
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
	assert.True(t, parsed.Storage.RclonePassObscured)
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

func TestStorageConfigRcloneArgsOmitsWhenNil(t *testing.T) {
	storage := StorageConfig{
		RcloneType:   "s3",
		RcloneConfig: map[string]string{"provider": "Cloudflare"},
		RepoPath:     "vaultfleet/agent-001",
	}

	data, err := json.Marshal(storage)
	require.NoError(t, err)

	var fields map[string]any
	require.NoError(t, json.Unmarshal(data, &fields))
	assert.NotContains(t, fields, "rclone_args")
}

func TestStorageConfigRcloneArgsIncludesWhenSet(t *testing.T) {
	storage := StorageConfig{
		RcloneType:   "s3",
		RcloneConfig: map[string]string{"provider": "Cloudflare"},
		RepoPath:     "vaultfleet/agent-001",
		RcloneArgs: map[string]string{
			"transfers": "8",
			"s3-acl":    "private",
		},
	}

	data, err := json.Marshal(storage)
	require.NoError(t, err)

	var fields map[string]any
	require.NoError(t, json.Unmarshal(data, &fields))
	assert.Equal(t, map[string]any{
		"transfers": "8",
		"s3-acl":    "private",
	}, fields["rclone_args"])
}

func TestPolicyAckPayload(t *testing.T) {
	ack := PolicyAckPayload{
		AgentID: "agent-001",
		Success: false,
		Error:   "invalid schedule",
	}

	_, parsed := roundTripPayload[PolicyAckPayload](t, TypePolicyAck, ack)
	assert.Equal(t, "agent-001", parsed.AgentID)
	assert.False(t, parsed.Success)
	assert.Equal(t, "invalid schedule", parsed.Error)
}

func TestBackupNowPayload(t *testing.T) {
	backupNow := BackupNowPayload{AgentID: "agent-002"}

	_, parsed := roundTripPayload[BackupNowPayload](t, TypeBackupNow, backupNow)
	assert.Equal(t, "agent-002", parsed.AgentID)
}

func TestBackupProgressPayloadMarshalsExpectedKeys(t *testing.T) {
	progress := BackupProgressPayload{
		AgentID:     "agent-002",
		Phase:       "uploading",
		PercentDone: 42.5,
		TotalFiles:  100,
		FilesDone:   42,
		TotalBytes:  104857600,
		BytesDone:   44564480,
		BytesPerSec: 524288,
		CurrentFile: "/var/lib/app/data.db",
	}

	msg, parsed := roundTripPayload[BackupProgressPayload](t, TypeBackupProgress, progress)
	assert.Equal(t, TypeBackupProgress, msg.Type)
	assert.Equal(t, progress, *parsed)

	data, err := json.Marshal(progress)
	require.NoError(t, err)

	var fields map[string]any
	require.NoError(t, json.Unmarshal(data, &fields))
	assert.Contains(t, fields, "agent_id")
	assert.Contains(t, fields, "phase")
	assert.Contains(t, fields, "percent_done")
	assert.Contains(t, fields, "total_files")
	assert.Contains(t, fields, "files_done")
	assert.Contains(t, fields, "total_bytes")
	assert.Contains(t, fields, "bytes_done")
	assert.Contains(t, fields, "bytes_per_sec")
	assert.Contains(t, fields, "current_file")
}

func TestCancelTaskPayloadMarshal(t *testing.T) {
	payload := CancelTaskPayload{
		AgentID:   "agent-1",
		MessageID: "msg-abc123",
	}

	msg, parsed := roundTripPayload[CancelTaskPayload](t, TypeCancelTask, payload)
	assert.Equal(t, TypeCancelTask, msg.Type)
	assert.Equal(t, payload, *parsed)
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
		Snapshots: []SnapshotInfo{
			{ID: "abc123def456", Time: finishedAt, Paths: []string{"/etc"}, Size: 4096},
		},
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
	require.Len(t, parsed.Snapshots, 1)
	assert.Equal(t, "abc123def456", parsed.Snapshots[0].ID)
	assert.True(t, parsed.Snapshots[0].Time.Equal(finishedAt))
	assert.Equal(t, []string{"/etc"}, parsed.Snapshots[0].Paths)
	assert.Equal(t, int64(4096), parsed.Snapshots[0].Size)
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

func TestSnapshotListReqPayload(t *testing.T) {
	req := SnapshotListReqPayload{AgentID: "agent-003"}

	_, parsed := roundTripPayload[SnapshotListReqPayload](t, TypeSnapshotListReq, req)
	assert.Equal(t, "agent-003", parsed.AgentID)
}

func TestCollectLogsPayloadRoundTrip(t *testing.T) {
	req, parsedReq := roundTripPayload[CollectLogsReqPayload](t, TypeCollectLogsReq, CollectLogsReqPayload{MaxBytes: 1024})
	assert.Equal(t, TypeCollectLogsReq, req.Type)
	assert.Equal(t, 1024, parsedReq.MaxBytes)

	respPayload := CollectLogsRespPayload{
		Logs:  "line1\nline2\n",
		Error: "partial collection failed",
	}
	resp, parsedResp := roundTripPayload[CollectLogsRespPayload](t, TypeCollectLogsResp, respPayload)
	assert.Equal(t, TypeCollectLogsResp, resp.Type)
	assert.Equal(t, respPayload, *parsedResp)
}

func TestRestorePayloads(t *testing.T) {
	reqPayload := RestoreReqPayload{
		SnapshotID:   "abc123",
		Target:       "/restore/20260518",
		IncludePaths: []string{"/etc/hosts", "/var/log/app.log"},
	}

	_, parsedReq := roundTripPayload[RestoreReqPayload](t, TypeRestoreReq, reqPayload)
	assert.Equal(t, "abc123", parsedReq.SnapshotID)
	assert.Equal(t, "/restore/20260518", parsedReq.Target)
	assert.Equal(t, []string{"/etc/hosts", "/var/log/app.log"}, parsedReq.IncludePaths)

	_, parsedSelectiveReq := roundTripPayload[RestoreReqPayload](t, TypeSelectiveRestoreReq, reqPayload)
	assert.Equal(t, reqPayload, *parsedSelectiveReq)

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

func TestDirSizeRoundTrip(t *testing.T) {
	reqPayload := DirSizeReqPayload{Path: "/home/data"}

	_, parsedReq := roundTripPayload[DirSizeReqPayload](t, TypeDirSizeReq, reqPayload)
	assert.Equal(t, "/home/data", parsedReq.Path)

	respPayload := DirSizeRespPayload{
		Path: "/home/data",
		Size: 1073741824,
	}

	_, parsedResp := roundTripPayload[DirSizeRespPayload](t, TypeDirSizeResp, respPayload)
	assert.Equal(t, "/home/data", parsedResp.Path)
	assert.Equal(t, int64(1073741824), parsedResp.Size)
	assert.Empty(t, parsedResp.Error)
}

func TestVersionInfoRoundTrip(t *testing.T) {
	payload := VersionInfoPayload{
		Version:    "v0.5.0",
		GitHubRepo: "momo-z/VaultFleet",
	}

	_, parsed := roundTripPayload[VersionInfoPayload](t, TypeVersionInfo, payload)
	assert.Equal(t, "v0.5.0", parsed.Version)
	assert.Equal(t, "momo-z/VaultFleet", parsed.GitHubRepo)
}

func TestUpdateAgentRoundTrip(t *testing.T) {
	payload := UpdateAgentPayload{
		Version:    "v0.5.0",
		GitHubRepo: "momo-z/VaultFleet",
	}

	_, parsed := roundTripPayload[UpdateAgentPayload](t, TypeUpdateAgent, payload)
	assert.Equal(t, "v0.5.0", parsed.Version)
	assert.Equal(t, "momo-z/VaultFleet", parsed.GitHubRepo)
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
		TypeSelectiveRestoreReq,
		TypeRestoreProgress,
		TypeSnapshotListReq,
		TypeSnapshotListResp,
		TypeSnapshotBrowseReq,
		TypeSnapshotBrowseResp,
		TypeCollectLogsReq,
		TypeCollectLogsResp,
		TypeDirSizeReq,
		TypeDirSizeResp,
		TypeVersionInfo,
		TypeUpdateAgent,
		TypeBackupProgress,
		TypeCancelTask,
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
		"selective_restore_req",
		"restore_progress",
		"snapshot_list_req",
		"snapshot_list_resp",
		"snapshot_browse_req",
		"snapshot_browse_resp",
		"collect_logs_req",
		"collect_logs_resp",
		"dir_size_req",
		"dir_size_resp",
		"version_info",
		"update_agent",
		"backup_progress",
		"cancel_task",
	}

	assert.Equal(t, expected, types)
	assert.Len(t, types, 22)
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
	assertHexMessageID(t, decoded.ID)

	parsed, err := ParsePayload[T](&decoded)
	require.NoError(t, err)

	return &decoded, parsed
}

func assertHexMessageID(t *testing.T, id string) {
	t.Helper()

	require.Len(t, id, 32)
	for _, r := range id {
		assert.Truef(t, unicode.Is(unicode.ASCII_Hex_Digit, r), "message ID contains non-hex character: %q", r)
	}
}
