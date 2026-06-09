package storagecheck

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vaultfleet/pkg/rcloneobscure"
)

type fakeRunner struct {
	err        error
	calls      int
	configPath string
	args       []string
	onRun      func(t *testing.T, ctx context.Context, configPath string) error
	t          *testing.T
}

func (r *fakeRunner) Run(ctx context.Context, configPath string, args ...string) error {
	r.calls++
	r.configPath = configPath
	r.args = append([]string(nil), args...)
	if r.onRun != nil {
		return r.onRun(r.t, ctx, configPath)
	}
	return r.err
}

func TestServiceRunsRcloneWithTempConfigAndRedactsSecrets(t *testing.T) {
	runner := &fakeRunner{err: errors.New("failed with SECRET456 and token-123")}
	service := NewService(runner)

	result := service.Test(context.Background(), Request{
		RcloneType: "s3",
		RcloneConfig: map[string]string{
			"provider":          "Cloudflare",
			"access_key_id":     "AKID123",
			"secret_access_key": "SECRET456",
			"token":             "token-123",
		},
	})

	assert.False(t, result.OK)
	assert.Contains(t, result.Error, "[redacted]")
	assert.NotContains(t, result.Error, "SECRET456")
	assert.NotContains(t, result.Error, "token-123")
	require.Equal(t, 1, runner.calls)
	assert.True(t, slices.Contains(runner.args, "lsd"), runner.args)
	assert.True(t, slices.Contains(runner.args, "vaultfleet:"), runner.args)
	assertTempConfigRemoved(t, runner.configPath)
}

func TestServiceRedactsSensitiveKeyPatternsFromRunnerError(t *testing.T) {
	runner := &fakeRunner{err: errors.New("api-key-value private-key-value key-pem-value")}
	service := NewService(runner)

	result := service.Test(context.Background(), Request{
		RcloneType: "s3",
		RcloneConfig: map[string]string{
			"api_key":     "api-key-value",
			"private_key": "private-key-value",
			"key_pem":     "key-pem-value",
		},
	})

	assert.False(t, result.OK)
	assert.Contains(t, result.Error, "[redacted]")
	assert.NotContains(t, result.Error, "api-key-value")
	assert.NotContains(t, result.Error, "private-key-value")
	assert.NotContains(t, result.Error, "key-pem-value")
}

func TestServiceRejectsUnsafeRcloneConfigKeysAndValues(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]string
	}{
		{
			name:   "reserved type key",
			config: map[string]string{"type": "sftp"},
		},
		{
			name:   "reserved type key with trailing space",
			config: map[string]string{"type ": "sftp"},
		},
		{
			name:   "reserved type key with leading space",
			config: map[string]string{" type": "sftp"},
		},
		{
			name:   "reserved type key with tab",
			config: map[string]string{"type\t": "sftp"},
		},
		{
			name:   "empty key",
			config: map[string]string{"": "value"},
		},
		{
			name:   "blank key after trim",
			config: map[string]string{" ": "value"},
		},
		{
			name:   "key with equals",
			config: map[string]string{"bad=key": "value"},
		},
		{
			name:   "key with brackets",
			config: map[string]string{"[section]": "value"},
		},
		{
			name:   "key with space",
			config: map[string]string{"bad key": "value"},
		},
		{
			name:   "key with newline",
			config: map[string]string{"bad\nkey": "value"},
		},
		{
			name:   "value with newline",
			config: map[string]string{"provider": "Cloudflare\r\nendpoint = injected"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{}
			service := NewService(runner)

			result := service.Test(context.Background(), Request{
				RcloneType:   "s3",
				RcloneConfig: tt.config,
			})

			assert.False(t, result.OK)
			assert.NotEmpty(t, result.Error)
			assert.Equal(t, 0, runner.calls)
		})
	}
}

func TestServiceUsesS3BucketInTargetAndOmitsBucketFromTempConfig(t *testing.T) {
	runner := &fakeRunner{
		t: t,
		onRun: func(t *testing.T, _ context.Context, configPath string) error {
			t.Helper()

			contents, err := os.ReadFile(configPath)
			require.NoError(t, err)

			config := string(contents)
			assert.NotContains(t, config, "bucket =")
			assert.Contains(t, config, "provider = Cloudflare")
			return nil
		},
	}
	service := NewService(runner)

	result := service.Test(context.Background(), Request{
		RcloneType: "s3",
		RcloneConfig: map[string]string{
			"provider": "Cloudflare",
			"bucket":   " /bucket-a/ ",
		},
	})

	assert.True(t, result.OK)
	require.Equal(t, 1, runner.calls)
	assert.Equal(t, []string{"lsd", "vaultfleet:bucket-a"}, runner.args)
}

func TestServiceUsesS3BucketRootTargetWhenBucketIsEmpty(t *testing.T) {
	runner := &fakeRunner{
		t: t,
		onRun: func(t *testing.T, _ context.Context, configPath string) error {
			t.Helper()

			contents, err := os.ReadFile(configPath)
			require.NoError(t, err)

			assert.NotContains(t, string(contents), "bucket =")
			return nil
		},
	}
	service := NewService(runner)

	result := service.Test(context.Background(), Request{
		RcloneType: "s3",
		RcloneConfig: map[string]string{
			"provider": "Cloudflare",
			"bucket":   "  ",
		},
	})

	assert.True(t, result.OK)
	require.Equal(t, 1, runner.calls)
	assert.Equal(t, []string{"lsd", "vaultfleet:"}, runner.args)
}

func TestServiceRedactsOverlappingSecretsByLongestFirst(t *testing.T) {
	runner := &fakeRunner{err: errors.New("failed with abcdef abc")}
	service := NewService(runner)

	result := service.Test(context.Background(), Request{
		RcloneType: "s3",
		RcloneConfig: map[string]string{
			"token":   "abc",
			"api_key": "abcdef",
		},
	})

	assert.False(t, result.OK)
	assert.Equal(t, "failed with [redacted] [redacted]", result.Error)
	assert.NotContains(t, result.Error, "abcdef")
	assert.NotContains(t, result.Error, "abc")
}

func TestServiceObscuresSFTPPasswordInTempConfig(t *testing.T) {
	runner := &fakeRunner{
		t: t,
		onRun: func(t *testing.T, _ context.Context, configPath string) error {
			t.Helper()

			contents, err := os.ReadFile(configPath)
			require.NoError(t, err)

			config := string(contents)
			assert.NotContains(t, config, "clear-sftp-pass")
			passValue := requireConfigValue(t, config, "pass")
			assert.NotEmpty(t, passValue)
			assert.NotEqual(t, "clear-sftp-pass", passValue)
			revealedPass := revealRcloneObscuredForTest(t, passValue)
			assert.Equal(t, "clear-sftp-pass", revealedPass)
			return nil
		},
	}
	service := NewService(runner)

	result := service.Test(context.Background(), Request{
		RcloneType: "sftp",
		RcloneConfig: map[string]string{
			"host": "sftp.example.test",
			"user": "vaultfleet",
			"pass": "clear-sftp-pass",
		},
	})

	assert.True(t, result.OK)
}

func TestServiceObscuresWebDAVPasswordInTempConfig(t *testing.T) {
	runner := &fakeRunner{
		t: t,
		onRun: func(t *testing.T, _ context.Context, configPath string) error {
			t.Helper()

			info, err := os.Stat(configPath)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

			contents, err := os.ReadFile(configPath)
			require.NoError(t, err)

			config := string(contents)
			assert.NotContains(t, config, "clear-webdav-pass")
			passValue := requireConfigValue(t, config, "pass")
			assert.NotEmpty(t, passValue)
			assert.NotEqual(t, "clear-webdav-pass", passValue)
			revealedPass := revealRcloneObscuredForTest(t, passValue)
			assert.Equal(t, "clear-webdav-pass", revealedPass)
			return nil
		},
	}
	service := NewService(runner)

	result := service.Test(context.Background(), Request{
		RcloneType: "webdav",
		RcloneConfig: map[string]string{
			"url":  "https://example.test/webdav",
			"user": "vaultfleet",
			"pass": "clear-webdav-pass",
		},
	})

	assert.True(t, result.OK)
}

func TestServicePassesTimeoutContextToRunner(t *testing.T) {
	timeout := 10 * time.Millisecond
	var observedDeadline bool
	var observedCancellation bool
	var observedRemaining time.Duration

	runner := &fakeRunner{
		t: t,
		onRun: func(t *testing.T, ctx context.Context, _ string) error {
			t.Helper()

			deadline, ok := ctx.Deadline()
			observedDeadline = ok
			if ok {
				observedRemaining = time.Until(deadline)
			}

			<-ctx.Done()
			observedCancellation = true
			return ctx.Err()
		},
	}
	service := NewService(runner)
	service.Timeout = timeout

	result := service.Test(context.Background(), Request{
		RcloneType:   "s3",
		RcloneConfig: map[string]string{"provider": "Cloudflare"},
	})

	assert.False(t, result.OK)
	assert.NotEmpty(t, result.Error)
	require.True(t, observedDeadline)
	assert.Greater(t, observedRemaining, time.Duration(0))
	assert.LessOrEqual(t, observedRemaining, timeout)
	assert.True(t, observedCancellation)
}

func TestServiceReportsSuccessfulConnection(t *testing.T) {
	runner := &fakeRunner{}
	service := NewService(runner)

	result := service.Test(context.Background(), Request{
		RcloneType:   "s3",
		RcloneConfig: map[string]string{"provider": "Cloudflare"},
	})

	assert.True(t, result.OK)
	assert.GreaterOrEqual(t, result.LatencyMs, int64(0))
	assert.False(t, result.CheckedAt.IsZero())
}

func assertTempConfigRemoved(t *testing.T, configPath string) {
	t.Helper()

	require.NotEmpty(t, configPath)
	assert.True(t, strings.HasSuffix(configPath, "rclone.conf"), configPath)
	_, err := os.Stat(configPath)
	require.True(t, errors.Is(err, os.ErrNotExist), "expected temp config to be removed, got %v", err)
}

func requireConfigValue(t *testing.T, config string, key string) string {
	t.Helper()

	prefix := key + " = "
	for _, line := range strings.Split(config, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	t.Fatalf("missing config key %q in:\n%s", key, config)
	return ""
}

func revealRcloneObscuredForTest(t *testing.T, value string) string {
	t.Helper()

	revealed, err := rcloneobscure.RevealPass(value)
	require.NoError(t, err)
	return revealed
}
