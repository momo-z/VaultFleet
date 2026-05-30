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

	"vaultfleet/pkg/rcloneobscure"
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

	if err := ValidateRequest(request); err != nil {
		result.Error = s.redactError(err, request)
		result.LatencyMs = s.latencySince(start)
		return result
	}

	tempDir, err := os.MkdirTemp("", "vaultfleet-rclone-*")
	if err != nil {
		result.Error = s.redactError(err, request)
		result.LatencyMs = s.latencySince(start)
		return result
	}
	defer os.RemoveAll(tempDir)

	configPath := filepath.Join(tempDir, "rclone.conf")
	configContents, err := rcloneConfigContents(request)
	if err != nil {
		result.Error = s.redactError(err, request)
		result.LatencyMs = s.latencySince(start)
		return result
	}

	if err := os.WriteFile(configPath, []byte(configContents), 0o600); err != nil {
		result.Error = s.redactError(err, request)
		result.LatencyMs = s.latencySince(start)
		return result
	}

	runCtx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()

	if err := s.runner().Run(runCtx, configPath, "lsd", rcloneTestTarget(request)); err != nil {
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

func rcloneConfigContents(request Request) (string, error) {
	if err := ValidateRequest(request); err != nil {
		return "", err
	}

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
		if shouldOmitConfigKey(request.RcloneType, key) {
			continue
		}
		value, err := rcloneobscure.ConfigValue(key, request.RcloneConfig[key])
		if err != nil {
			return "", err
		}
		builder.WriteString(key)
		builder.WriteString(" = ")
		builder.WriteString(value)
		builder.WriteString("\n")
	}
	return builder.String(), nil
}

func rcloneTestTarget(request Request) string {
	if request.RcloneType == "s3" {
		bucket := S3BucketPathSegment(request.RcloneConfig["bucket"])
		if bucket != "" {
			return "vaultfleet:" + bucket
		}
	}
	return "vaultfleet:"
}

func S3BucketPathSegment(bucket string) string {
	return strings.Trim(strings.TrimSpace(bucket), "/")
}

func shouldOmitConfigKey(rcloneType string, key string) bool {
	return rcloneType == "s3" && key == "bucket"
}

func ValidateRequest(request Request) error {
	if containsLineBreak(request.RcloneType) {
		return fmt.Errorf("rclone type contains invalid characters")
	}
	for key, value := range request.RcloneConfig {
		if err := validateRcloneConfigKey(key); err != nil {
			return err
		}
		if containsLineBreak(value) {
			return fmt.Errorf("rclone config value contains invalid characters")
		}
	}
	return nil
}

func validateRcloneConfigKey(key string) error {
	trimmed := strings.TrimSpace(key)
	switch {
	case trimmed == "":
		return fmt.Errorf("rclone config key cannot be empty")
	case strings.EqualFold(trimmed, "type"):
		return fmt.Errorf("rclone config key is reserved")
	case key != trimmed:
		return fmt.Errorf("rclone config key contains invalid characters")
	case !isSafeRcloneConfigKey(key):
		return fmt.Errorf("rclone config key contains invalid characters")
	}
	return nil
}

func isSafeRcloneConfigKey(key string) bool {
	for i := 0; i < len(key); i++ {
		char := key[i]
		if char >= 'a' && char <= 'z' {
			continue
		}
		if char >= 'A' && char <= 'Z' {
			continue
		}
		if char >= '0' && char <= '9' {
			continue
		}
		if char == '_' {
			continue
		}
		return false
	}
	return true
}

func containsLineBreak(value string) bool {
	return strings.ContainsAny(value, "\r\n")
}

func (s *Service) redactError(err error, request Request) string {
	message := err.Error()
	values := secretValuesByLength(request.RcloneConfig)
	for _, value := range values {
		message = strings.ReplaceAll(message, value, "[redacted]")
	}
	return message
}

func secretValuesByLength(config map[string]string) []string {
	values := make([]string, 0, len(config))
	seen := make(map[string]bool, len(config))
	for key, value := range config {
		if value == "" || !IsSecretConfigKey(key) {
			continue
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		return len(values[i]) > len(values[j])
	})
	return values
}

func IsSecretConfigKey(key string) bool {
	normalized := strings.ToLower(key)
	return normalized == "pass" ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "password") ||
		strings.Contains(normalized, "access_key") ||
		strings.Contains(normalized, "api_key") ||
		strings.Contains(normalized, "private_key") ||
		strings.Contains(normalized, "key_pem") ||
		strings.HasSuffix(normalized, "_key")
}
