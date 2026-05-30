package rcloneobscure

import "testing"

func TestObscurePassIfNeededPreservesAlreadyObscuredValue(t *testing.T) {
	obscured, err := ObscurePass("clear-sftp-password")
	if err != nil {
		t.Fatalf("ObscurePass() error = %v", err)
	}

	got, err := ObscurePassIfNeeded(obscured)
	if err != nil {
		t.Fatalf("ObscurePassIfNeeded() error = %v", err)
	}
	if got != obscured {
		t.Fatalf("ObscurePassIfNeeded() = %q, want unchanged obscured value", got)
	}

	revealed, err := RevealPass(got)
	if err != nil {
		t.Fatalf("RevealPass() error = %v", err)
	}
	if revealed != "clear-sftp-password" {
		t.Fatalf("RevealPass() = %q, want original password", revealed)
	}
}

func TestPrepareConfigForAgentObscuresPass(t *testing.T) {
	prepared, err := PrepareConfigForAgent(map[string]string{
		"host": "sftp.example.test",
		"user": "vaultfleet",
		"pass": "clear-sftp-password",
	})
	if err != nil {
		t.Fatalf("PrepareConfigForAgent() error = %v", err)
	}
	if prepared["pass"] == "clear-sftp-password" {
		t.Fatal("pass was written in clear text")
	}
	revealed, err := RevealPass(prepared["pass"])
	if err != nil {
		t.Fatalf("RevealPass() error = %v", err)
	}
	if revealed != "clear-sftp-password" {
		t.Fatalf("RevealPass() = %q, want original password", revealed)
	}
}
