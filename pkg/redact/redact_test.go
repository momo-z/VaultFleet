package redact

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRedactText_PasswordKeyValue(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"password=value", `password=secret123`, `password=[REDACTED]`},
		{"PASSWORD=value", `PASSWORD=secret123`, `PASSWORD=[REDACTED]`},
		{"token: value", `token: abc-xyz-123`, `token: [REDACTED]`},
		{"secret_key = value", `secret_key = my-secret`, `secret_key = [REDACTED]`},
		{"api_key=value", `api_key=AKIAIOSFODNN7EXAMPLE`, `api_key=[REDACTED]`},
		{"access_key=value", `access_key=AKIAIOSFODNN7EXAMPLE`, `access_key=[REDACTED]`},
		{"private_key=val", `private_key=pk_live_xxx`, `private_key=[REDACTED]`},
		{"credential=val", `credential=cred_abc`, `credential=[REDACTED]`},
		{"cookie=val", `cookie=sess_abc123`, `cookie=[REDACTED]`},
		{"auth=val", `auth=bearer_token_here`, `auth=[REDACTED]`},
		{"bearer token", `Authorization: Bearer eyJhbGciOiJIUzI1`, `Authorization: Bearer [REDACTED]`},
		{"basic auth", `Authorization: Basic Zm9vOmJhcg==`, `Authorization: Basic [REDACTED]`},
		{"pass=val", `pass=webdav-password`, `pass=[REDACTED]`},
		{"key_pem=val", `key_pem=-----BEGIN`, `key_pem=[REDACTED]`},
		{"custom_key=val", `custom_key=custom-secret`, `custom_key=[REDACTED]`},
		{"secret_access_key=val", `secret_access_key=s3-secret`, `secret_access_key=[REDACTED]`},
		{"no match", `this is a normal log line`, `this is a normal log line`},
		{"mixed line", `connecting to server password=abc123 on port 8080`, `connecting to server password=[REDACTED] on port 8080`},
		{"passwd=val", `passwd=hunter2`, `passwd=[REDACTED]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Text(tt.input))
		})
	}
}

func TestRedactText_MultiLine(t *testing.T) {
	input := "line1 normal\npassword=secret\nline3 token: abc\n"
	want := "line1 normal\npassword=[REDACTED]\nline3 token: [REDACTED]\n"
	assert.Equal(t, want, Text(input))
}

func TestRedactJSON_StorageFields(t *testing.T) {
	input := map[string]any{
		"name":       "my-storage",
		"endpoint":   "https://s3.example.com",
		"access_key": "AKIAIOSFODNN7EXAMPLE",
		"secret_key": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLE",
		"bucket":     "my-bucket",
		"password":   "p@ssw0rd",
		"region":     "us-east-1",
	}
	result := JSONFields(input, "access_key", "secret_key", "password", "endpoint")
	assert.Equal(t, "[REDACTED]", result["access_key"])
	assert.Equal(t, "[REDACTED]", result["secret_key"])
	assert.Equal(t, "[REDACTED]", result["password"])
	assert.Equal(t, "[REDACTED]", result["endpoint"])
	assert.Equal(t, "my-storage", result["name"])
	assert.Equal(t, "my-bucket", result["bucket"])
	assert.Equal(t, "us-east-1", result["region"])
}
