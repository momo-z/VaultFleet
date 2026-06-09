package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Message type constants identify WebSocket payload kinds exchanged by master and agents.
const (
	TypeHeartbeat           = "heartbeat"
	TypeDirBrowseReq        = "dir_browse_req"
	TypeDirBrowseResp       = "dir_browse_resp"
	TypePolicyPush          = "policy_push"
	TypePolicyAck           = "policy_ack"
	TypeBackupNow           = "backup_now"
	TypeTaskResult          = "task_result"
	TypeRestoreReq          = "restore_req"
	TypeSelectiveRestoreReq = "selective_restore_req"
	TypeRestoreProgress     = "restore_progress"
	TypeSnapshotListReq     = "snapshot_list_req"
	TypeSnapshotListResp    = "snapshot_list_resp"
	TypeSnapshotBrowseReq   = "snapshot_browse_req"
	TypeSnapshotBrowseResp  = "snapshot_browse_resp"
	TypeCollectLogsReq      = "collect_logs_req"
	TypeCollectLogsResp     = "collect_logs_resp"
	TypeDirSizeReq          = "dir_size_req"
	TypeDirSizeResp         = "dir_size_resp"
	TypeVersionInfo         = "version_info"
	TypeUpdateAgent         = "update_agent"
	TypeBackupProgress      = "backup_progress"
	TypeCancelTask          = "cancel_task"
)

const (
	CapabilitySnapshotBrowse            = "snapshot_browse"
	CapabilityRestoreIncludePaths       = "restore_include_paths"
	CapabilityPolicyPlaintextRclonePass = "policy_plaintext_rclone_pass"
)

// Message is the shared WebSocket envelope used by master and agents.
type Message struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Payload json.RawMessage `json:"payload"`
}

// NewMessage wraps a typed payload in a Message and assigns a random 16-byte hex ID.
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

// ParsePayload unmarshals a message payload into the requested payload type.
func ParsePayload[T any](msg *Message) (*T, error) {
	var payload T
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

// HeartbeatPayload reports agent health and installed backup tool versions.
type HeartbeatPayload struct {
	CPUPercent    float64  `json:"cpu_percent"`
	MemoryPercent float64  `json:"memory_percent"`
	DiskPercent   float64  `json:"disk_percent"`
	ResticVersion string   `json:"restic_version"`
	RcloneVersion string   `json:"rclone_version"`
	AgentVersion  string   `json:"agent_version,omitempty"`
	Capabilities  []string `json:"capabilities,omitempty"`
	Uptime        int64    `json:"uptime"`
}

// DirBrowseRespPayload returns directory entries for a browse request.
type DirBrowseRespPayload struct {
	Path    string     `json:"path"`
	Entries []DirEntry `json:"entries"`
	Error   string     `json:"error,omitempty"`
}

// DirEntry describes one file-system entry returned by directory browsing.
type DirEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

// PolicyAckPayload acknowledges whether an agent accepted a pushed policy.
type PolicyAckPayload struct {
	AgentID string `json:"agent_id"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// TaskResultPayload reports completion metadata for backup, restore, or maintenance work.
type TaskResultPayload struct {
	AgentID    string         `json:"agent_id"`
	TaskType   string         `json:"task_type"`
	Status     string         `json:"status"`
	SnapshotID string         `json:"snapshot_id,omitempty"`
	DurationMs int64          `json:"duration_ms"`
	RepoSize   int64          `json:"repo_size"`
	ErrorLog   string         `json:"error_log,omitempty"`
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt time.Time      `json:"finished_at"`
	Snapshots  []SnapshotInfo `json:"snapshots,omitempty"`
}

// BackupProgressPayload reports incremental backup progress from an agent.
type BackupProgressPayload struct {
	AgentID     string  `json:"agent_id"`
	Phase       string  `json:"phase"`
	PercentDone float64 `json:"percent_done"`
	TotalFiles  int64   `json:"total_files"`
	FilesDone   int64   `json:"files_done"`
	TotalBytes  int64   `json:"total_bytes"`
	BytesDone   int64   `json:"bytes_done"`
	BytesPerSec int64   `json:"bytes_per_sec"`
	CurrentFile string  `json:"current_file"`
}

// CancelTaskPayload requests cancellation of a running agent task by message ID.
type CancelTaskPayload struct {
	AgentID   string `json:"agent_id"`
	MessageID string `json:"message_id"`
}

// RestoreProgressPayload reports incremental restore progress from an agent.
type RestoreProgressPayload struct {
	AgentID       string  `json:"agent_id"`
	SnapshotID    string  `json:"snapshot_id"`
	FilesRestored int64   `json:"files_restored"`
	BytesRestored int64   `json:"bytes_restored"`
	Percent       float64 `json:"percent"`
}

// SnapshotListRespPayload returns snapshots known to an agent repository.
type SnapshotListRespPayload struct {
	AgentID   string         `json:"agent_id"`
	Snapshots []SnapshotInfo `json:"snapshots"`
	Error     string         `json:"error,omitempty"`
}

// SnapshotInfo describes one restic snapshot.
type SnapshotInfo struct {
	ID    string    `json:"id"`
	Time  time.Time `json:"time"`
	Paths []string  `json:"paths"`
	Size  int64     `json:"size"`
}

// DirBrowseReqPayload requests a bounded directory listing from an agent.
type DirBrowseReqPayload struct {
	Path  string `json:"path"`
	Depth int    `json:"depth"`
}

type DirSizeReqPayload struct {
	Path string `json:"path"`
}

type DirSizeRespPayload struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Error string `json:"error,omitempty"`
}

// PolicyPushPayload contains the full backup policy sent from master to agent.
type PolicyPushPayload struct {
	AgentID         string          `json:"agent_id"`
	Storage         StorageConfig   `json:"storage"`
	ResticPassword  string          `json:"restic_password"`
	BackupDirs      []string        `json:"backup_dirs"`
	ExcludePatterns []string        `json:"exclude_patterns"`
	Schedule        string          `json:"schedule"`
	Retention       RetentionPolicy `json:"retention"`
}

// StorageConfig contains rclone and repository settings for a backup policy.
type StorageConfig struct {
	RcloneType         string            `json:"rclone_type"`
	RcloneConfig       map[string]string `json:"rclone_config"`
	RepoPath           string            `json:"repo_path"`
	RcloneArgs         map[string]string `json:"rclone_args,omitempty"`
	RclonePassObscured bool              `json:"rclone_pass_obscured,omitempty"`
}

// RetentionPolicy maps directly to restic forget retention options.
type RetentionPolicy struct {
	KeepLast    int `json:"keep_last"`
	KeepDaily   int `json:"keep_daily"`
	KeepWeekly  int `json:"keep_weekly"`
	KeepMonthly int `json:"keep_monthly"`
}

// BackupNowPayload requests an immediate backup run for an agent.
type BackupNowPayload struct {
	AgentID string `json:"agent_id"`
}

// RestoreReqPayload requests a snapshot restore to a target path.
type RestoreReqPayload struct {
	SnapshotID   string   `json:"snapshot_id"`
	Target       string   `json:"target"`
	IncludePaths []string `json:"include_paths,omitempty"`
}

// SnapshotListReqPayload requests repository snapshots from an agent.
type SnapshotListReqPayload struct {
	AgentID string `json:"agent_id"`
}

// SnapshotBrowseReqPayload requests entries contained in one repository snapshot.
type SnapshotBrowseReqPayload struct {
	SnapshotID string `json:"snapshot_id"`
	Path       string `json:"path,omitempty"`
}

// SnapshotBrowseRespPayload returns file entries contained in one snapshot.
type SnapshotBrowseRespPayload struct {
	SnapshotID string              `json:"snapshot_id"`
	Entries    []SnapshotFileEntry `json:"entries"`
	Error      string              `json:"error,omitempty"`
}

// SnapshotFileEntry describes one file or directory inside a snapshot.
type SnapshotFileEntry struct {
	Path  string `json:"path"`
	Type  string `json:"type"`
	Size  int64  `json:"size"`
	Mtime string `json:"mtime"`
}

// CollectLogsReqPayload requests recent logs from an agent.
type CollectLogsReqPayload struct {
	MaxBytes int `json:"max_bytes"`
}

// CollectLogsRespPayload returns collected log text from an agent.
type CollectLogsRespPayload struct {
	Logs  string `json:"logs"`
	Error string `json:"error,omitempty"`
}

type VersionInfoPayload struct {
	Version    string `json:"version"`
	GitHubRepo string `json:"github_repo"`
}

type UpdateAgentPayload struct {
	Version    string `json:"version"`
	GitHubRepo string `json:"github_repo"`
}
