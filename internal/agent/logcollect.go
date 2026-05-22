package agent

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"vaultfleet/pkg/redact"
)

type logSource int

const (
	logSourceJournalctl logSource = iota
	logSourceFile
	logSourceNone
)

func detectLogSource(fallbackLogFile string) logSource {
	if _, err := exec.LookPath("journalctl"); err == nil {
		out, err := exec.Command("systemctl", "is-active", "vaultfleet-agent").Output()
		if err == nil && strings.TrimSpace(string(out)) == "active" {
			return logSourceJournalctl
		}
	}
	if _, err := os.Stat(fallbackLogFile); err == nil {
		return logSourceFile
	}
	return logSourceNone
}

const defaultLogFile = "/var/log/vaultfleet-agent.log"
const redactionTailContextBytes = 64 * 1024

func collectLogs(logFile string, maxBytes int) (string, error) {
	source := detectLogSource(logFile)
	switch source {
	case logSourceJournalctl:
		return collectLogsFromJournalctl(maxBytes)
	case logSourceFile:
		return collectLogsFromFile(logFile, maxBytes)
	default:
		return "", fmt.Errorf("no agent log source found")
	}
}

func collectLogsFromJournalctl(maxBytes int) (string, error) {
	lines := journalctlLineLimit(maxBytes)
	cmd := exec.Command("journalctl", "-u", "vaultfleet-agent", "--since", "24 hours ago", "--no-pager", "--lines", strconv.Itoa(lines))
	out, err := cmd.Output()
	if err != nil {
		log.Printf("collect journalctl logs failed: %v", err)
		return "", fmt.Errorf("collect journalctl logs: %w", err)
	}
	return redactAndLimit(string(out), maxBytes), nil
}

func collectLogsFromFile(path string, maxBytes int) (string, error) {
	data, err := readTail(path, tailReadBytes(maxBytes))
	if err != nil {
		return "", fmt.Errorf("read log file %s: %w", path, err)
	}
	return redactAndLimit(string(data), maxBytes), nil
}

func tailReadBytes(maxBytes int) int {
	if maxBytes <= 0 {
		return redactionTailContextBytes
	}
	return maxBytes + redactionTailContextBytes
}

func readTail(path string, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 {
		return os.ReadFile(path)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}

	size := info.Size()
	offset := size - int64(maxBytes)
	if offset < 0 {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(file)
}

func journalctlLineLimit(maxBytes int) int {
	if maxBytes <= 0 {
		return 2000
	}
	lines := maxBytes / 256
	if maxBytes%256 != 0 {
		lines++
	}
	if lines < 50 {
		return 50
	}
	if lines > 2000 {
		return 2000
	}
	return lines
}

func redactAndLimit(text string, maxBytes int) string {
	text = redact.Text(text)
	if maxBytes > 0 && len(text) > maxBytes {
		text = text[len(text)-maxBytes:]
	}
	return text
}
