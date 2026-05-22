package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vaultfleet/internal/master/db"
	"vaultfleet/internal/master/logbuf"
	"vaultfleet/pkg/protocol"
)

type diagnosticTestSetup struct {
	database *db.Database
	router   *gin.Engine
	logBuf   *logbuf.RingBuffer
	hub      *fakeDiagnosticHub
}

type fakeDiagnosticHub struct {
	online    map[string]bool
	responses map[string]protocol.Message
	sent      []protocol.Message
	sendErr   map[string]error
}

func (h *fakeDiagnosticHub) SendAndWait(agentID string, msg protocol.Message, timeout time.Duration) (<-chan protocol.Message, error) {
	h.sent = append(h.sent, msg)
	if err := h.sendErr[agentID]; err != nil {
		return nil, err
	}
	ch := make(chan protocol.Message, 1)
	if resp, ok := h.responses[agentID]; ok {
		resp.ID = msg.ID
		ch <- resp
	}
	close(ch)
	return ch, nil
}

func (h *fakeDiagnosticHub) IsOnline(agentID string) bool {
	return h.online[agentID]
}

func setupDiagnosticAPI(t *testing.T) diagnosticTestSetup {
	t.Helper()
	gin.SetMode(gin.TestMode)

	database, err := db.New(t.TempDir())
	require.NoError(t, err)

	hub := &fakeDiagnosticHub{
		online:    make(map[string]bool),
		responses: make(map[string]protocol.Message),
		sendErr:   make(map[string]error),
	}
	buf := logbuf.New(1024)
	h := &DiagnosticHandler{
		DB:      database,
		Hub:     hub,
		LogBuf:  buf,
		Version: "v0.3.2",
	}

	router := gin.New()
	RegisterDiagnosticRoutes(router.Group("/api/system"), h)
	return diagnosticTestSetup{database: database, router: router, logBuf: buf, hub: hub}
}

func seedDiagnosticAgent(t *testing.T, database *db.Database, name, status string) string {
	t.Helper()
	agent := db.Agent{Name: name, Status: status}
	require.NoError(t, database.DB.Create(&agent).Error)
	return agent.ID
}

func seedFailedTask(t *testing.T, database *db.Database, agentID, errorLog string) {
	t.Helper()
	now := time.Now()
	task := db.TaskHistory{
		AgentID:    agentID,
		Type:       "backup",
		Status:     "failed",
		ErrorLog:   errorLog,
		FinishedAt: &now,
	}
	require.NoError(t, database.DB.Create(&task).Error)
}

func readZipFiles(t *testing.T, body *bytes.Buffer) map[string]string {
	t.Helper()
	zipReader, err := zip.NewReader(bytes.NewReader(body.Bytes()), int64(body.Len()))
	require.NoError(t, err)

	files := make(map[string]string)
	for _, f := range zipReader.File {
		rc, err := f.Open()
		require.NoError(t, err)
		data, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		files[f.Name] = string(data)
	}
	return files
}

func TestDiagnosticHandler_GenerateZip(t *testing.T) {
	setup := setupDiagnosticAPI(t)
	agentID := seedDiagnosticAgent(t, setup.database, "Test-Agent-1", "online")
	seedDiagnosticAgent(t, setup.database, "Test-Agent-2", "offline")
	seedFailedTask(t, setup.database, agentID, "backup failed: connection refused")

	setup.logBuf.Write([]byte("2026-05-22 master log line 1\n"))
	setup.logBuf.Write([]byte("2026-05-22 master log line 2 password=secret\n"))

	req := httptest.NewRequest(http.MethodGet, "/api/system/diagnostic", nil)
	w := httptest.NewRecorder()
	setup.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/zip", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Header().Get("Content-Disposition"), "vaultfleet-diagnostic-")

	files := readZipFiles(t, w.Body)

	assert.Contains(t, files, "meta.json")
	assert.Contains(t, files, "master/logs.txt")
	assert.Contains(t, files, "master/nodes.json")
	assert.Contains(t, files, "master/storage.json")
	assert.Contains(t, files, "master/policies.json")
	assert.Contains(t, files, "master/recent_errors.json")

	assert.Contains(t, files["master/logs.txt"], "master log line 1")
	assert.NotContains(t, files["master/logs.txt"], "secret")
	assert.Contains(t, files["master/logs.txt"], "[REDACTED]")

	var meta map[string]any
	require.NoError(t, json.Unmarshal([]byte(files["meta.json"]), &meta))
	assert.Equal(t, "v0.3.2", meta["version"])

	assert.Contains(t, files["master/recent_errors.json"], "connection refused")
}

func TestDiagnosticHandler_CollectsSelectedAgentLogs(t *testing.T) {
	setup := setupDiagnosticAPI(t)
	agentID := seedDiagnosticAgent(t, setup.database, "Agent One", "online")
	setup.hub.online[agentID] = true
	resp, err := protocol.NewMessage(protocol.TypeCollectLogsResp, protocol.CollectLogsRespPayload{
		Logs: "agent log token=raw-token\n",
	})
	require.NoError(t, err)
	setup.hub.responses[agentID] = *resp

	req := httptest.NewRequest(http.MethodGet, "/api/system/diagnostic?agents="+agentID, nil)
	w := httptest.NewRecorder()
	setup.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	files := readZipFiles(t, w.Body)
	assert.Equal(t, "agent log token=[REDACTED]\n", files["agents/"+agentID+"/logs.txt"])
	assert.NotContains(t, files["agents/"+agentID+"/logs.txt"], "raw-token")
	require.Len(t, setup.hub.sent, 1)
	assert.Equal(t, protocol.TypeCollectLogsReq, setup.hub.sent[0].Type)
}

func TestDiagnosticHandler_UsesAgentIDForArchivePathsAndRecordsWarnings(t *testing.T) {
	setup := setupDiagnosticAPI(t)
	traversalID := seedDiagnosticAgent(t, setup.database, "../../escape", "online")
	collidingID := seedDiagnosticAgent(t, setup.database, "safe/name", "online")
	setup.hub.online[traversalID] = true
	setup.hub.online[collidingID] = true
	setup.hub.sendErr[traversalID] = errors.New("connection refused")
	resp, err := protocol.NewMessage(protocol.TypeCollectLogsResp, protocol.CollectLogsRespPayload{
		Error: "journalctl unavailable",
	})
	require.NoError(t, err)
	setup.hub.responses[collidingID] = *resp

	req := httptest.NewRequest(http.MethodGet, "/api/system/diagnostic?agents="+traversalID+","+collidingID, nil)
	w := httptest.NewRecorder()
	setup.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	files := readZipFiles(t, w.Body)
	assert.Contains(t, files, "agents/"+traversalID+"/error.txt")
	assert.Contains(t, files, "agents/"+collidingID+"/error.txt")
	assert.NotContains(t, files, "agents/../../escape/error.txt")
	assert.NotContains(t, files, "agents/safe/name/error.txt")

	var meta map[string]any
	require.NoError(t, json.Unmarshal([]byte(files["meta.json"]), &meta))
	warnings, ok := meta["warnings"].([]any)
	require.True(t, ok)
	require.Len(t, warnings, 2)
	assert.Contains(t, warnings[0].(string), traversalID)
	assert.Contains(t, warnings[0].(string), "connection refused")
	assert.Contains(t, warnings[1].(string), collidingID)
	assert.Contains(t, warnings[1].(string), "journalctl unavailable")
}
