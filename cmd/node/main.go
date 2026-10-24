// Command node runs a FleetDeck node daemon: it connects to the hub, registers,
// and executes agent adapters the hub asks it to run.
//
// Usage:
//
//	node                                   # ~/.config/fleetdeck/node.yaml
//	node --config examples/fake-node.config.yaml
//	node config                            # choose the agents to advertise
//	node workspace add --path /srv/app     # register a workspace on this node
//	node install                           # ~/.local/bin + user service (autostart)
//	node update                            # replace this binary with the latest release
//	node --hub-url ws://hub.example.com/ws/node --join-token <token from the UI>
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
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "config":
			return runConfig(os.Args[2:])
		case "workspace":
			return runWorkspace(os.Args[2:])
		case "install":
			return runInstall(os.Args[2:])
		case "update":
			return runUpdate(os.Args[2:])
		case "version", "--version", "-version", "-v":
			return runVersion()
		}
	}

	configPath := flag.String("config", "", "path to node YAML config (default: the platform config file, created when missing)")
	adapterFlag := flag.String("adapter", "", "advertise/force a single adapter type (overrides config)")
	hubURL := flag.String("hub-url", "", "hub node WebSocket endpoint (ws://host:7710/ws/node); written to the config file after the first successful registration")
	joinToken := flag.String("join-token", "", "single-use join token for the first registration (never written to disk)")
	flag.Parse()

	path, created, err := node.ConfigPath(*configPath)
	if err != nil {
		return err
	}
	cfg, err := node.LoadConfig(path)
	if err != nil {
		return err
	}

	// Flags must survive the first-start wizard's reload of the config file.
	applyFlags := func(c *node.Config) error {
		if *hubURL != "" {
			c.SetHubURL(*hubURL)
		}
		if *joinToken != "" {
			c.JoinToken = *joinToken
		}
		return c.Validate()
	}
	if err := applyFlags(&cfg); err != nil {
		return err
	}
	if *adapterFlag != "" {
		cfg.AgentTypes = []string{*adapterFlag}
		cfg.DefaultAdapter = *adapterFlag
	}

	// One daemon per machine: a second process (any config, any hub) fails
	// immediately instead of registering a duplicate node.
	release, err := node.AcquireLock(cfg.LockFile)
	if err != nil {
		return err
	}
	defer release()

	// A freshly created config has no agents: pick them before dialing out, so
	// the node never registers with an empty binding.
	configured, err := setupAgents(path, created, *adapterFlag, isTerminal(os.Stdin), os.Stdin, os.Stdout)
	if err != nil {
		return err
	}
	if configured {
		if cfg, err = node.LoadConfig(path); err != nil {
			return err
		}
		if err := applyFlags(&cfg); err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	slog.Info("node starting", "name", cfg.Name, "hub", cfg.HubURL, "agent_types", cfg.AgentTypes)
	return node.New(cfg, adapters.Default).Run(ctx)
}
