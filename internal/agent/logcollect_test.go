package agent

import (
	"os"
	"path/filepath"
	"strings"
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

	result, err := collectLogsFromFile(logFile, 1024*1024)
	require.NoError(t, err)
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

	result, err := collectLogsFromFile(logFile, 50)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(result), 50)
}

func TestCollectLogsFromFile_TailReadsLargeFile(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "agent.log")
	file, err := os.Create(logFile)
	require.NoError(t, err)
	_, err = file.Seek(32*1024*1024, 0)
	require.NoError(t, err)
	_, err = file.WriteString("tail-line\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())

	result, err := collectLogsFromFile(logFile, len("tail-line\n"))

	require.NoError(t, err)
	assert.Equal(t, "tail-line\n", result)
}

func TestReadTailReturnsOnlyRequestedBytes(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "agent.log")
	require.NoError(t, os.WriteFile(logFile, []byte(strings.Repeat("A", 1024)+"tail-line\n"), 0o644))

	data, err := readTail(logFile, len("tail-line\n"))

	require.NoError(t, err)
	assert.Equal(t, "tail-line\n", string(data))
}

func TestCollectLogsFromJournalctl_LimitsOutputAtSource(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(binDir, 0o755))
	argsFile := filepath.Join(dir, "args.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\nprintf 'line1\\npassword=secret\\nline3\\n'\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "journalctl"), []byte(script), 0o755))
	t.Setenv("PATH", binDir)

	result, err := collectLogsFromJournalctl(12)

	require.NoError(t, err)
	assert.LessOrEqual(t, len(result), 12)
	assert.NotContains(t, result, "secret")
	args, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.Contains(t, string(args), "--lines")
}

func TestCollectLogs_TruncationAfterRedaction(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "agent.log")
	require.NoError(t, os.WriteFile(logFile, []byte("password=x\n"), 0o644))

	result, err := collectLogsFromFile(logFile, len("password=x\n"))
	require.NoError(t, err)

	assert.LessOrEqual(t, len(result), len("password=x\n"))
	assert.NotContains(t, result, "x")
}

func TestCollectLogs_TruncationDoesNotExposeClippedSecret(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "agent.log")
	require.NoError(t, os.WriteFile(logFile, []byte("password=very-secret-value\n"), 0o644))

	result, err := collectLogsFromFile(logFile, len("secret-value\n"))
	require.NoError(t, err)

	assert.LessOrEqual(t, len(result), len("secret-value\n"))
	assert.NotContains(t, result, "very-secret-value")
	assert.NotContains(t, result, "secret-value")
}

func TestCollectLogs_MissingFile(t *testing.T) {
	result, err := collectLogsFromFile("/nonexistent/path/agent.log", 1024)
	assert.Equal(t, "", result)
	require.Error(t, err)
}

func TestCollectLogsFromFile_ReturnsReadError(t *testing.T) {
	logs, err := collectLogsFromFile("/nonexistent/path/agent.log", 1024)

	assert.Empty(t, logs)
	require.Error(t, err)
}

func TestCollectLogs_ReturnsNoSourceError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	logs, err := collectLogs(filepath.Join(dir, "missing.log"), 1024)

	assert.Empty(t, logs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no agent log source found")
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

func TestHandler_InjectedLogFileBypassesJournalctlDetection(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "agent.log")
	require.NoError(t, os.WriteFile(logFile, []byte("injected log password=file-secret\n"), 0o644))

	binDir := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(binDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "systemctl"), []byte("#!/bin/sh\nprintf active\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "journalctl"), []byte("#!/bin/sh\nprintf 'host journal token=host-secret\\n'\n"), 0o755))
	t.Setenv("PATH", binDir)

	sent := &sentMessages{}
	handler := NewHandler(HandlerConfig{SendFunc: sent.send, LogFile: logFile})
	msg, err := protocol.NewMessage(protocol.TypeCollectLogsReq, protocol.CollectLogsReqPayload{MaxBytes: 1024})
	require.NoError(t, err)

	handler.Handle(*msg)

	require.Len(t, sent.messages, 1)
	payload, err := protocol.ParsePayload[protocol.CollectLogsRespPayload](&sent.messages[0])
	require.NoError(t, err)
	assert.Contains(t, payload.Logs, "injected log")
	assert.NotContains(t, payload.Logs, "host journal")
	assert.NotContains(t, payload.Logs, "file-secret")
	assert.NotContains(t, payload.Logs, "host-secret")
}

func TestHandler_HandleCollectLogsReqReportsReadError(t *testing.T) {
	sent := &sentMessages{}
	handler := NewHandler(HandlerConfig{SendFunc: sent.send, LogFile: "/nonexistent/path/agent.log"})
	msg, err := protocol.NewMessage(protocol.TypeCollectLogsReq, protocol.CollectLogsReqPayload{MaxBytes: 1024})
	require.NoError(t, err)

	handler.Handle(*msg)

	require.Len(t, sent.messages, 1)
	payload, err := protocol.ParsePayload[protocol.CollectLogsRespPayload](&sent.messages[0])
	require.NoError(t, err)
	assert.Empty(t, payload.Logs)
	assert.Contains(t, payload.Error, "/nonexistent/path/agent.log")
}
