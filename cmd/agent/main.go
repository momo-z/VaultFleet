package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"gopkg.in/yaml.v3"

	agenthandler "vaultfleet/internal/agent"
	"vaultfleet/internal/agent/connect"
	enrollpkg "vaultfleet/internal/agent/enroll"
	"vaultfleet/internal/agent/policy"
	"vaultfleet/internal/agent/selfupdate"
	"vaultfleet/pkg/protocol"
)

const defaultConfigPath = "/etc/vaultfleet/agent.yaml"

var version string

type AgentConfig struct {
	Server      string `yaml:"server"`
	AgentID     string `yaml:"agent_id"`
	AgentToken  string `yaml:"agent_token"`
	AutoUpdate  *bool  `yaml:"auto_update,omitempty"`
	GitHubProxy string `yaml:"github_proxy,omitempty"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runAgent(ctx, os.Args[1:], defaultAgentRuntime()); err != nil {
		log.Fatal(err)
	}
}

type agentRuntime struct {
	loadConfig func(path string) (*AgentConfig, error)
	enroll     func(server, token, configPath string) (*AgentConfig, error)
	runClient  func(ctx context.Context, cfg *AgentConfig) error
}

func defaultAgentRuntime() agentRuntime {
	return agentRuntime{
		loadConfig: loadConfig,
		enroll:     enroll,
		runClient:  runClient,
	}
}

func runAgent(ctx context.Context, args []string, runtime agentRuntime) error {
	flags := flag.NewFlagSet("vaultfleet-agent", flag.ContinueOnError)
	configPath := flags.String("config", defaultConfigPath, "path to agent config file")
	server := flags.String("server", "", "master server URL for enrollment")
	token := flags.String("token", "", "enrollment token for first-time registration")
	enrollOnly := flags.Bool("enroll-only", false, "enroll agent and exit")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	if *enrollOnly {
		if *server == "" || *token == "" {
			return fmt.Errorf("--server and --token are required with --enroll-only")
		}
		_, err := runtime.enroll(*server, *token, *configPath)
		return err
	}

	cfg, err := runtime.loadConfig(*configPath)
	if err != nil {
		if *server == "" || *token == "" {
			return fmt.Errorf("load config: %w", err)
		}
		cfg, err = runtime.enroll(*server, *token, *configPath)
		if err != nil {
			return fmt.Errorf("enrollment failed: %w", err)
		}
	}

	return runtime.runClient(ctx, cfg)
}

func runClient(ctx context.Context, cfg *AgentConfig) error {
	store := policy.NewStore("")
	var updater *selfupdate.Updater
	if cfg.AutoUpdate == nil || *cfg.AutoUpdate {
		execPath, err := os.Executable()
		if err == nil {
			updater = selfupdate.NewUpdater(selfupdate.Config{
				CurrentVersion: version,
				BinaryPath:     execPath,
				GitHubRepo:     "momo-z/VaultFleet",
				GitHubProxy:    cfg.GitHubProxy,
				Arch:           runtime.GOARCH,
			})
		}
	}
	var client *connect.Client
	handler := agenthandler.NewHandler(agenthandler.HandlerConfig{
		PolicyStore: store,
		AgentID:     cfg.AgentID,
		SendFunc: func(msg protocol.Message) error {
			return client.Send(msg)
		},
		AgentVersion: version,
		Updater:      updater,
	})
	collector := func() connect.SystemInfo {
		info := connect.DefaultSystemInfoCollector()
		info.AgentVersion = version
		info.Capabilities = []string{
			protocol.CapabilitySnapshotBrowse,
			protocol.CapabilityRestoreIncludePaths,
			protocol.CapabilityPolicyPlaintextRclonePass,
		}
		return info
	}
	client = connect.NewClient(cfg.Server, cfg.AgentToken, handler.Handle)
	sendHeartbeat := func() {
		collectorInfo := collector()
		payload := protocol.HeartbeatPayload{
			AgentVersion: collectorInfo.AgentVersion,
			Capabilities: collectorInfo.Capabilities,
		}
		msg, err := protocol.NewMessage(protocol.TypeHeartbeat, payload)
		if err == nil {
			_ = client.Send(*msg)
		}
	}
	client.SetOnConnect(func() {
		sendHeartbeat()
		handler.FlushPendingResults()
	})
	go connect.RunHeartbeat(ctx, client, collector, 0)
	client.Run(ctx)
	return nil
}

func loadConfig(path string) (*AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg AgentConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func enroll(server, token, configPath string) (*AgentConfig, error) {
	cfg, err := enrollpkg.Enroll(server, token, configPath, version)
	if err != nil {
		return nil, err
	}
	return &AgentConfig{
		Server:     cfg.Server,
		AgentID:    cfg.AgentID,
		AgentToken: cfg.AgentToken,
	}, nil
}
