package agent

import (
	"log"
	"os"
	"os/exec"
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

func collectLogs(logFile string, maxBytes int) string {
	source := detectLogSource(logFile)
	switch source {
	case logSourceJournalctl:
		return collectLogsFromJournalctl(maxBytes)
	case logSourceFile:
		return collectLogsFromFile(logFile, maxBytes)
	default:
		return ""
	}
}

func collectLogsFromJournalctl(maxBytes int) string {
	cmd := exec.Command("journalctl", "-u", "vaultfleet-agent", "--since", "24 hours ago", "--no-pager")
	out, err := cmd.Output()
	if err != nil {
		log.Printf("collect journalctl logs failed: %v", err)
		return ""
	}
	return redactAndLimit(string(out), maxBytes)
}

func collectLogsFromFile(path string, maxBytes int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return redactAndLimit(string(data), maxBytes)
}

func redactAndLimit(text string, maxBytes int) string {
	text = redact.Text(text)
	if maxBytes > 0 && len(text) > maxBytes {
		text = text[len(text)-maxBytes:]
	}
	return text
}
