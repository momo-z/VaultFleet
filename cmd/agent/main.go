package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"gopkg.in/yaml.v3"

	"vaultfleet/internal/agent/connect"
	enrollpkg "vaultfleet/internal/agent/enroll"
	"vaultfleet/internal/agent/policy"
	"vaultfleet/pkg/protocol"
)

const defaultConfigPath = "/etc/vaultfleet/agent.yaml"

type AgentConfig struct {
	Server     string `yaml:"server"`
	AgentID    string `yaml:"agent_id"`
	AgentToken string `yaml:"agent_token"`
}

func main() {
	configPath := flag.String("config", defaultConfigPath, "path to agent config file")
	server := flag.String("server", "", "master server URL for enrollment")
	token := flag.String("token", "", "enrollment token for first-time registration")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		if *server == "" || *token == "" {
			log.Fatalf("load config: %v", err)
		}
		cfg, err = enroll(*server, *token, *configPath)
		if err != nil {
			log.Fatalf("enrollment failed: %v", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store := policy.NewStore("")
	client := connect.NewClient(cfg.Server, cfg.AgentToken, handleMessage(store))

	go connect.RunHeartbeat(ctx, client, connect.DefaultSystemInfoCollector, 0)
	client.Run(ctx)
}

func handleMessage(store *policy.Store) connect.MessageHandler {
	return func(msg protocol.Message) {
		switch msg.Type {
		case protocol.TypePolicyPush:
			pushedPolicy, err := protocol.ParsePayload[protocol.PolicyPushPayload](&msg)
			if err != nil {
				log.Printf("parse policy push failed: %v", err)
				return
			}
			if err := store.SavePolicy(pushedPolicy); err != nil {
				log.Printf("save policy failed: %v", err)
			}
		}
	}
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
	cfg, err := enrollpkg.Enroll(server, token, configPath)
	if err != nil {
		return nil, err
	}
	return &AgentConfig{
		Server:     cfg.Server,
		AgentID:    cfg.AgentID,
		AgentToken: cfg.AgentToken,
	}, nil
}
