package backup

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createZipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := w.Create(name)
		require.NoError(t, err)
		_, err = f.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func TestValidateBackupZip_Valid(t *testing.T) {
	data := createZipBytes(t, map[string]string{
		"vaultfleet.db": "db data",
		"master.key":    "key data",
	})
	result := ValidateBackupZip(data)
	assert.True(t, result.Valid)
	assert.Empty(t, result.Errors)
	assert.Contains(t, result.Files, "vaultfleet.db")
	assert.Contains(t, result.Files, "master.key")
}

func TestValidateBackupZip_MissingDB(t *testing.T) {
	data := createZipBytes(t, map[string]string{
		"master.key": "key data",
	})
	result := ValidateBackupZip(data)
	assert.False(t, result.Valid)
	assert.Contains(t, result.Errors, "缺少必需文件: vaultfleet.db")
}

func TestValidateBackupZip_MissingKey(t *testing.T) {
	data := createZipBytes(t, map[string]string{
		"vaultfleet.db": "db data",
	})
	result := ValidateBackupZip(data)
	assert.False(t, result.Valid)
	assert.Contains(t, result.Errors, "缺少必需文件: master.key")
}

func TestValidateBackupZip_InvalidZip(t *testing.T) {
	result := ValidateBackupZip([]byte("not a zip"))
	assert.False(t, result.Valid)
	assert.NotEmpty(t, result.Errors)
}

func TestValidateBackupZip_PathTraversal(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, _ := w.Create("../escape.txt")
	f.Write([]byte("bad"))
	f2, _ := w.Create("vaultfleet.db")
	f2.Write([]byte("db"))
	f3, _ := w.Create("master.key")
	f3.Write([]byte("key"))
	w.Close()

	result := ValidateBackupZip(buf.Bytes())
	assert.False(t, result.Valid)
}

func TestValidateBackupZip_Empty(t *testing.T) {
	result := ValidateBackupZip(nil)
	assert.False(t, result.Valid)
}

func TestValidateBackupFile_SizeLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.zip")
	f, _ := os.Create(path)
	f.Close()
	result, err := ValidateBackupFile(path, 1)
	require.NoError(t, err)
	assert.False(t, result.Valid)
}
