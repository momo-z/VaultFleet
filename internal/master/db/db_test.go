package db

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestDB(t *testing.T) *Database {
	t.Helper()

	database, err := New(t.TempDir())
	require.NoError(t, err)
	return database
}

func TestDatabaseInit(t *testing.T) {
	database := setupTestDB(t)

	assert.NotNil(t, database.DB)
	assert.NotEmpty(t, database.DataDir)
	assert.Len(t, database.MasterKey, 32)
}

func TestDatabaseInit_CreatesDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")

	database, err := New(dir)

	require.NoError(t, err)
	assert.NotNil(t, database.DB)

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestUserCRUD(t *testing.T) {
	database := setupTestDB(t)

	user := User{Username: "admin", PasswordHash: "$2a$10$fakehash"}
	require.NoError(t, database.DB.Create(&user).Error)
	assert.NotEmpty(t, user.ID)

	var found User
	require.NoError(t, database.DB.First(&found, "id = ?", user.ID).Error)
	assert.Equal(t, "admin", found.Username)

	require.NoError(t, database.DB.Model(&found).Update("username", "superadmin").Error)

	var updated User
	require.NoError(t, database.DB.First(&updated, "id = ?", user.ID).Error)
	assert.Equal(t, "superadmin", updated.Username)

	require.NoError(t, database.DB.Delete(&User{}, "id = ?", user.ID).Error)
	result := database.DB.First(&User{}, "id = ?", user.ID)
	assert.Error(t, result.Error)
}

func TestUserUniqueUsername(t *testing.T) {
	database := setupTestDB(t)

	u1 := User{Username: "admin", PasswordHash: "hash1"}
	require.NoError(t, database.DB.Create(&u1).Error)

	u2 := User{Username: "admin", PasswordHash: "hash2"}
	err := database.DB.Create(&u2).Error
	assert.Error(t, err)
}

func TestAgentCRUD(t *testing.T) {
	database := setupTestDB(t)
	now := time.Now()

	agent := Agent{
		Name:        "Tokyo-1",
		EnrollToken: "ek_test123",
		Status:      "offline",
		LastSeenAt:  &now,
		SystemInfo:  `{"os":"linux","arch":"amd64"}`,
	}
	require.NoError(t, database.DB.Create(&agent).Error)
	assert.NotEmpty(t, agent.ID)

	var found Agent
	require.NoError(t, database.DB.First(&found, "id = ?", agent.ID).Error)
	assert.Equal(t, "Tokyo-1", found.Name)
	assert.Equal(t, "ek_test123", found.EnrollToken)

	require.NoError(t, database.DB.Model(&found).Update("status", "online").Error)

	var updated Agent
	require.NoError(t, database.DB.First(&updated, "id = ?", agent.ID).Error)
	assert.Equal(t, "online", updated.Status)

	require.NoError(t, database.DB.Delete(&Agent{}, "id = ?", agent.ID).Error)
	result := database.DB.First(&Agent{}, "id = ?", agent.ID)
	assert.Error(t, result.Error)
}

func TestStorageConfigCRUD(t *testing.T) {
	database := setupTestDB(t)

	sc := StorageConfig{
		Name:         "Cloudflare R2",
		RcloneType:   "s3",
		RcloneConfig: `{"provider":"Cloudflare","endpoint":"https://xxx.r2.cloudflarestorage.com"}`,
	}
	require.NoError(t, database.DB.Create(&sc).Error)
	assert.NotEmpty(t, sc.ID)

	var found StorageConfig
	require.NoError(t, database.DB.First(&found, "id = ?", sc.ID).Error)
	assert.Equal(t, "s3", found.RcloneType)
	assert.Contains(t, found.RcloneConfig, "Cloudflare")

	require.NoError(t, database.DB.Model(&found).Update("name", "Updated R2").Error)

	var updated StorageConfig
	require.NoError(t, database.DB.First(&updated, "id = ?", sc.ID).Error)
	assert.Equal(t, "Updated R2", updated.Name)

	require.NoError(t, database.DB.Delete(&StorageConfig{}, "id = ?", sc.ID).Error)
	result := database.DB.First(&StorageConfig{}, "id = ?", sc.ID)
	assert.Error(t, result.Error)
}

func TestBackupPolicyCRUD(t *testing.T) {
	database := setupTestDB(t)

	policy := BackupPolicy{
		AgentID:         "agent-001",
		StorageID:       "storage-001",
		RepoPath:        "vaultfleet/agent-001",
		ResticPassword:  "encrypted-password",
		BackupDirs:      `["/etc","/home"]`,
		ExcludePatterns: `["*.log"]`,
		Schedule:        "0 3 * * *",
		Retention:       `{"keep_last":3,"keep_daily":7}`,
		Synced:          false,
	}
	require.NoError(t, database.DB.Create(&policy).Error)
	assert.NotEmpty(t, policy.ID)

	var found BackupPolicy
	require.NoError(t, database.DB.First(&found, "id = ?", policy.ID).Error)
	assert.Equal(t, "0 3 * * *", found.Schedule)
	assert.False(t, found.Synced)

	require.NoError(t, database.DB.Model(&found).Update("synced", true).Error)

	var updated BackupPolicy
	require.NoError(t, database.DB.First(&updated, "id = ?", policy.ID).Error)
	assert.True(t, updated.Synced)

	require.NoError(t, database.DB.Delete(&BackupPolicy{}, "id = ?", policy.ID).Error)
	result := database.DB.First(&BackupPolicy{}, "id = ?", policy.ID)
	assert.Error(t, result.Error)
}

func TestTaskHistoryCRUD(t *testing.T) {
	database := setupTestDB(t)
	now := time.Now()
	finished := now.Add(45 * time.Second)

	th := TaskHistory{
		AgentID:    "agent-001",
		Type:       "backup",
		Status:     "success",
		SnapshotID: "snap123",
		StartedAt:  &now,
		FinishedAt: &finished,
		DurationMs: 45000,
		RepoSize:   1073741824,
	}
	require.NoError(t, database.DB.Create(&th).Error)
	assert.NotEmpty(t, th.ID)

	var found TaskHistory
	require.NoError(t, database.DB.First(&found, "id = ?", th.ID).Error)
	assert.Equal(t, "success", found.Status)
	assert.Equal(t, int64(45000), found.DurationMs)

	require.NoError(t, database.DB.Model(&found).Update("status", "failed").Error)

	var updated TaskHistory
	require.NoError(t, database.DB.First(&updated, "id = ?", th.ID).Error)
	assert.Equal(t, "failed", updated.Status)

	require.NoError(t, database.DB.Delete(&TaskHistory{}, "id = ?", th.ID).Error)
	result := database.DB.First(&TaskHistory{}, "id = ?", th.ID)
	assert.Error(t, result.Error)
}

func TestSnapshotCRUD(t *testing.T) {
	database := setupTestDB(t)

	snap := Snapshot{
		AgentID:    "agent-001",
		SnapshotID: "restic-snap-123",
		Timestamp:  time.Now(),
		Paths:      `["/etc","/home"]`,
		Size:       500000,
	}
	require.NoError(t, database.DB.Create(&snap).Error)
	assert.NotEmpty(t, snap.ID)

	var found Snapshot
	require.NoError(t, database.DB.First(&found, "id = ?", snap.ID).Error)
	assert.Equal(t, "restic-snap-123", found.SnapshotID)
	assert.Equal(t, int64(500000), found.Size)

	require.NoError(t, database.DB.Model(&found).Update("size", 600000).Error)

	var updated Snapshot
	require.NoError(t, database.DB.First(&updated, "id = ?", snap.ID).Error)
	assert.Equal(t, int64(600000), updated.Size)

	require.NoError(t, database.DB.Delete(&Snapshot{}, "id = ?", snap.ID).Error)
	result := database.DB.First(&Snapshot{}, "id = ?", snap.ID)
	assert.Error(t, result.Error)
}

func TestNotificationConfigCRUD(t *testing.T) {
	database := setupTestDB(t)

	nc := NotificationConfig{
		Type:   "telegram",
		Config: `{"bot_token":"123:ABC","chat_id":"-100123"}`,
		Events: `["backup_failed","agent_offline"]`,
	}
	require.NoError(t, database.DB.Create(&nc).Error)
	assert.NotEmpty(t, nc.ID)

	var found NotificationConfig
	require.NoError(t, database.DB.First(&found, "id = ?", nc.ID).Error)
	assert.Equal(t, "telegram", found.Type)

	require.NoError(t, database.DB.Model(&found).Update("type", "webhook").Error)

	var updated NotificationConfig
	require.NoError(t, database.DB.First(&updated, "id = ?", nc.ID).Error)
	assert.Equal(t, "webhook", updated.Type)

	require.NoError(t, database.DB.Delete(&NotificationConfig{}, "id = ?", nc.ID).Error)
	result := database.DB.First(&NotificationConfig{}, "id = ?", nc.ID)
	assert.Error(t, result.Error)
}

func TestAgentByEnrollToken(t *testing.T) {
	database := setupTestDB(t)

	agent := Agent{Name: "Tokyo-1", EnrollToken: "ek_unique_token", Status: "offline"}
	require.NoError(t, database.DB.Create(&agent).Error)

	var found Agent
	err := database.DB.Where("enroll_token = ?", "ek_unique_token").First(&found).Error
	require.NoError(t, err)
	assert.Equal(t, agent.ID, found.ID)

	err = database.DB.Where("enroll_token = ?", "ek_nonexistent").First(&Agent{}).Error
	assert.Error(t, err)
}

func TestAgentEnrollTokenUniqueWhenNonEmpty(t *testing.T) {
	database := setupTestDB(t)

	first := Agent{Name: "Tokyo-1", EnrollToken: "ek_duplicate", Status: "offline"}
	require.NoError(t, database.DB.Create(&first).Error)

	duplicate := Agent{Name: "Tokyo-2", EnrollToken: "ek_duplicate", Status: "offline"}
	err := database.DB.Create(&duplicate).Error

	require.Error(t, err)
}

func TestAgentAgentTokenUniqueWhenNonEmpty(t *testing.T) {
	database := setupTestDB(t)

	first := Agent{Name: "Tokyo-1", AgentToken: "ak_duplicate", Status: "online"}
	require.NoError(t, database.DB.Create(&first).Error)

	duplicate := Agent{Name: "Tokyo-2", AgentToken: "ak_duplicate", Status: "online"}
	err := database.DB.Create(&duplicate).Error

	require.Error(t, err)
}

func TestAgentEmptyConsumedTokensAreNotUnique(t *testing.T) {
	database := setupTestDB(t)

	first := Agent{Name: "Tokyo-1", EnrollToken: "", AgentToken: "", Status: "offline"}
	require.NoError(t, database.DB.Create(&first).Error)

	second := Agent{Name: "Tokyo-2", EnrollToken: "", AgentToken: "", Status: "offline"}
	require.NoError(t, database.DB.Create(&second).Error)
}
