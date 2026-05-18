package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

const (
	TypeHeartbeat        = "heartbeat"
	TypeDirBrowseReq     = "dir_browse_req"
	TypeDirBrowseResp    = "dir_browse_resp"
	TypePolicyPush       = "policy_push"
	TypePolicyAck        = "policy_ack"
	TypeBackupNow        = "backup_now"
	TypeTaskResult       = "task_result"
	TypeRestoreReq       = "restore_req"
	TypeRestoreProgress  = "restore_progress"
	TypeSnapshotListReq  = "snapshot_list_req"
	TypeSnapshotListResp = "snapshot_list_resp"
)

type Message struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Payload json.RawMessage `json:"payload"`
}

func NewMessage(msgType string, payload interface{}) (*Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, fmt.Errorf("generate message id: %w", err)
	}

	return &Message{
		Type:    msgType,
		ID:      hex.EncodeToString(idBytes),
		Payload: json.RawMessage(data),
	}, nil
}

func ParsePayload[T any](msg *Message) (*T, error) {
	var payload T
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

type HeartbeatPayload struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryPercent float64 `json:"memory_percent"`
	DiskPercent   float64 `json:"disk_percent"`
	ResticVersion string  `json:"restic_version"`
	RcloneVersion string  `json:"rclone_version"`
	Uptime        int64   `json:"uptime"`
}

type DirBrowseRespPayload struct {
	Path    string     `json:"path"`
	Entries []DirEntry `json:"entries"`
	Error   string     `json:"error,omitempty"`
}

type DirEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

type PolicyAckPayload struct {
	AgentID string `json:"agent_id"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

type TaskResultPayload struct {
	AgentID    string    `json:"agent_id"`
	TaskType   string    `json:"task_type"`
	Status     string    `json:"status"`
	SnapshotID string    `json:"snapshot_id,omitempty"`
	DurationMs int64     `json:"duration_ms"`
	RepoSize   int64     `json:"repo_size"`
	ErrorLog   string    `json:"error_log,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

type RestoreProgressPayload struct {
	AgentID       string  `json:"agent_id"`
	SnapshotID    string  `json:"snapshot_id"`
	FilesRestored int64   `json:"files_restored"`
	BytesRestored int64   `json:"bytes_restored"`
	Percent       float64 `json:"percent"`
}

type SnapshotListRespPayload struct {
	AgentID   string         `json:"agent_id"`
	Snapshots []SnapshotInfo `json:"snapshots"`
	Error     string         `json:"error,omitempty"`
}

type SnapshotInfo struct {
	ID    string    `json:"id"`
	Time  time.Time `json:"time"`
	Paths []string  `json:"paths"`
	Size  int64     `json:"size"`
}

type DirBrowseReqPayload struct {
	Path  string `json:"path"`
	Depth int    `json:"depth"`
}

type PolicyPushPayload struct {
	AgentID         string          `json:"agent_id"`
	Storage         StorageConfig   `json:"storage"`
	ResticPassword  string          `json:"restic_password"`
	BackupDirs      []string        `json:"backup_dirs"`
	ExcludePatterns []string        `json:"exclude_patterns"`
	Schedule        string          `json:"schedule"`
	Retention       RetentionPolicy `json:"retention"`
}

type StorageConfig struct {
	RcloneType   string            `json:"rclone_type"`
	RcloneConfig map[string]string `json:"rclone_config"`
	RepoPath     string            `json:"repo_path"`
}

type RetentionPolicy struct {
	KeepLast    int `json:"keep_last"`
	KeepDaily   int `json:"keep_daily"`
	KeepWeekly  int `json:"keep_weekly"`
	KeepMonthly int `json:"keep_monthly"`
}

type BackupNowPayload struct {
	AgentID string `json:"agent_id"`
}

type RestoreReqPayload struct {
	SnapshotID string `json:"snapshot_id"`
	Target     string `json:"target"`
}

type SnapshotListReqPayload struct {
	AgentID string `json:"agent_id"`
}
