package build

import (
	"os"
	"strings"
	"testing"
)

func TestInstallScriptMatchesAcceptanceContract(t *testing.T) {
	data, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)

	if strings.Contains(script, `-z "$MASTER_URL" || -z "$ENROLL_TOKEN" || -z "$AGENT_URL" || -z "$AGENT_SHA256"`) {
		t.Fatal("agent url and sha256 must be optional for acceptance installer flow")
	}
	for _, want := range []string{
		`GITHUB_REPO="momo-z/VaultFleet"`,
		`GITHUB_PROXY=""`,
		`--github-proxy`,
		`--agent-url <agent-binary-url>`,
		`AGENT_URL="${GITHUB_RELEASE_BASE}/vaultfleet-agent-linux-${ARCH}"`,
		`AGENT_URL="${GITHUB_PROXY%/}/${AGENT_URL}"`,
		`if [[ ! -x "${INSTALL_DIR}/restic" ]]; then`,
		`if [[ ! -x "${INSTALL_DIR}/rclone" ]]; then`,
		"ensure_command bunzip2 bzip2",
		"ensure_command unzip unzip",
		"apt-get install -y --no-install-recommends",
		"apk add --no-cache",
		"command -v rc-update",
		"rc-service vaultfleet-agent restart",
		"nohup \"$INSTALL_DIR/vaultfleet-agent\" --config \"$CONFIG_DIR/agent.yaml\"",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("install.sh missing %q", want)
		}
	}
}
