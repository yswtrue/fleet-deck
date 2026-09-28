{
  pkgs,
  ...
}:

let
  # ---------------------------------------------------------------------------
  # FleetDeck local development topology
  # ---------------------------------------------------------------------------
  pgPort = 55432; # non-default port so we never fight a system postgres
  dbName = "fleetdeck";
  dbUser = "fleetdeck";
  dbPass = "fleetdeck";

  databaseUrl = "postgres://${dbUser}:${dbPass}@127.0.0.1:${toString pgPort}/${dbName}?sslmode=disable";

  hubAddr = "127.0.0.1:7710"; # HTTP API + WebSocket (web UI + node connections)
  mcpAddr = "127.0.0.1:7717"; # MCP server: fleetdeck_* agent status tools
  webPort = 7711; # vite dev server

  # `devenv up` processes must tolerate phases whose code does not exist yet,
  # otherwise they restart-loop while we are still in an earlier phase.
  # See docs/IMPLEMENTATION.md for the phase plan.
  guarded = path: cmd: ''
    if [ -e "${path}" ]; then
      exec ${cmd}
    else
      echo "fleetdeck: ${path} not implemented yet — see docs/IMPLEMENTATION.md"
      exec sleep infinity
    fi
  '';
in
{
  # ---------------------------------------------------------------------------
  # Packages
  # ---------------------------------------------------------------------------
  packages = with pkgs; [
    # general
    git
    jq
    curl
    ripgrep
    fd
    watchexec
    just

    # database
    postgresql # psql, pg_dump, pg_restore
    pgcli # interactive shell with completion
    goose # migration authoring (runtime uses the goose Go library)

    # go toolchain helpers
    golangci-lint
    gotools # goimports, stringer, ...
    delve
    air # hot reload

    # schema / templating
    sqlc

    # web
    nodejs_22
    pnpm

    # nix / formatting
    nixfmt-rfc-style
    treefmt
    prettier
    shellcheck
  ];

  # ---------------------------------------------------------------------------
  # Languages
  # ---------------------------------------------------------------------------
  languages.go = {
    enable = true;
    version = "1.27.1";
  };

  languages.javascript = {
    enable = true;
    pnpm.enable = true;
  };

  # ---------------------------------------------------------------------------
  # Services
  # ---------------------------------------------------------------------------
  services.postgres = {
    enable = true;
    port = pgPort;
    listen_addresses = "127.0.0.1";
    initialDatabases = [
      {
        name = dbName;
        user = dbUser;
        pass = dbPass;
      }
    ];
    # gen_random_uuid() is built in since PG13. Fuzzy search is deferred until
    # Task titles actually need it; see docs/IMPLEMENTATION.md (deferred list).
  };

  # ---------------------------------------------------------------------------
  # Environment
  # ---------------------------------------------------------------------------
  env = {
    FLEETDECK_DATABASE_URL = databaseUrl;
    FLEETDECK_HUB_ADDR = hubAddr;
    FLEETDECK_MCP_ADDR = mcpAddr;
    FLEETDECK_WEB_PORT = toString webPort;

    # Node-side secrets / buffers. Kept out of the repo.
    FLEETDECK_NODE_TOKEN_FILE = "./.devenv/state/fleetdeck/node-token";
    FLEETDECK_EVENT_BUFFER_DIR = "./.devenv/state/fleetdeck/events";

    # Pi adapter: path to the bundled bridge extension used by transport/rpc.
    FLEETDECK_PI_BRIDGE = "./integrations/pi/fleetdeck-bridge.ts";

    # Generated protocol artifacts consumed by web/ and the Pi bridge.
    FLEETDECK_SCHEMA_OUT = "./web/src/gen/protocol";

    GODEBUG = "http2client=0";
  };

  # ---------------------------------------------------------------------------
  # Processes (`devenv up`)
  # ---------------------------------------------------------------------------
  processes = {
    hub = {
      exec = guarded "cmd/hub" "go run ./cmd/hub";
      process-compose = {
        availability.restart = "on_failure";
        depends_on.postgres.condition = "process_healthy";
      };
    };

    node = {
      exec = guarded "cmd/node" "go run ./cmd/node --config ./examples/node.config.yaml";
      process-compose = {
        availability.restart = "on_failure";
        depends_on.hub.condition = "process_started";
      };
    };

    web = {
      exec = guarded "web/package.json" "pnpm --dir web dev --host 127.0.0.1 --port ${toString webPort}";
      process-compose = {
        availability.restart = "on_failure";
      };
    };

    # A second Node wired to the Fake Adapter. Useful for integration work and
    # for driving the UI without touching a real agent. Not started by default:
    # run `devenv up fake-node` when you want it.
    fake-node = {
      exec = guarded "cmd/node" "go run ./cmd/node --config ./examples/fake-node.config.yaml --adapter fake";
      process-compose = {
        availability.restart = "on_failure";
        is_disabled = true;
      };
    };
  };

  # ---------------------------------------------------------------------------
  # Scripts (available inside the shell)
  # ---------------------------------------------------------------------------
  scripts = {
    # --- database -----------------------------------------------------------
    db-up.exec = "pg_isready -h 127.0.0.1 -p ${toString pgPort} -t 5";
    db-url.exec = "echo \"$FLEETDECK_DATABASE_URL\"";
    db-shell.exec = "pgcli \"$FLEETDECK_DATABASE_URL\"";

    migrate.exec = ''
      cd "$DEVENV_ROOT"
      goose -dir ./migrations postgres "$FLEETDECK_DATABASE_URL" up
    '';

    migrate-status.exec = ''
      cd "$DEVENV_ROOT"
      goose -dir ./migrations postgres "$FLEETDECK_DATABASE_URL" status
    '';

    migrate-create.exec = ''
      cd "$DEVENV_ROOT"
      if [ -z "$1" ]; then echo "usage: migrate-create <name>"; exit 1; fi
      goose -dir ./migrations create "$1" sql
    '';

    db-reset.exec = ''
      cd "$DEVENV_ROOT"
      echo "dropping and recreating ${dbName}"
      dropdb --if-exists -h 127.0.0.1 -p ${toString pgPort} -U ${dbUser} ${dbName}
      createdb -h 127.0.0.1 -p ${toString pgPort} -U ${dbUser} ${dbName}
      migrate up
    '';

    # --- go -----------------------------------------------------------------
    test.exec = "cd \"$DEVENV_ROOT\" && go test -race ./...";

    test-short.exec = "cd \"$DEVENV_ROOT\" && go test -short ./...";

    lint.exec = "cd \"$DEVENV_ROOT\" && golangci-lint run ./...";

    fmt.exec = "cd \"$DEVENV_ROOT\" && gofmt -s -w . && goimports -w .";

    vet.exec = "cd \"$DEVENV_ROOT\" && go vet ./...";

    check.exec = ''
      cd "$DEVENV_ROOT"
      set -e
      gofmt -l . | tee /tmp/fleetdeck-gofmt.out
      test ! -s /tmp/fleetdeck-gofmt.out || { echo "unformatted files above"; exit 1; }
      go vet ./...
      golangci-lint run ./...
      go test -race ./...
    '';

    # --- protocol codegen ---------------------------------------------------
    # Go structs in internal/protocol are the single source of truth.
    # This exports JSON Schema, then web/ generates TypeScript from it.
    gen-schema.exec = ''
      cd "$DEVENV_ROOT"
      go run ./cmd/fleetctl schema export --out "$FLEETDECK_SCHEMA_OUT"
    '';

    gen-web-types.exec = ''
      cd "$DEVENV_ROOT/web"
      pnpm run gen:types
    '';

    gen.exec = "gen-schema && gen-web-types";

    # --- dev helpers --------------------------------------------------------
    hub-dev.exec = "cd \"$DEVENV_ROOT\" && air --build.cmd 'go build -o .devenv/state/bin/hub ./cmd/hub' --build.bin .devenv/state/bin/hub";

    # Ask the local hub for its health.
    hub-health.exec = "curl -fsS http://${hubAddr}/api/health | jq .";

    # Show which agent adapters this machine can actually drive.
    probe-adapters.exec = ''
      cd "$DEVENV_ROOT"
      command -v pi   >/dev/null && echo "pi:    $(pi --version 2>/dev/null || echo present)" || echo "pi:    MISSING (transport/rpc unavailable)"
      command -v npx  >/dev/null && echo "npx:   present (ACP adapters via npx)"                || echo "npx:   MISSING (transport/acp unavailable)"
      command -v codex >/dev/null && echo "codex: present" || echo "codex: MISSING"
    '';
  };

  # ---------------------------------------------------------------------------
  # Tasks
  # ---------------------------------------------------------------------------
  tasks = {
    # Create the state directories the node uses for its token and event buffer.
    "fleetdeck:state-dirs".exec = ''
      mkdir -p .devenv/state/fleetdeck/events
    '';

    # Bounded check so `devenv shell` never hangs when postgres is not running.
    # Migrations are applied explicitly, not implicitly on every shell entry.
    "fleetdeck:migrate".exec = ''
      if [ -d "$DEVENV_ROOT/migrations" ] && pg_isready -h 127.0.0.1 -p ${toString pgPort} -q; then
        goose -dir "$DEVENV_ROOT/migrations" postgres "$FLEETDECK_DATABASE_URL" up || \
          echo "fleetdeck: migrations failed - run 'db-reset' or 'migrate-status'"
      fi
    '';

    "fleetdeck:web-deps".exec = ''
      if [ -f "$DEVENV_ROOT/web/package.json" ] && [ ! -d "$DEVENV_ROOT/web/node_modules" ]; then
        pnpm --dir "$DEVENV_ROOT/web" install
      fi
    '';

    "devenv:enterShell".after = [
      "fleetdeck:state-dirs"
      "fleetdeck:migrate"
      "fleetdeck:web-deps"
    ];
  };

  # ---------------------------------------------------------------------------
  # Git hooks
  # ---------------------------------------------------------------------------
  git-hooks.hooks = {
    nixfmt-rfc-style.enable = true;
    treefmt.enable = true;
    shellcheck.enable = true;
  };

  # ---------------------------------------------------------------------------
  # Shell experience
  # ---------------------------------------------------------------------------
  enterShell = ''
    echo ""
    echo "  FleetDeck development shell"
    echo "  ─────────────────────────────────────────────────────────────"
    printf "  %-14s %s\n" "postgres" "127.0.0.1:${toString pgPort} / ${dbName} (user ${dbUser})"
    printf "  %-14s %s\n" "hub"      "http://${hubAddr}"
    printf "  %-14s %s\n" "mcp"      "http://${mcpAddr}"
    printf "  %-14s %s\n" "web"      "http://127.0.0.1:${toString webPort}"
    echo ""
    printf "  %-14s %s\n" "go"       "$(go version | cut -d' ' -f3)"
    printf "  %-14s %s\n" "node"     "$(node --version)"
    echo "  ─────────────────────────────────────────────────────────────"
    echo "  devenv up            start hub + node + web"
    echo "  devenv up fake-node  additionally start a Fake Adapter node"
    echo "  check                fmt + vet + lint + race tests"
    echo "  migrate / db-reset   apply or rebuild migrations"
    echo "  probe-adapters       report which agent CLIs are available"
    echo ""
  '';

  enterTest = ''
    set -e
    cd "$DEVENV_ROOT"

    echo "== go toolchain =="
    go version

    echo "== database reachable =="
    until pg_isready -h 127.0.0.1 -p ${toString pgPort} -q; do sleep 0.5; done
    psql "$FLEETDECK_DATABASE_URL" -c 'select 1' >/dev/null

    echo "== migrations apply cleanly =="
    if [ -d "$DEVENV_ROOT/migrations" ]; then
      goose -dir "$DEVENV_ROOT/migrations" postgres "$FLEETDECK_DATABASE_URL" up
    fi

    echo "== go build + vet + test =="
    go build ./...
    go vet ./...
    go test -race ./...
  '';
}
