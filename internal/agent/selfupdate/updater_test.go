package selfupdate

import (
	"errors"
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

type restartCommandCall struct {
	name string
	args []string
}

func withRestartDeps(
	t *testing.T,
	lookup func(string) (string, error),
	start func(string, ...string) error,
	exit func(int),
	execFn func(string, []string, []string) error,
) {
	t.Helper()

	oldLookPath := lookPath
	oldStartCommand := startCommand
	oldExitProcess := exitProcess
	oldExecProcess := execProcess

	lookPath = lookup
	startCommand = start
	exitProcess = exit
	execProcess = execFn

	t.Cleanup(func() {
		lookPath = oldLookPath
		startCommand = oldStartCommand
		exitProcess = oldExitProcess
		execProcess = oldExecProcess
	})
}

func TestDefaultRestartUsesSystemctlWhenAvailable(t *testing.T) {
	var started restartCommandCall
	exitCode := -1

	withRestartDeps(
		t,
		func(name string) (string, error) {
			if name == "systemctl" {
				return "/sbin/systemctl", nil
			}
			return "", exec.ErrNotFound
		},
		func(name string, args ...string) error {
			started = restartCommandCall{name: name, args: append([]string(nil), args...)}
			return nil
		},
		func(code int) {
			exitCode = code
		},
		func(string, []string, []string) error {
			t.Fatal("unexpected re-exec")
			return nil
		},
	)

	err := defaultRestart("/usr/local/bin/vaultfleet-agent")()
	require.NoError(t, err)
	assert.Equal(t, restartCommandCall{
		name: "/sbin/systemctl",
		args: []string{"restart", "vaultfleet-agent"},
	}, started)
	assert.Equal(t, 0, exitCode)
}

func TestDefaultRestartUsesOpenRCWhenSystemctlUnavailable(t *testing.T) {
	var started restartCommandCall
	exitCode := -1

	withRestartDeps(
		t,
		func(name string) (string, error) {
			if name == "rc-service" {
				return "/sbin/rc-service", nil
			}
			return "", exec.ErrNotFound
		},
		func(name string, args ...string) error {
			started = restartCommandCall{name: name, args: append([]string(nil), args...)}
			return nil
		},
		func(code int) {
			exitCode = code
		},
		func(string, []string, []string) error {
			t.Fatal("unexpected re-exec")
			return nil
		},
	)

	err := defaultRestart("/usr/local/bin/vaultfleet-agent")()
	require.NoError(t, err)
	assert.Equal(t, restartCommandCall{
		name: "/sbin/rc-service",
		args: []string{"vaultfleet-agent", "restart"},
	}, started)
	assert.Equal(t, 0, exitCode)
}

func TestDefaultRestartReexecsBinaryWhenNoServiceManager(t *testing.T) {
	originalArgs := os.Args
	os.Args = []string{"vaultfleet-agent", "--config", "/boot/config/plugins/vaultfleet/agent.yaml"}
	t.Cleanup(func() {
		os.Args = originalArgs
	})

	wantErr := errors.New("exec failed")
	var gotPath string
	var gotArgv []string
	var gotEnv []string

	withRestartDeps(
		t,
		func(string) (string, error) {
			return "", exec.ErrNotFound
		},
		func(string, ...string) error {
			t.Fatal("unexpected service restart")
			return nil
		},
		func(int) {
			t.Fatal("unexpected exit")
		},
		func(path string, argv []string, env []string) error {
			gotPath = path
			gotArgv = append([]string(nil), argv...)
			gotEnv = append([]string(nil), env...)
			return wantErr
		},
	)

	err := defaultRestart("/usr/local/bin/vaultfleet-agent")()
	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, "/usr/local/bin/vaultfleet-agent", gotPath)
	assert.Equal(t, os.Args, gotArgv)
	assert.Equal(t, os.Environ(), gotEnv)
}
