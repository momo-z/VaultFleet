package executor

import (
	"context"
	"strings"
	"testing"
)

func TestPlainRunnerMaintenanceUnsupported(t *testing.T) {
	r := PlainRunner{RepoPath: "repo"}
	_, err := r.RunMaintenance(context.Background(), OpCheck)
	if err == nil || !strings.Contains(err.Error(), "not supported for plain backups") {
		t.Fatalf("err = %v, want not-supported error", err)
	}
}
