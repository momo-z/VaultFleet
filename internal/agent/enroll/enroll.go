package enroll

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"vaultfleet/pkg/protocol"

	"gopkg.in/yaml.v3"
)

const enrollHTTPTimeout = 30 * time.Second

type AgentConfig struct {
	Server     string `yaml:"server"`
	AgentID    string `yaml:"agent_id"`
	AgentToken string `yaml:"agent_token"`
}

type enrollRequest struct {
	EnrollToken string `json:"enroll_token"`
	SystemInfo  string `json:"system_info"`
}

type EnrollResponse struct {
	AgentID    string `json:"agent_id"`
	AgentToken string `json:"agent_token"`
}

type enrollResponseEnvelope struct {
	OK    bool           `json:"ok"`
	Error string         `json:"error"`
	Data  EnrollResponse `json:"data"`
}

type systemInfo struct {
	Hostname     string   `json:"hostname"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Version      string   `json:"version,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

func Enroll(serverURL, enrollToken, configPath, version string) (*AgentConfig, error) {
	body, err := json.Marshal(enrollRequest{
		EnrollToken: enrollToken,
		SystemInfo:  collectSystemInfo(version),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal enroll request: %w", err)
	}

	endpoint, err := enrollURL(serverURL)
	if err != nil {
		return nil, err
	}

	resp, err := enrollHTTPClient().Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("enroll request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("enroll failed (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var envelope enrollResponseEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode enroll response: %w", err)
	}
	if !envelope.OK {
		if envelope.Error != "" {
			return nil, fmt.Errorf("enroll failed: %s", envelope.Error)
		}
		return nil, errors.New("enroll failed")
	}
	if envelope.Data.AgentID == "" || envelope.Data.AgentToken == "" {
		return nil, errors.New("invalid enroll response: missing agent data")
	}

	cfg := &AgentConfig{
		Server:     serverURL,
		AgentID:    envelope.Data.AgentID,
		AgentToken: envelope.Data.AgentToken,
	}
	if err := saveConfig(cfg, configPath); err != nil {
		return nil, fmt.Errorf("save agent config: %w", err)
	}

	return cfg, nil
}

func enrollURL(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("parse server URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errors.New("server URL must include scheme and host")
	}

	basePath := strings.TrimRight(u.Path, "/")
	u.Path = path.Join(basePath, "/api/agent/enroll")
	return u.String(), nil
}

func collectSystemInfo(version string) string {
	hostname, _ := os.Hostname()
	data, err := json.Marshal(systemInfo{
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Version:  version,
		Capabilities: []string{
			protocol.CapabilitySnapshotBrowse,
			protocol.CapabilityRestoreIncludePaths,
			protocol.CapabilityPolicyPlaintextRclonePass,
		},
	})
	if err != nil {
		return fmt.Sprintf("hostname=%s os=%s arch=%s", hostname, runtime.GOOS, runtime.GOARCH)
	}
	return string(data)
}

func enrollHTTPClient() *http.Client {
	return &http.Client{Timeout: enrollHTTPTimeout}
}

func saveConfig(cfg *AgentConfig, configPath string) error {
	dir := filepath.Dir(configPath)
	if err := ensureConfigDir(dir); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	return writeConfigFile(configPath, data)
}

func ensureConfigDir(dir string) error {
	return os.MkdirAll(dir, 0700)
}

func writeConfigFile(configPath string, data []byte) error {
	dir := filepath.Dir(configPath)
	tempFile, err := os.CreateTemp(dir, ".agent.yaml.*")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	if err := tempFile.Chmod(0600); err != nil {
		_ = tempFile.Close()
		return err
	}
	if _, err := tempFile.Write(data); err != nil {
		_ = tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, configPath); err != nil {
		return err
	}
	return nil
}
