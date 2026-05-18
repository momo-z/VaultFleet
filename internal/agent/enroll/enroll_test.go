package enroll

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestEnroll_SuccessPostsCurrentContractAndSavesConfig(t *testing.T) {
	requests := make(chan map[string]string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/agent/enroll", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var req map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		requests <- req

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"data": map[string]string{
				"agent_id":    "agent-uuid-1",
				"agent_token": "ak_returned_token",
			},
		}))
	}))
	t.Cleanup(server.Close)

	configPath := filepath.Join(t.TempDir(), "agent.yaml")
	cfg, err := Enroll(server.URL, "ek_test123", configPath)

	require.NoError(t, err)
	assert.Equal(t, server.URL, cfg.Server)
	assert.Equal(t, "agent-uuid-1", cfg.AgentID)
	assert.Equal(t, "ak_returned_token", cfg.AgentToken)

	req := <-requests
	assert.Equal(t, "ek_test123", req["enroll_token"])
	require.NotEmpty(t, req["system_info"])
	assert.NotContains(t, string(mustJSON(t, req)), `"token"`)

	var systemInfo map[string]string
	require.NoError(t, json.Unmarshal([]byte(req["system_info"]), &systemInfo))
	assert.NotEmpty(t, systemInfo["hostname"])
	assert.Equal(t, runtime.GOOS, systemInfo["os"])
	assert.Equal(t, runtime.GOARCH, systemInfo["arch"])

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	var saved AgentConfig
	require.NoError(t, yaml.Unmarshal(data, &saved))
	assert.Equal(t, *cfg, saved)
}

func TestEnroll_JoinsPathWhenServerURLHasTrailingSlash(t *testing.T) {
	seenPath := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath <- r.URL.Path
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"data": map[string]string{
				"agent_id":    "agent-uuid-1",
				"agent_token": "ak_returned_token",
			},
		}))
	}))
	t.Cleanup(server.Close)

	_, err := Enroll(server.URL+"/", "ek_test123", filepath.Join(t.TempDir(), "agent.yaml"))

	require.NoError(t, err)
	assert.Equal(t, "/api/agent/enroll", <-seenPath)
}

func TestEnroll_InvalidTokenReturnsStatusAndDoesNotWriteConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"error":"invalid enrollment token"}`))
	}))
	t.Cleanup(server.Close)
	configPath := filepath.Join(t.TempDir(), "agent.yaml")

	_, err := Enroll(server.URL, "bad-token", configPath)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 401")
	assert.NoFileExists(t, configPath)
}

func TestEnroll_AlreadyUsedTokenReturnsConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"ok":false,"error":"agent already enrolled"}`))
	}))
	t.Cleanup(server.Close)

	_, err := Enroll(server.URL, "used-token", filepath.Join(t.TempDir(), "agent.yaml"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 409")
}

func TestEnroll_ConfigPathPermissions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"data": map[string]string{
				"agent_id":    "agent-uuid-1",
				"agent_token": "ak_returned_token",
			},
		}))
	}))
	t.Cleanup(server.Close)

	configPath := filepath.Join(t.TempDir(), "vaultfleet", "agent.yaml")
	_, err := Enroll(server.URL, "ek_test123", configPath)

	require.NoError(t, err)
	parent, err := os.Stat(filepath.Dir(configPath))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), parent.Mode().Perm())
	config, err := os.Stat(configPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), config.Mode().Perm())
}

func TestEnroll_RepairsExistingConfigFilePermissions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"data": map[string]string{
				"agent_id":    "agent-uuid-1",
				"agent_token": "ak_returned_token",
			},
		}))
	}))
	t.Cleanup(server.Close)

	configPath := filepath.Join(t.TempDir(), "agent.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("old: value\n"), 0644))

	_, err := Enroll(server.URL, "ek_test123", configPath)

	require.NoError(t, err)
	config, err := os.Stat(configPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), config.Mode().Perm())
}

func TestEnroll_BadEnvelopeReturnsErrorAndDoesNotWriteConfig(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "ok false", body: `{"ok":false,"error":"nope","data":{"agent_id":"agent-1","agent_token":"ak_1"}}`},
		{name: "missing data", body: `{"ok":true}`},
		{name: "missing agent id", body: `{"ok":true,"data":{"agent_token":"ak_1"}}`},
		{name: "missing agent token", body: `{"ok":true,"data":{"agent_id":"agent-1"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			configPath := filepath.Join(t.TempDir(), "agent.yaml")

			_, err := Enroll(server.URL, "ek_test123", configPath)

			require.Error(t, err)
			assert.NoFileExists(t, configPath)
		})
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()

	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}
