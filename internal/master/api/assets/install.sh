#!/usr/bin/env bash
set -euo pipefail

MASTER_URL=""
ENROLL_TOKEN=""
AGENT_URL=""
AGENT_SHA256=""
GITHUB_PROXY=""
GITHUB_REPO="momo-z/VaultFleet"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/vaultfleet"
RESTIC_VERSION="0.17.3"
RCLONE_VERSION="1.70.3"

RESTIC_SHA256_AMD64="5097faeda6aa13167aae6e36efdba636637f8741fed89bbf015678334632d4d3"
RESTIC_SHA256_ARM64="db27b803534d301cef30577468cf61cb2e242165b8cd6d8cd6efd7001be2e557"
RCLONE_SHA256_AMD64="7d69057e69385f6514a9684c7eaa424d972096b130284bb34dd967c4ed4f9dad"
RCLONE_SHA256_ARM64="1b08be34622f1f9bb9b069a85d036bba822b690790215c18a9418dbaf19505fe"

usage() {
    echo "Usage: $0 --server <master-url> --token <enroll-token> [--github-proxy <proxy-url>] [--agent-url <agent-binary-url>] [--agent-sha256 <sha256>]"
    exit 1
}

require_command() {
    local name="$1"
    if ! command -v "$name" &>/dev/null; then
        echo "Required command not found: $name"
        exit 1
    fi
}

ensure_command() {
    local name="$1"
    local package="${2:-$1}"
    if command -v "$name" &>/dev/null; then
        return
    fi

    echo "==> Installing dependency ${package}..."
    if command -v apt-get &>/dev/null; then
        export DEBIAN_FRONTEND=noninteractive
        apt-get update
        apt-get install -y --no-install-recommends "$package"
    elif command -v apk &>/dev/null; then
        apk add --no-cache "$package"
    elif command -v dnf &>/dev/null; then
        dnf install -y "$package"
    elif command -v yum &>/dev/null; then
        yum install -y "$package"
    else
        echo "Required command not found: $name (install package: $package)"
        exit 1
    fi

    require_command "$name"
}

while [[ $# -gt 0 ]]; do
    case $1 in
        --server)
            [[ $# -ge 2 && "$2" != --* ]] || usage
            MASTER_URL="$2"
            shift 2
            ;;
        --token)
            [[ $# -ge 2 && "$2" != --* ]] || usage
            ENROLL_TOKEN="$2"
            shift 2
            ;;
        --agent-url)
            [[ $# -ge 2 && "$2" != --* ]] || usage
            AGENT_URL="$2"
            shift 2
            ;;
        --github-proxy)
            [[ $# -ge 2 && "$2" != --* ]] || usage
            GITHUB_PROXY="$2"
            shift 2
            ;;
        --agent-sha256)
            [[ $# -ge 2 && "$2" != --* ]] || usage
            AGENT_SHA256="$2"
            shift 2
            ;;
        *) usage ;;
    esac
done

if [[ -z "$MASTER_URL" || -z "$ENROLL_TOKEN" ]]; then
    usage
fi

if [[ "$(id -u)" -ne 0 ]]; then
    echo "This installer must run as root"
    exit 1
fi

require_command curl
require_command install
require_command mktemp

ARCH=$(uname -m)
case "$ARCH" in
    x86_64)
        ARCH="amd64"
        RESTIC_SHA256="$RESTIC_SHA256_AMD64"
        RCLONE_SHA256="$RCLONE_SHA256_AMD64"
        ;;
    aarch64)
        ARCH="arm64"
        RESTIC_SHA256="$RESTIC_SHA256_ARM64"
        RCLONE_SHA256="$RCLONE_SHA256_ARM64"
        ;;
    *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
if [[ "$OS" != "linux" ]]; then
    echo "Unsupported OS: $OS (only Linux is supported)"
    exit 1
fi

if [[ -z "$AGENT_URL" ]]; then
    GITHUB_RELEASE_BASE="https://github.com/${GITHUB_REPO}/releases/latest/download"
    AGENT_URL="${GITHUB_RELEASE_BASE}/vaultfleet-agent-linux-${ARCH}"
    if [[ -n "$GITHUB_PROXY" ]]; then
        AGENT_URL="${GITHUB_PROXY%/}/${AGENT_URL}"
    fi
fi
if [[ -n "$AGENT_SHA256" && ! "$AGENT_SHA256" =~ ^[0-9a-fA-F]{64}$ ]]; then
    echo "Agent SHA256 must be a 64-character hex digest"
    exit 1
fi

echo "==> Installing VaultFleet Agent (${OS}/${ARCH})"

tmp_dir="$(mktemp -d)"
cleanup() {
    rm -rf "$tmp_dir"
}
trap cleanup EXIT

install_file() {
    local source="$1"
    local target="$2"
    install -m 0755 -o root -g root "$source" "$target"
}

verify_sha256() {
    local expected="$1"
    local file="$2"
    printf '%s  %s\n' "$expected" "$file" | sha256sum -c -
}

echo "==> Downloading vaultfleet-agent from ${AGENT_URL}..."
agent_tmp="${tmp_dir}/vaultfleet-agent"
curl -fsSL "$AGENT_URL" -o "$agent_tmp"
if [[ -n "$AGENT_SHA256" ]]; then
    require_command sha256sum
    verify_sha256 "$AGENT_SHA256" "$agent_tmp"
fi
chmod +x "$agent_tmp"
if ! "$agent_tmp" --help >/dev/null 2>&1; then
    echo "Downloaded agent binary failed startup validation"
    exit 1
fi
install_file "$agent_tmp" "${INSTALL_DIR}/vaultfleet-agent"

if [[ ! -x "${INSTALL_DIR}/restic" ]]; then
	echo "==> Downloading restic..."
	ensure_command bunzip2 bzip2
	ensure_command sha256sum coreutils
    restic_archive="${tmp_dir}/restic.bz2"
    restic_bin="${tmp_dir}/restic"
    curl -fsSL "https://github.com/restic/restic/releases/download/v${RESTIC_VERSION}/restic_${RESTIC_VERSION}_${OS}_${ARCH}.bz2" -o "$restic_archive"
    verify_sha256 "$RESTIC_SHA256" "$restic_archive"
    bunzip2 -c "$restic_archive" > "$restic_bin"
    chmod +x "$restic_bin"
	install_file "$restic_bin" "${INSTALL_DIR}/restic"
fi

if [[ ! -x "${INSTALL_DIR}/rclone" ]]; then
	echo "==> Downloading rclone..."
	ensure_command unzip unzip
	ensure_command sha256sum coreutils
    rclone_archive="${tmp_dir}/rclone.zip"
    rclone_extract="${tmp_dir}/rclone"
    curl -fsSL "https://downloads.rclone.org/v${RCLONE_VERSION}/rclone-v${RCLONE_VERSION}-${OS}-${ARCH}.zip" -o "$rclone_archive"
    verify_sha256 "$RCLONE_SHA256" "$rclone_archive"
    unzip -q "$rclone_archive" -d "$rclone_extract"
    install_file "${rclone_extract}/rclone-v${RCLONE_VERSION}-${OS}-${ARCH}/rclone" "${INSTALL_DIR}/rclone"
fi

echo "==> Creating config directory..."
mkdir -p "$CONFIG_DIR"
chmod 700 "$CONFIG_DIR"

echo "==> Enrolling agent with master..."
"${INSTALL_DIR}/vaultfleet-agent" \
    --enroll-only \
    --server "$MASTER_URL" \
    --token "$ENROLL_TOKEN" \
    --config "${CONFIG_DIR}/agent.yaml"

if command -v systemctl &>/dev/null; then
    echo "==> Creating systemd service..."
    cat > /etc/systemd/system/vaultfleet-agent.service <<EOF
[Unit]
Description=VaultFleet Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/vaultfleet-agent --config ${CONFIG_DIR}/agent.yaml
Restart=always
RestartSec=10
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

    echo "==> Starting vaultfleet-agent service..."
    systemctl daemon-reload
    systemctl enable vaultfleet-agent
    systemctl restart vaultfleet-agent
elif command -v rc-update &>/dev/null && command -v rc-service &>/dev/null; then
    echo "==> Creating OpenRC service..."
    cat > /etc/init.d/vaultfleet-agent <<EOF
#!/sbin/openrc-run
name="VaultFleet Agent"
command="${INSTALL_DIR}/vaultfleet-agent"
command_args="--config ${CONFIG_DIR}/agent.yaml"
command_background=true
pidfile="/run/vaultfleet-agent.pid"
depend() { need net; }
EOF
    chmod 0755 /etc/init.d/vaultfleet-agent
    rc-update add vaultfleet-agent default
    rc-service vaultfleet-agent restart
else
    echo "==> No supported init system found; starting agent with nohup..."
    nohup "$INSTALL_DIR/vaultfleet-agent" --config "$CONFIG_DIR/agent.yaml" >/var/log/vaultfleet-agent.log 2>&1 &
fi

echo "==> VaultFleet Agent installed and running!"
echo "    Config: ${CONFIG_DIR}/agent.yaml"
