package db

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitMasterKey_Generate(t *testing.T) {
	dir := t.TempDir()

	key, err := InitMasterKey(dir)

	require.NoError(t, err)
	assert.Len(t, key, 32)

	data, err := os.ReadFile(filepath.Join(dir, "master.key"))
	require.NoError(t, err)
	assert.Equal(t, key, data)
}

func TestInitMasterKey_LoadExisting(t *testing.T) {
	dir := t.TempDir()

	key1, err := InitMasterKey(dir)
	require.NoError(t, err)

	key2, err := InitMasterKey(dir)

	require.NoError(t, err)
	assert.Equal(t, key1, key2)
}

func TestInitMasterKey_InvalidSize(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "master.key"), []byte("too-short"), 0600))

	_, err := InitMasterKey(dir)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid master.key")
}

func TestInitMasterKey_RepairsExistingFilePermissions(t *testing.T) {
	dir := t.TempDir()
	key := testKey(0)
	keyPath := filepath.Join(dir, "master.key")
	require.NoError(t, os.WriteFile(keyPath, key, 0644))

	loaded, err := InitMasterKey(dir)

	require.NoError(t, err)
	assert.Equal(t, key, loaded)

	info, err := os.Stat(keyPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func TestInitMasterKey_ConcurrentCallsReturnPersistedKey(t *testing.T) {
	dir := t.TempDir()

	const callers = 16
	keys := make([][]byte, callers)
	errs := make([]error, callers)

	var start sync.WaitGroup
	start.Add(1)

	var done sync.WaitGroup
	done.Add(callers)
	for i := 0; i < callers; i++ {
		go func(i int) {
			defer done.Done()
			start.Wait()
			keys[i], errs[i] = InitMasterKey(dir)
		}(i)
	}

	start.Done()
	done.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}

	persisted, err := os.ReadFile(filepath.Join(dir, "master.key"))
	require.NoError(t, err)
	require.Len(t, persisted, 32)

	for _, key := range keys {
		assert.Equal(t, persisted, key)
	}
}

func TestEncryptDecrypt(t *testing.T) {
	key := testKey(0)
	plaintext := "super-secret-rclone-credential"

	encrypted, err := Encrypt(plaintext, key)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, encrypted)

	decrypted, err := Decrypt(encrypted, key)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestEncryptDecrypt_DifferentNonces(t *testing.T) {
	key := testKey(0)

	enc1, err := Encrypt("same-text", key)
	require.NoError(t, err)

	enc2, err := Encrypt("same-text", key)
	require.NoError(t, err)

	assert.NotEqual(t, enc1, enc2, "random nonce should produce different ciphertexts")
}

func TestDecrypt_WrongKey(t *testing.T) {
	key1 := testKey(0)
	key2 := testKey(1)

	encrypted, err := Encrypt("secret", key1)
	require.NoError(t, err)

	_, err = Decrypt(encrypted, key2)
	assert.Error(t, err)
}

func TestDecrypt_InvalidBase64(t *testing.T) {
	key := testKey(0)

	_, err := Decrypt("not-valid-base64!!!", key)

	assert.Error(t, err)
}

func TestEncryptDecrypt_EmptyString(t *testing.T) {
	key := testKey(0)

	encrypted, err := Encrypt("", key)
	require.NoError(t, err)

	decrypted, err := Decrypt(encrypted, key)
	require.NoError(t, err)
	assert.Equal(t, "", decrypted)
}

func TestEncrypt_InvalidAES256KeySize(t *testing.T) {
	tests := []struct {
		name string
		key  []byte
	}{
		{name: "16 byte AES-128 key", key: make([]byte, 16)},
		{name: "24 byte AES-192 key", key: make([]byte, 24)},
		{name: "too short", key: make([]byte, 31)},
		{name: "too long", key: make([]byte, 33)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Encrypt("secret", tt.key)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid AES-256 key size")
		})
	}
}

func TestDecrypt_InvalidAES256KeySize(t *testing.T) {
	validCiphertext, err := Encrypt("secret", testKey(0))
	require.NoError(t, err)

	tests := []struct {
		name string
		key  []byte
	}{
		{name: "16 byte AES-128 key", key: make([]byte, 16)},
		{name: "24 byte AES-192 key", key: make([]byte, 24)},
		{name: "too short", key: make([]byte, 31)},
		{name: "too long", key: make([]byte, 33)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decrypt(validCiphertext, tt.key)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid AES-256 key size")
		})
	}
}

func TestEncryptDecrypt_LongString(t *testing.T) {
	key := testKey(0)
	longText := strings.Repeat("abcdefghij", 1000)

	encrypted, err := Encrypt(longText, key)
	require.NoError(t, err)

	decrypted, err := Decrypt(encrypted, key)
	require.NoError(t, err)
	assert.Equal(t, longText, decrypted)
}

func TestMasterKeyFilePermissions(t *testing.T) {
	dir := t.TempDir()

	_, err := InitMasterKey(dir)
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(dir, "master.key"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func testKey(offset byte) []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i) + offset
	}
	return key
}
