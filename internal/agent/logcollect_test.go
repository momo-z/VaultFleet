package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vaultfleet/pkg/protocol"
)

func TestCollectLogs_FromFile(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "agent.log")

	now := time.Now()
	lines := now.Add(-2*time.Hour).Format(time.RFC3339) + " early line\n" +
		now.Add(-30*time.Minute).Format(time.RFC3339) + " recent line password=secret123\n" +
		now.Add(-5*time.Minute).Format(time.RFC3339) + " latest line\n"
	require.NoError(t, os.WriteFile(logFile, []byte(lines), 0o644))

	result := collectLogsFromFile(logFile, 1024*1024)
	assert.Contains(t, result, "latest line")
	assert.Contains(t, result, "[REDACTED]")
	assert.NotContains(t, result, "secret123")
}

func TestCollectLogs_Truncation(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "agent.log")

	data := make([]byte, 100)
	for i := range data {
		data[i] = 'A'
	}
	data[99] = '\n'
	require.NoError(t, os.WriteFile(logFile, data, 0o644))

	result := collectLogsFromFile(logFile, 50)
	assert.LessOrEqual(t, len(result), 50)
}

func TestCollectLogs_TruncationAfterRedaction(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "agent.log")
	require.NoError(t, os.WriteFile(logFile, []byte("password=x\n"), 0o644))

	result := collectLogsFromFile(logFile, len("password=x\n"))

	assert.LessOrEqual(t, len(result), len("password=x\n"))
	assert.NotContains(t, result, "x")
}

func TestCollectLogs_TruncationDoesNotExposeClippedSecret(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "agent.log")
	require.NoError(t, os.WriteFile(logFile, []byte("password=very-secret-value\n"), 0o644))

	result := collectLogsFromFile(logFile, len("secret-value\n"))

	assert.LessOrEqual(t, len(result), len("secret-value\n"))
	assert.NotContains(t, result, "very-secret-value")
	assert.NotContains(t, result, "secret-value")
}

func TestCollectLogs_MissingFile(t *testing.T) {
	result := collectLogsFromFile("/nonexistent/path/agent.log", 1024)
	assert.Equal(t, "", result)
}

func TestDetectLogSource_Fallback(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "agent.log")
	require.NoError(t, os.WriteFile(logFile, []byte("test\n"), 0o644))

	source := detectLogSource(logFile)
	assert.Equal(t, logSourceFile, source)
}

func TestHandler_HandleCollectLogsReq(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "agent.log")
	require.NoError(t, os.WriteFile(logFile, []byte("agent log token: abc\n"), 0o644))

	sent := &sentMessages{}
	handler := NewHandler(HandlerConfig{SendFunc: sent.send, LogFile: logFile})
	msg, err := protocol.NewMessage(protocol.TypeCollectLogsReq, protocol.CollectLogsReqPayload{MaxBytes: 1024})
	require.NoError(t, err)

	handler.Handle(*msg)

	require.Len(t, sent.messages, 1)
	resp := sent.messages[0]
	assert.Equal(t, protocol.TypeCollectLogsResp, resp.Type)
	assert.Equal(t, msg.ID, resp.ID)
	payload, err := protocol.ParsePayload[protocol.CollectLogsRespPayload](&resp)
	require.NoError(t, err)
	assert.Contains(t, payload.Logs, "agent log")
	assert.Contains(t, payload.Logs, "[REDACTED]")
	assert.NotContains(t, payload.Logs, "abc")
}
