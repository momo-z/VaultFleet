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

	"gopkg.in/yaml.v3"
)

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
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
}

func Enroll(serverURL, enrollToken, configPath string) (*AgentConfig, error) {
	body, err := json.Marshal(enrollRequest{
		EnrollToken: enrollToken,
		SystemInfo:  collectSystemInfo(),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal enroll request: %w", err)
	}

	endpoint, err := enrollURL(serverURL)
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(endpoint, "application/json", bytes.NewReader(body))
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

func collectSystemInfo() string {
	hostname, _ := os.Hostname()
	data, err := json.Marshal(systemInfo{
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
	})
	if err != nil {
		return fmt.Sprintf("hostname=%s os=%s arch=%s", hostname, runtime.GOOS, runtime.GOARCH)
	}
	return string(data)
}

func saveConfig(cfg *AgentConfig, configPath string) error {
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return err
	}
	return os.Chmod(configPath, 0600)
}
