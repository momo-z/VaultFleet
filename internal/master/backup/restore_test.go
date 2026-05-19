package backup

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestBackupZip(t *testing.T, dataDir string, files map[string]string) {
	t.Helper()

	var buf bytes.Buffer
	archive := zip.NewWriter(&buf)
	for name, content := range files {
		writer, err := archive.Create(name)
		require.NoError(t, err)
		_, err = writer.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, archive.Close())
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "backup.zip"), buf.Bytes(), 0644))
}

func createTestBackupZipEntries(t *testing.T, dataDir string, entries []zipEntrySpec) {
	t.Helper()

	var buf bytes.Buffer
	archive := zip.NewWriter(&buf)
	for _, entry := range entries {
		header := &zip.FileHeader{
			Name:   entry.name,
			Method: zip.Deflate,
		}
		header.SetMode(entry.mode)

		writer, err := archive.CreateHeader(header)
		require.NoError(t, err)
		_, err = writer.Write([]byte(entry.content))
		require.NoError(t, err)
	}
	require.NoError(t, archive.Close())
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "backup.zip"), buf.Bytes(), 0644))
}

type zipEntrySpec struct {
	name    string
	content string
	mode    os.FileMode
}

func TestCheckAndRestore_NoBackupZip(t *testing.T) {
	dataDir := setupTestDataDir(t)

	restored, err := CheckAndRestore(dataDir)

	require.NoError(t, err)
	assert.False(t, restored)
	assertFileContent(t, filepath.Join(dataDir, "vaultfleet.db"), "db data")
	assertFileContent(t, filepath.Join(dataDir, "master.key"), "master key")
}

func TestCheckAndRestore_WithBackupZip(t *testing.T) {
	dataDir := setupTestDataDir(t)
	createTestBackupZip(t, dataDir, map[string]string{
		"vaultfleet.db": "restored db",
		"master.key":    "restored key",
	})

	restored, err := CheckAndRestore(dataDir)

	require.NoError(t, err)
	assert.True(t, restored)
	assert.NoFileExists(t, filepath.Join(dataDir, "backup.zip"))
	assertFileContent(t, filepath.Join(dataDir, "vaultfleet.db"), "restored db")
	assertFileContent(t, filepath.Join(dataDir, "master.key"), "restored key")
}

func TestCheckAndRestore_ReplacesDataSet(t *testing.T) {
	dataDir := setupTestDataDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "vaultfleet.db-wal"), []byte("stale wal"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "vaultfleet.db-shm"), []byte("stale shm"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "old-file.txt"), []byte("stale file"), 0644))
	createTestBackupZip(t, dataDir, map[string]string{
		"vaultfleet.db": "restored db",
		"master.key":    "restored key",
	})

	restored, err := CheckAndRestore(dataDir)

	require.NoError(t, err)
	assert.True(t, restored)
	assert.NoFileExists(t, filepath.Join(dataDir, "vaultfleet.db-wal"))
	assert.NoFileExists(t, filepath.Join(dataDir, "vaultfleet.db-shm"))
	assert.NoFileExists(t, filepath.Join(dataDir, "old-file.txt"))
	assert.DirExists(t, filepath.Join(dataDir, "rollback"))
	assert.NoFileExists(t, filepath.Join(dataDir, "backup.zip"))
	assertFileContent(t, filepath.Join(dataDir, "vaultfleet.db"), "restored db")
	assertFileContent(t, filepath.Join(dataDir, "master.key"), "restored key")
}

func TestCheckAndRestore_CreatesRollback(t *testing.T) {
	dataDir := setupTestDataDir(t)
	createTestBackupZip(t, dataDir, map[string]string{
		"vaultfleet.db": "restored db",
	})

	beforePrefix := time.Now().Format("20060102")
	restored, err := CheckAndRestore(dataDir)
	afterPrefix := time.Now().Format("20060102")

	require.NoError(t, err)
	require.True(t, restored)

	rollbackEntries, err := os.ReadDir(filepath.Join(dataDir, "rollback"))
	require.NoError(t, err)

	validPrefixes := map[string]bool{
		beforePrefix: true,
		afterPrefix:  true,
	}
	var rollbackFile string
	for _, entry := range rollbackEntries {
		prefix := strings.SplitN(entry.Name(), "-", 2)[0]
		if validPrefixes[prefix] && strings.HasSuffix(entry.Name(), ".zip") {
			rollbackFile = entry.Name()
			break
		}
	}
	require.NotEmpty(t, rollbackFile, "expected rollback zip with current date prefix")

	rollbackPath := filepath.Join(dataDir, "rollback", rollbackFile)
	rollbackBytes, err := os.ReadFile(rollbackPath)
	require.NoError(t, err)
	entries := readZipEntries(t, rollbackBytes)
	assert.Equal(t, []byte("db data"), entries["vaultfleet.db"])
	assert.Equal(t, []byte("master key"), entries["master.key"])
	assert.NotContains(t, entries, "backup.zip")
}

func TestCheckAndRestore_CreatesRollbackWithSecurePermissions(t *testing.T) {
	dataDir := setupTestDataDir(t)
	createTestBackupZip(t, dataDir, map[string]string{
		"vaultfleet.db": "restored db",
	})

	beforePrefix := time.Now().Format("20060102")
	restored, err := CheckAndRestore(dataDir)
	afterPrefix := time.Now().Format("20060102")

	require.NoError(t, err)
	require.True(t, restored)

	rollbackDir := filepath.Join(dataDir, "rollback")
	rollbackDirInfo, err := os.Stat(rollbackDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), rollbackDirInfo.Mode().Perm())

	rollbackEntries, err := os.ReadDir(rollbackDir)
	require.NoError(t, err)

	validPrefixes := map[string]bool{
		beforePrefix: true,
		afterPrefix:  true,
	}
	var rollbackFile os.DirEntry
	for _, entry := range rollbackEntries {
		prefix := strings.SplitN(entry.Name(), "-", 2)[0]
		if validPrefixes[prefix] && strings.HasSuffix(entry.Name(), ".zip") {
			rollbackFile = entry
			break
		}
	}
	require.NotNil(t, rollbackFile, "expected rollback zip")

	rollbackInfo, err := rollbackFile.Info()
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), rollbackInfo.Mode().Perm())
}

func TestCheckAndRestore_BackupZipWithSubdirs(t *testing.T) {
	dataDir := setupTestDataDir(t)
	createTestBackupZip(t, dataDir, map[string]string{
		"configs/rclone/remote.conf": "remote config",
	})

	restored, err := CheckAndRestore(dataDir)

	require.NoError(t, err)
	assert.True(t, restored)
	assertFileContent(t, filepath.Join(dataDir, "configs", "rclone", "remote.conf"), "remote config")
}

func TestCheckAndRestore_InvalidZip(t *testing.T) {
	dataDir := setupTestDataDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "backup.zip"), []byte("not a zip"), 0644))

	restored, err := CheckAndRestore(dataDir)

	require.Error(t, err)
	assert.False(t, restored)
	assert.Contains(t, strings.ToLower(err.Error()), "zip")
	assert.FileExists(t, filepath.Join(dataDir, "backup.zip"))
}

func TestCheckAndRestore_BlocksPathTraversal(t *testing.T) {
	dataDir := setupTestDataDir(t)
	outsidePath := filepath.Join(filepath.Dir(dataDir), "outside.txt")
	createTestBackupZip(t, dataDir, map[string]string{
		"../outside.txt": "escaped",
	})

	restored, err := CheckAndRestore(dataDir)

	require.Error(t, err)
	assert.False(t, restored)
	assert.Contains(t, err.Error(), "unsafe zip entry path")
	assert.NoFileExists(t, outsidePath)
	assert.FileExists(t, filepath.Join(dataDir, "backup.zip"))
}

func TestCheckAndRestore_InvalidLaterEntryDoesNotMutateDataDir(t *testing.T) {
	dataDir := setupTestDataDir(t)
	outsidePath := filepath.Join(filepath.Dir(dataDir), "outside.txt")
	createTestBackupZipEntries(t, dataDir, []zipEntrySpec{
		{name: "vaultfleet.db", content: "mutated db", mode: 0644},
		{name: "../outside.txt", content: "escaped", mode: 0644},
	})

	restored, err := CheckAndRestore(dataDir)

	require.Error(t, err)
	assert.False(t, restored)
	assertFileContent(t, filepath.Join(dataDir, "vaultfleet.db"), "db data")
	assert.NoFileExists(t, outsidePath)
	assert.FileExists(t, filepath.Join(dataDir, "backup.zip"))
}

func TestCheckAndRestore_ReservedLaterEntryDoesNotMutateDataDir(t *testing.T) {
	dataDir := setupTestDataDir(t)
	createTestBackupZipEntries(t, dataDir, []zipEntrySpec{
		{name: "vaultfleet.db", content: "mutated db", mode: 0644},
		{name: "rollback/old.zip", content: "reserved", mode: 0644},
	})

	restored, err := CheckAndRestore(dataDir)

	require.Error(t, err)
	assert.False(t, restored)
	assert.Contains(t, err.Error(), "reserved zip entry path")
	assertFileContent(t, filepath.Join(dataDir, "vaultfleet.db"), "db data")
	assert.DirExists(t, filepath.Join(dataDir, "rollback"))
	assert.FileExists(t, filepath.Join(dataDir, "backup.zip"))
}

func TestCheckAndRestore_BackupZipDescendantDoesNotMutateDataDir(t *testing.T) {
	dataDir := setupTestDataDir(t)
	createTestBackupZipEntries(t, dataDir, []zipEntrySpec{
		{name: "vaultfleet.db", content: "mutated db", mode: 0644},
		{name: "backup.zip/child", content: "reserved", mode: 0644},
	})

	restored, err := CheckAndRestore(dataDir)

	require.Error(t, err)
	assert.False(t, restored)
	assert.Contains(t, err.Error(), "reserved zip entry path")
	assertFileContent(t, filepath.Join(dataDir, "vaultfleet.db"), "db data")
	assert.DirExists(t, filepath.Join(dataDir, "rollback"))
	assert.FileExists(t, filepath.Join(dataDir, "backup.zip"))
}

func TestCheckAndRestore_ReplacesExistingSymlinkWithRegularFile(t *testing.T) {
	dataDir := setupTestDataDir(t)
	outsidePath := filepath.Join(filepath.Dir(dataDir), "outside.txt")
	require.NoError(t, os.WriteFile(outsidePath, []byte("outside original"), 0644))

	linkPath := filepath.Join(dataDir, "linked-file")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	createTestBackupZip(t, dataDir, map[string]string{
		"linked-file": "restored link path",
	})

	restored, err := CheckAndRestore(dataDir)

	require.NoError(t, err)
	assert.True(t, restored)
	assertFileContent(t, outsidePath, "outside original")

	info, err := os.Lstat(linkPath)
	require.NoError(t, err)
	assert.Zero(t, info.Mode()&os.ModeSymlink)
	assertFileContent(t, linkPath, "restored link path")
}

func TestCheckAndRestore_UpdatesExistingFilePermissions(t *testing.T) {
	dataDir := setupTestDataDir(t)
	require.NoError(t, os.Chmod(filepath.Join(dataDir, "master.key"), 0644))
	createTestBackupZipEntries(t, dataDir, []zipEntrySpec{
		{name: "master.key", content: "restored key", mode: 0600},
	})

	restored, err := CheckAndRestore(dataDir)

	require.NoError(t, err)
	assert.True(t, restored)
	info, err := os.Stat(filepath.Join(dataDir, "master.key"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	assertFileContent(t, filepath.Join(dataDir, "master.key"), "restored key")
}

func assertFileContent(t *testing.T, path, expected string) {
	t.Helper()

	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()

	content, err := io.ReadAll(file)
	require.NoError(t, err)
	assert.Equal(t, expected, string(content))
}
