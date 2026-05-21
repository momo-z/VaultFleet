package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"vaultfleet/internal/master/api"
	"vaultfleet/internal/master/backup"
	"vaultfleet/internal/master/commands"
	"vaultfleet/internal/master/db"
	"vaultfleet/internal/master/events"
	"vaultfleet/internal/master/notify"
	"vaultfleet/internal/master/ws"
)

type masterRuntime struct {
	hub            *ws.Hub
	bus            *events.Bus
	commandService *commands.Service
	wsHandler      *ws.Handler
	policyPusher   *api.PolicyChangedPusher
	router         http.Handler
}

type runtimeOptions struct {
	commandTimeoutScanInterval time.Duration
}

func main() {
	dataDir := flag.String("data-dir", "/data", "path to master data directory")
	addr := flag.String("addr", ":8080", "HTTP listen address")
	flag.Parse()

	log.Printf("starting VaultFleet master data-dir=%s addr=%s", *dataDir, *addr)

	restored, err := backup.CheckAndRestore(*dataDir)
	if err != nil {
		log.Fatalf("backup restore check failed: %v", err)
	}
	if restored {
		log.Printf("restored data directory from backup.zip")
	}

	database, err := db.New(*dataDir)
	if err != nil {
		log.Fatalf("database initialization failed: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runtime := buildRuntime(ctx, database)
	go ws.NewMonitor(runtime.hub, runtime.bus).Run(ctx)

	server := &http.Server{
		Addr:              *addr,
		Handler:           runtime.router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("VaultFleet master listening on %s", *addr)
		serverErr <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		log.Printf("shutdown signal received")
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server failed: %v", err)
		}
		return
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("server shutdown failed: %v", err)
	}

	if err := <-serverErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server failed during shutdown: %v", err)
	}
	log.Printf("VaultFleet master stopped")
}

func buildRuntime(ctx context.Context, database *db.Database) masterRuntime {
	return buildRuntimeWithOptions(ctx, database, runtimeOptions{
		commandTimeoutScanInterval: time.Minute,
	})
}

func buildRuntimeWithOptions(ctx context.Context, database *db.Database, options runtimeOptions) masterRuntime {
	if options.commandTimeoutScanInterval <= 0 {
		options.commandTimeoutScanInterval = time.Minute
	}

	hub := ws.NewHub()
	bus := events.NewBus()
	commandService := commands.NewService(database, hub)
	api.SubscribeAgentStateEvents(database, bus)
	notify.NewDispatcher(database, bus).Start()
	policyLookup := api.CurrentPolicyLookup(database)

	wsHandler := ws.NewHandler(
		hub,
		bus,
		api.AuthenticateAgentByToken(database),
		nil,
		api.NewTaskResultProcessor(database, commandService),
	)
	wsHandler.PolicyAckProcessor = api.NewPolicyAckProcessor(database, commandService)
	wsHandler.SnapshotListResponseProcessor = api.NewSnapshotListResponseProcessor(database, commandService)
	policyPusher := api.NewPolicyChangedPusher(database, hub, policyLookup)
	policyPusher.Commands = commandService
	wsHandler.PendingCommandDispatcher = func(agentID string) error {
		policyPusher.EnsureDurableCommand(context.Background(), agentID)
		return commandService.DispatchPendingForAgent(ctx, agentID, 20)
	}
	wsHandler.AgentStateUpdater = api.NewAgentStateUpdater(database)
	wsHandler.HeartbeatStateUpdater = api.NewHeartbeatStateUpdater(database)
	go commandService.RunTimeoutScanner(ctx, options.commandTimeoutScanInterval)
	bus.Subscribe(events.PolicyChanged, policyPusher.Handle)
	router := api.NewRouter(api.RouterConfig{
		Database:       database,
		Hub:            hub,
		CommandService: commandService,
		EventBus:       bus,
		AgentWebSocket: wsHandler.HandleWebSocket,
	})

	return masterRuntime{
		hub:            hub,
		bus:            bus,
		commandService: commandService,
		wsHandler:      wsHandler,
		policyPusher:   policyPusher,
		router:         router,
	}
}
