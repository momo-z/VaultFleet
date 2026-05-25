package selfupdate

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateSkipsWhenVersionMatches(t *testing.T) {
	u := NewUpdater(Config{CurrentVersion: "v1.0.0"})
	err := u.Update("v1.0.0", "momo-z/VaultFleet")
	require.NoError(t, err)
}

func TestUpdateDownloadsAndReplaces(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\nexit 0\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/releases/download/v2.0.0/vaultfleet-agent-linux-amd64")
		w.Write(binaryContent)
	}))
	defer server.Close()

	binaryPath := filepath.Join(t.TempDir(), "vaultfleet-agent")
	require.NoError(t, os.WriteFile(binaryPath, []byte("old-binary"), 0755))

	var restarted bool
	u := NewUpdater(Config{
		CurrentVersion: "v1.0.0",
		BinaryPath:     binaryPath,
		GitHubRepo:     "momo-z/VaultFleet",
		Arch:           "amd64",
	})
	u.httpClient = server.Client()
	u.config.GitHubProxy = server.URL
	u.execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("true")
	}
	u.restart = func() error {
		restarted = true
		return nil
	}

	err := u.Update("v2.0.0", "")
	require.NoError(t, err)

	data, err := os.ReadFile(binaryPath)
	require.NoError(t, err)
	assert.Equal(t, binaryContent, data)
	assert.True(t, restarted)
}

func TestUpdateReturnsErrorOnDownloadFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	u := NewUpdater(Config{
		CurrentVersion: "v1.0.0",
		BinaryPath:     filepath.Join(t.TempDir(), "agent"),
		GitHubRepo:     "momo-z/VaultFleet",
		Arch:           "amd64",
	})
	u.httpClient = server.Client()
	u.config.GitHubProxy = server.URL

	err := u.Update("v2.0.0", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 404")
}

func TestUpdateReturnsErrorOnVerifyFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not-a-binary"))
	}))
	defer server.Close()

	binaryPath := filepath.Join(t.TempDir(), "agent")
	require.NoError(t, os.WriteFile(binaryPath, []byte("old"), 0755))

	u := NewUpdater(Config{
		CurrentVersion: "v1.0.0",
		BinaryPath:     binaryPath,
		GitHubRepo:     "momo-z/VaultFleet",
		Arch:           "amd64",
	})
	u.httpClient = server.Client()
	u.config.GitHubProxy = server.URL
	u.execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("false")
	}

	err := u.Update("v2.0.0", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "verify")

	data, err := os.ReadFile(binaryPath)
	require.NoError(t, err)
	assert.Equal(t, []byte("old"), data)
}

func TestUpdateSkipsWhenAlreadyInProgress(t *testing.T) {
	u := NewUpdater(Config{CurrentVersion: "v1.0.0"})
	u.mu.Lock()

	err := u.Update("v2.0.0", "momo-z/VaultFleet")
	require.NoError(t, err)

	u.mu.Unlock()
}

func TestBuildDownloadURL(t *testing.T) {
	u := NewUpdater(Config{Arch: "arm64"})
	url := u.buildDownloadURL("momo-z/VaultFleet", "v1.0.0")
	assert.Equal(t, "https://github.com/momo-z/VaultFleet/releases/download/v1.0.0/vaultfleet-agent-linux-arm64", url)
}

func TestBuildDownloadURLWithProxy(t *testing.T) {
	u := NewUpdater(Config{Arch: "amd64", GitHubProxy: "https://proxy.example.com"})
	url := u.buildDownloadURL("momo-z/VaultFleet", "v1.0.0")
	assert.Equal(t, "https://proxy.example.com/https://github.com/momo-z/VaultFleet/releases/download/v1.0.0/vaultfleet-agent-linux-amd64", url)
}

func TestConcurrentUpdateCallsAreSafe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("#!/bin/sh\nexit 0\n"))
	}))
	defer server.Close()

	binaryPath := filepath.Join(t.TempDir(), "agent")
	require.NoError(t, os.WriteFile(binaryPath, []byte("old"), 0755))

	u := NewUpdater(Config{
		CurrentVersion: "v1.0.0",
		BinaryPath:     binaryPath,
		GitHubRepo:     "momo-z/VaultFleet",
		Arch:           "amd64",
	})
	u.httpClient = server.Client()
	u.config.GitHubProxy = server.URL
	u.execCommand = func(name string, args ...string) *exec.Cmd { return exec.Command("true") }
	u.restart = func() error { return nil }

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = u.Update("v2.0.0", "")
		}()
	}
	wg.Wait()
}
