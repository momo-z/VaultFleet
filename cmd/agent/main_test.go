package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnrollReturnsCmdAgentConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"data": map[string]string{
				"agent_id":    "agent-1",
				"agent_token": "ak_test",
			},
		}))
	}))
	t.Cleanup(server.Close)

	cfg, err := enroll(server.URL, "ek_test", filepath.Join(t.TempDir(), "agent.yaml"))

	require.NoError(t, err)
	assert.Equal(t, &AgentConfig{
		Server:     server.URL,
		AgentID:    "agent-1",
		AgentToken: "ak_test",
	}, cfg)
}
