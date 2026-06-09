package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vaultfleet/pkg/rcloneobscure"
)

func TestGenerateRcloneConfS3SortedOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rclone.conf")
	config := RcloneConfig{
		Type: "s3",
		Params: map[string]string{
			"secret_access_key": "secret",
			"provider":          "AWS",
			"access_key_id":     "key",
			"region":            "us-east-1",
		},
	}

	if err := WriteRcloneConf(path, config); err != nil {
		t.Fatalf("WriteRcloneConf() error = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}

	want := "[vaultfleet]\n" +
		"type = s3\n" +
		"access_key_id = key\n" +
		"provider = AWS\n" +
		"region = us-east-1\n" +
		"secret_access_key = secret\n"
	if string(got) != want {
		t.Fatalf("generated config mismatch\nwant:\n%s\ngot:\n%s", want, string(got))
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat generated config: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("config mode = %o, want 600", gotMode)
	}
}

func TestGenerateRcloneConfSFTPPasswordObscured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rclone.conf")
	config := RcloneConfig{
		Type: "sftp",
		Params: map[string]string{
			"host": "sftp.example.test",
			"user": "vaultfleet",
			"pass": "clear-sftp-password",
			"port": "22",
		},
	}

	if err := WriteRcloneConf(path, config); err != nil {
		t.Fatalf("WriteRcloneConf() error = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}

	passValue := configValue(t, string(got), "pass")
	if passValue == "clear-sftp-password" {
		t.Fatalf("sftp pass was written in clear text")
	}
	revealed, err := rcloneobscure.RevealPass(passValue)
	if err != nil {
		t.Fatalf("reveal generated sftp pass: %v", err)
	}
	if revealed != "clear-sftp-password" {
		t.Fatalf("revealed pass = %q, want original secret", revealed)
	}
}

func TestGenerateRcloneConfWebDAVContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rclone.conf")
	config := RcloneConfig{
		Type: "webdav",
		Params: map[string]string{
			"url":    "https://dav.example.test/remote.php/dav/files/user",
			"vendor": "nextcloud",
			"user":   "user@example.test",
			"pass":   "clear-webdav-password",
		},
	}

	if err := WriteRcloneConf(path, config); err != nil {
		t.Fatalf("WriteRcloneConf() error = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}

	for _, want := range []string{
		"[vaultfleet]\n",
		"type = webdav\n",
		"url = https://dav.example.test/remote.php/dav/files/user\n",
		"user = user@example.test\n",
		"vendor = nextcloud\n",
	} {
		if !containsLine(string(got), want) {
			t.Fatalf("generated config missing %q in:\n%s", want, string(got))
		}
	}

	passValue := configValue(t, string(got), "pass")
	if passValue == "clear-webdav-password" {
		t.Fatalf("webdav pass was written in clear text")
	}
	revealed, err := rcloneobscure.RevealPass(passValue)
	if err != nil {
		t.Fatalf("reveal generated webdav pass: %v", err)
	}
	if revealed != "clear-webdav-password" {
		t.Fatalf("revealed pass = %q, want original secret", revealed)
	}
}

func configValue(t *testing.T, content string, key string) string {
	t.Helper()

	prefix := key + " = "
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	t.Fatalf("config missing %q line in:\n%s", key, content)
	return ""
}

func containsLine(content, line string) bool {
	return len(line) == 0 || (len(content) >= len(line) && contains(content, line))
}

func contains(content, substr string) bool {
	for i := 0; i+len(substr) <= len(content); i++ {
		if content[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
