// Command node runs a FleetDeck node daemon: it connects to the hub, registers,
// and executes agent adapters the hub asks it to run.
//
// Usage:
//
//	node --config examples/fake-node.config.yaml
//	node --config examples/node.config.yaml --adapter fake
//	node --config node.config.yaml --hub-url ws://hub.example.com/ws/node \
//	     --join-token <token from the UI>
//	node config --config node.config.yaml   # choose the agents to advertise
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"fleetdeck/internal/adapters"
	_ "fleetdeck/internal/adapters/all" // registers every adapter (fake + ACP agents)
	"fleetdeck/internal/node"
)

func main() {
	if err := run(); err != nil {
		slog.Error("node exited with error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "config" {
		return runConfig(os.Args[2:])
	}

	configPath := flag.String("config", "examples/fake-node.config.yaml", "path to node YAML config")
	adapterFlag := flag.String("adapter", "", "advertise/force a single adapter type (overrides config)")
	hubURL := flag.String("hub-url", "", "hub node WebSocket endpoint (ws://host:7710/ws/node); written to the config file after the first successful registration")
	joinToken := flag.String("join-token", "", "single-use join token for the first registration (never written to disk)")
	flag.Parse()

	cfg, err := node.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	if *hubURL != "" {
		cfg.SetHubURL(*hubURL)
	}
	if *joinToken != "" {
		cfg.JoinToken = *joinToken
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if *adapterFlag != "" {
		cfg.AgentTypes = []string{*adapterFlag}
		cfg.DefaultAdapter = *adapterFlag
		cfg.Capabilities = appendUnique(cfg.Capabilities, *adapterFlag)
	}

	// One daemon per machine: a second process (any config, any hub) fails
	// immediately instead of registering a duplicate node.
	release, err := node.AcquireLock(cfg.LockFile)
	if err != nil {
		return err
	}
	defer release()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	slog.Info("node starting", "name", cfg.Name, "hub", cfg.HubURL, "agent_types", cfg.AgentTypes)
	return node.New(cfg, adapters.Default).Run(ctx)
}

func appendUnique(list []string, v string) []string {
	for _, item := range list {
		if item == v {
			return list
		}
	}
	return append(list, v)
}
