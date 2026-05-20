package storagecheck

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Runner interface {
	Run(ctx context.Context, configPath string, args ...string) error
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, configPath string, args ...string) error {
	commandArgs := append([]string{"--config", configPath}, args...)
	cmd := exec.CommandContext(ctx, "rclone", commandArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if len(output) == 0 {
			return err
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

type Service struct {
	Runner  Runner
	Now     func() time.Time
	Timeout time.Duration
}

type Request struct {
	RcloneType   string
	RcloneConfig map[string]string
}

type Result struct {
	OK        bool      `json:"ok"`
	LatencyMs int64     `json:"latency_ms"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

func NewService(runner Runner) *Service {
	if runner == nil {
		runner = ExecRunner{}
	}
	return &Service{
		Runner:  runner,
		Now:     time.Now,
		Timeout: 15 * time.Second,
	}
}

func (s *Service) Test(ctx context.Context, request Request) Result {
	now := s.now()
	start := now
	result := Result{CheckedAt: now}

	tempDir, err := os.MkdirTemp("", "vaultfleet-rclone-*")
	if err != nil {
		result.Error = s.redactError(err, request)
		result.LatencyMs = s.latencySince(start)
		return result
	}
	defer os.RemoveAll(tempDir)

	configPath := filepath.Join(tempDir, "rclone.conf")
	if err := os.WriteFile(configPath, []byte(rcloneConfigContents(request)), 0o600); err != nil {
		result.Error = s.redactError(err, request)
		result.LatencyMs = s.latencySince(start)
		return result
	}

	runCtx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()

	if err := s.runner().Run(runCtx, configPath, "lsd", "vaultfleet:"); err != nil {
		result.Error = s.redactError(err, request)
		result.LatencyMs = s.latencySince(start)
		return result
	}

	result.OK = true
	result.LatencyMs = s.latencySince(start)
	return result
}

func (s *Service) runner() Runner {
	if s.Runner != nil {
		return s.Runner
	}
	return ExecRunner{}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return 15 * time.Second
}

func (s *Service) latencySince(start time.Time) int64 {
	return s.now().Sub(start).Milliseconds()
}

func rcloneConfigContents(request Request) string {
	var builder strings.Builder
	builder.WriteString("[vaultfleet]\n")
	builder.WriteString("type = ")
	builder.WriteString(request.RcloneType)
	builder.WriteString("\n")

	keys := make([]string, 0, len(request.RcloneConfig))
	for key := range request.RcloneConfig {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteString(" = ")
		builder.WriteString(request.RcloneConfig[key])
		builder.WriteString("\n")
	}
	return builder.String()
}

func (s *Service) redactError(err error, request Request) string {
	message := err.Error()
	for key, value := range request.RcloneConfig {
		if value == "" || !isSecretKey(key) {
			continue
		}
		message = strings.ReplaceAll(message, value, "[redacted]")
	}
	return message
}

func isSecretKey(key string) bool {
	normalized := strings.ToLower(key)
	return normalized == "pass" ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "password") ||
		strings.Contains(normalized, "access_key")
}
