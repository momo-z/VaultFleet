package backup

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestDataDir(t *testing.T) string {
	t.Helper()

	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "vaultfleet.db"), []byte("db data"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "master.key"), []byte("master key"), 0600))

	rollbackDir := filepath.Join(dataDir, "rollback")
	require.NoError(t, os.MkdirAll(rollbackDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(rollbackDir, "20260517-030000.zip"), []byte("rollback"), 0644))

	return dataDir
}

func TestExportDataDir(t *testing.T) {
	dataDir := setupTestDataDir(t)

	buf, err := ExportDataDir(dataDir)

	require.NoError(t, err)
	require.NotNil(t, buf)
	assert.NotZero(t, buf.Len())

	entries := readZipEntries(t, buf.Bytes())
	assert.Contains(t, entries, "vaultfleet.db")
	assert.Contains(t, entries, "master.key")
	assert.NotContains(t, entries, "rollback/20260517-030000.zip")
}

func TestExportDataDir_SkipsBackupZip(t *testing.T) {
	dataDir := setupTestDataDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "backup.zip"), []byte("pending restore"), 0644))

	buf, err := ExportDataDir(dataDir)

	require.NoError(t, err)
	entries := readZipEntries(t, buf.Bytes())
	assert.NotContains(t, entries, "backup.zip")
}

func TestExportDataDir_EmptyDir(t *testing.T) {
	dataDir := t.TempDir()

	buf, err := ExportDataDir(dataDir)

	require.NoError(t, err)
	require.NotNil(t, buf)
	assert.NotZero(t, buf.Len())
	assert.Empty(t, readZipEntries(t, buf.Bytes()))
}

func TestExportDataDir_PreservesSubdirs(t *testing.T) {
	dataDir := setupTestDataDir(t)
	nestedPath := filepath.Join(dataDir, "configs", "rclone", "remote.conf")
	require.NoError(t, os.MkdirAll(filepath.Dir(nestedPath), 0755))
	require.NoError(t, os.WriteFile(nestedPath, []byte("remote config"), 0644))

	buf, err := ExportDataDir(dataDir)

	require.NoError(t, err)
	entries := readZipEntries(t, buf.Bytes())
	assert.Contains(t, entries, "configs/rclone/remote.conf")
}

func readZipEntries(t *testing.T, data []byte) map[string][]byte {
	t.Helper()

	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)

	entries := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		rc, err := file.Open()
		require.NoError(t, err)

		content, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())

		entries[file.Name] = content
	}
	return entries
}
