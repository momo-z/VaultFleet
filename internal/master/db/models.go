package db

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type User struct {
	ID           string    `gorm:"type:text;primaryKey" json:"id"`
	Username     string    `gorm:"type:text;uniqueIndex;not null" json:"username"`
	PasswordHash string    `gorm:"type:text;not null" json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (u *User) BeforeCreate(tx *gorm.DB) error {
	if u.ID == "" {
		u.ID = uuid.NewString()
	}
	return nil
}

type Agent struct {
	ID          string     `gorm:"type:text;primaryKey" json:"id"`
	Name        string     `gorm:"type:text;not null" json:"name"`
	EnrollToken string     `gorm:"type:text;uniqueIndex:idx_agents_enroll_token_nonempty,where:enroll_token <> ''" json:"enroll_token,omitempty"`
	AgentToken  string     `gorm:"type:text;uniqueIndex:idx_agents_agent_token_nonempty,where:agent_token <> ''" json:"-"`
	Status      string     `gorm:"type:text;default:offline" json:"status"`
	LastSeenAt  *time.Time `json:"last_seen_at"`
	SystemInfo  string     `gorm:"type:text" json:"system_info"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (a *Agent) BeforeCreate(tx *gorm.DB) error {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	return nil
}

type StorageConfig struct {
	ID           string    `gorm:"type:text;primaryKey" json:"id"`
	Name         string    `gorm:"type:text;not null" json:"name"`
	RcloneType   string    `gorm:"type:text;not null" json:"rclone_type"`
	RcloneConfig string    `gorm:"type:text" json:"rclone_config"`
	CreatedAt    time.Time `json:"created_at"`
}

func (s *StorageConfig) BeforeCreate(tx *gorm.DB) error {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	return nil
}

type BackupPolicy struct {
	ID              string    `gorm:"type:text;primaryKey" json:"id"`
	AgentID         string    `gorm:"type:text;index;not null" json:"agent_id"`
	StorageID       string    `gorm:"type:text;not null" json:"storage_id"`
	RepoPath        string    `gorm:"type:text" json:"repo_path"`
	ResticPassword  string    `gorm:"type:text" json:"-"`
	BackupDirs      string    `gorm:"type:text" json:"backup_dirs"`
	ExcludePatterns string    `gorm:"type:text" json:"exclude_patterns"`
	Schedule        string    `gorm:"type:text" json:"schedule"`
	Retention       string    `gorm:"type:text" json:"retention"`
	Synced          bool      `gorm:"default:false" json:"synced"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (b *BackupPolicy) BeforeCreate(tx *gorm.DB) error {
	if b.ID == "" {
		b.ID = uuid.NewString()
	}
	return nil
}

type TaskHistory struct {
	ID         string     `gorm:"type:text;primaryKey" json:"id"`
	AgentID    string     `gorm:"type:text;index;not null" json:"agent_id"`
	Type       string     `gorm:"type:text;not null" json:"type"`
	Status     string     `gorm:"type:text;not null" json:"status"`
	SnapshotID string     `gorm:"type:text" json:"snapshot_id"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	DurationMs int64      `json:"duration_ms"`
	RepoSize   int64      `json:"repo_size"`
	ErrorLog   string     `gorm:"type:text" json:"error_log"`
	CreatedAt  time.Time  `json:"created_at"`
}

func (th *TaskHistory) BeforeCreate(tx *gorm.DB) error {
	if th.ID == "" {
		th.ID = uuid.NewString()
	}
	return nil
}

type Snapshot struct {
	ID         string    `gorm:"type:text;primaryKey" json:"id"`
	AgentID    string    `gorm:"type:text;index;not null" json:"agent_id"`
	SnapshotID string    `gorm:"type:text;not null" json:"snapshot_id"`
	Timestamp  time.Time `json:"timestamp"`
	Paths      string    `gorm:"type:text" json:"paths"`
	Size       int64     `json:"size"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Snapshot) BeforeCreate(tx *gorm.DB) error {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	return nil
}

type NotificationConfig struct {
	ID        string    `gorm:"type:text;primaryKey" json:"id"`
	Type      string    `gorm:"type:text;not null" json:"type"`
	Config    string    `gorm:"type:text" json:"config"`
	Events    string    `gorm:"type:text" json:"events"`
	CreatedAt time.Time `json:"created_at"`
}

func (n *NotificationConfig) BeforeCreate(tx *gorm.DB) error {
	if n.ID == "" {
		n.ID = uuid.NewString()
	}
	return nil
}
