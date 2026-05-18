package db

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const masterKeySize = 32

func InitMasterKey(dataDir string) ([]byte, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	keyPath := filepath.Join(dataDir, "master.key")

	data, err := loadMasterKey(keyPath)
	if err == nil {
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	key := make([]byte, masterKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}

	tempFile, err := os.CreateTemp(dataDir, ".master.key.*")
	if err != nil {
		return nil, fmt.Errorf("create temporary master.key: %w", err)
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	if _, err := tempFile.Write(key); err != nil {
		_ = tempFile.Close()
		return nil, fmt.Errorf("write temporary master.key: %w", err)
	}
	if err := tempFile.Chmod(0600); err != nil {
		_ = tempFile.Close()
		return nil, fmt.Errorf("chmod temporary master.key: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return nil, fmt.Errorf("close temporary master.key: %w", err)
	}

	if err := os.Link(tempPath, keyPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return loadMasterKey(keyPath)
		}
		return nil, fmt.Errorf("install master.key: %w", err)
	}

	_ = os.Remove(tempPath)

	return key, nil
}

func Encrypt(plaintext string, key []byte) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func Decrypt(ciphertext string, key []byte) (string, error) {
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}

	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}

	nonce := data[:nonceSize]
	sealed := data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != masterKeySize {
		return nil, fmt.Errorf("invalid AES-256 key size: expected 32 bytes, got %d", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func loadMasterKey(keyPath string) ([]byte, error) {
	info, err := os.Stat(keyPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("stat master.key: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("read master.key: %s is a directory", keyPath)
	}
	if info.Mode().Perm() != 0600 {
		if err := os.Chmod(keyPath, 0600); err != nil {
			return nil, fmt.Errorf("repair master.key permissions: %w", err)
		}
	}

	data, err := os.ReadFile(keyPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("read master.key: %w", err)
	}
	if len(data) != masterKeySize {
		return nil, fmt.Errorf("invalid master.key: expected 32 bytes, got %d", len(data))
	}

	return data, nil
}
