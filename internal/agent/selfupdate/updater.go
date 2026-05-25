package selfupdate

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

type Updater struct {
	config      Config
	mu          sync.Mutex
	httpClient  *http.Client
	execCommand func(name string, args ...string) *exec.Cmd
	restart     func() error
}

type Config struct {
	CurrentVersion string
	BinaryPath     string
	GitHubRepo     string
	GitHubProxy    string
	Arch           string
}

func NewUpdater(config Config) *Updater {
	if config.Arch == "" {
		config.Arch = runtime.GOARCH
	}
	return &Updater{
		config:      config,
		httpClient:  &http.Client{Timeout: 5 * time.Minute},
		execCommand: exec.Command,
		restart:     func() error { return exec.Command("systemctl", "restart", "vaultfleet-agent").Run() },
	}
}

func (u *Updater) Update(targetVersion, githubRepo string) error {
	if targetVersion == u.config.CurrentVersion {
		return nil
	}
	if !u.mu.TryLock() {
		log.Printf("self-update already in progress, skipping")
		return nil
	}
	defer u.mu.Unlock()

	repo := githubRepo
	if repo == "" {
		repo = u.config.GitHubRepo
	}

	downloadURL := u.buildDownloadURL(repo, targetVersion)
	log.Printf("self-update: downloading %s from %s", targetVersion, downloadURL)

	tmpPath, err := u.download(downloadURL)
	if err != nil {
		return fmt.Errorf("download %s: %w", targetVersion, err)
	}
	defer os.Remove(tmpPath)

	if err := u.verify(tmpPath); err != nil {
		return fmt.Errorf("verify %s: %w", targetVersion, err)
	}

	if err := os.Rename(tmpPath, u.config.BinaryPath); err != nil {
		return fmt.Errorf("replace binary: %w", err)
	}
	log.Printf("self-update: replaced binary with %s", targetVersion)

	log.Printf("self-update: restarting service")
	if err := u.restart(); err != nil {
		log.Printf("self-update: restart failed (binary already replaced): %v", err)
		return fmt.Errorf("restart: %w", err)
	}
	return nil
}

func (u *Updater) buildDownloadURL(repo, version string) string {
	assetName := fmt.Sprintf("vaultfleet-agent-linux-%s", u.config.Arch)
	rawURL := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, version, assetName)
	if u.config.GitHubProxy != "" {
		return fmt.Sprintf("%s/%s", u.config.GitHubProxy, rawURL)
	}
	return rawURL
}

func (u *Updater) download(url string) (string, error) {
	resp, err := u.httpClient.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	dir := filepath.Dir(u.config.BinaryPath)
	tmpFile, err := os.CreateTemp(dir, ".vaultfleet-update-*")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("write temp file: %w", err)
	}
	tmpFile.Close()

	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("chmod: %w", err)
	}
	return tmpPath, nil
}

func (u *Updater) verify(binaryPath string) error {
	cmd := u.execCommand(binaryPath, "--help")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("binary validation failed: %w", err)
	}
	return nil
}
