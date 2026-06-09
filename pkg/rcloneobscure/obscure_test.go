package rcloneobscure

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigValueObscuresBase64LikePlaintextPass(t *testing.T) {
	const plaintext = "abcdefghijklmnopQRSTUV"

	got, err := ConfigValue("pass", plaintext, false)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, got)

	revealed, err := RevealPass(got)
	require.NoError(t, err)
	assert.Equal(t, plaintext, revealed)
}

func TestConfigValuePreservesExplicitlyObscuredPass(t *testing.T) {
	obscured, err := ObscurePass("clear-sftp-password")
	require.NoError(t, err)

	got, err := ConfigValue("pass", obscured, true)
	require.NoError(t, err)
	assert.Equal(t, obscured, got)

	revealed, err := RevealPass(got)
	require.NoError(t, err)
	assert.Equal(t, "clear-sftp-password", revealed)
}

func TestPrepareConfigForLegacyAgentObscuresPass(t *testing.T) {
	prepared, err := PrepareConfigForLegacyAgent(map[string]string{
		"host": "sftp.example.test",
		"user": "vaultfleet",
		"pass": "clear-sftp-password",
	})
	require.NoError(t, err)
	assert.NotEqual(t, "clear-sftp-password", prepared["pass"])

	revealed, err := RevealPass(prepared["pass"])
	require.NoError(t, err)
	assert.Equal(t, "clear-sftp-password", revealed)
}
