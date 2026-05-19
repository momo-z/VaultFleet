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

func assertFileContent(t *testing.T, path, expected string) {
	t.Helper()

	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()

	content, err := io.ReadAll(file)
	require.NoError(t, err)
	assert.Equal(t, expected, string(content))
}
