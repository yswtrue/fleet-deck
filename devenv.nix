{
  pkgs,
  config,
  ...
}:

let
  # ---------------------------------------------------------------------------
  # FleetDeck local development topology — sourced from the dotenv file
  # (official `dotenv` integration, see https://devenv.sh/integrations/dotenv/).
  # Ports/credentials are defined ONLY there.
  # ---------------------------------------------------------------------------
  envValue =
    key:
    config.env.${key} or (throw "${key} is missing from the dotenv file (copy .env.example to .env)");

  pgHost = envValue "FLEETDECK_DB_HOST";
  pgPort = pkgs.lib.toInt (envValue "FLEETDECK_DB_PORT");
  dbName = envValue "FLEETDECK_DB_NAME";
  dbUser = envValue "FLEETDECK_DB_USER";
  dbPass = envValue "FLEETDECK_DB_PASSWORD";

  databaseUrl = "postgres://${dbUser}:${dbPass}@${pgHost}:${toString pgPort}/${dbName}?sslmode=disable";
  redisHost = envValue "FLEETDECK_REDIS_HOST";
  redisPort = pkgs.lib.toInt (envValue "FLEETDECK_REDIS_PORT");
  redisUrl = "redis://${redisHost}:${toString redisPort}/0";

  hubHost = envValue "FLEETDECK_HUB_HOST";
  hubPort = pkgs.lib.toInt (envValue "FLEETDECK_HUB_PORT");
  hubAddr = "${hubHost}:${toString hubPort}"; # HTTP + WebSocket (web UI + node connections)
  webHost = envValue "FLEETDECK_WEB_HOST";
  webPort = envValue "FLEETDECK_WEB_PORT"; # vite dev server
in
{
  # Loads .env into config.env (and the shell/process environment). Falls back
  # to .env.example when .env is absent. Note the dotenv file lands in the nix
  # store — fine for these local dev credentials, see the dotenv docs for the
  # SecretSpec alternative.
  dotenv = {
    enable = true;
    filename = if builtins.pathExists ./.env then ".env" else ".env.example";
  };

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
    listen_addresses = pgHost;
    initialDatabases = [
      {
        name = dbName;
        user = dbUser;
        pass = dbPass;
      }
    ];
    # No extensions are declared here on purpose: migration 00008 creates
    # pg_trgm itself (CREATE EXTENSION IF NOT EXISTS), and gen_random_uuid()
    # is built in since PG13.
  };

  # Hub read-through cache (auth tokens, task lists). Cache-only: no
  # persistence, data may vanish between runs without consequence.
  services.redis = {
    enable = true;
    port = redisPort;
    bind = redisHost;
  };

  # ---------------------------------------------------------------------------
  # Environment
  # ---------------------------------------------------------------------------
  env = {
    # Derived connection strings the binaries actually read. The raw .env keys
    # are exported by the dotenv integration itself.
    FLEETDECK_DATABASE_URL = databaseUrl;
    FLEETDECK_REDIS_URL = redisUrl;
    FLEETDECK_HUB_ADDR = hubAddr;

    # Node-side secrets / buffers. Kept out of the repo.
    FLEETDECK_NODE_TOKEN_FILE = "./.devenv/state/fleetdeck/node-token";
    # Dev-only pairing secret so freshly-created local nodes can register
    # without minting a token in the UI. Production deployments leave this
    # unset and use Nodes -> Add node instead.
    FLEETDECK_NODE_JOIN_TOKEN = "dev-join-token";
    FLEETDECK_EVENT_BUFFER_DIR = "./.devenv/state/fleetdeck/events";

    # Generated protocol artifacts consumed by web/.
    FLEETDECK_SCHEMA_OUT = "./web/src/gen/protocol";

    GODEBUG = "http2client=0";
  };

  # ---------------------------------------------------------------------------
  # Processes (`devenv up`)
  # ---------------------------------------------------------------------------
  processes = {
    # The native process manager (devenv 2.x default) reads `after`, `ready`,
    # `restart` and `start.enable`; the process-compose sub-options are ignored
    # unless `process.manager.implementation = "process-compose"`.
    hub = {
      exec = "go run ./cmd/hub";
      after = [ "devenv:processes:postgres" ];
      ready = {
        period = 1;
        timeout = 60;
        http.get = {
          host = hubHost;
          port = hubPort;
          path = "/api/health";
        };
      };
    };

    node = {
      # The dev pairing secret is passed as a flag: the daemon takes the join
      # token from --join-token, never from the environment.
      exec = "go run ./cmd/node --config ./examples/node.config.yaml --join-token \"$FLEETDECK_NODE_JOIN_TOKEN\"";
      after = [ "devenv:processes:hub" ];
    };

    web = {
      exec = "pnpm --dir web dev --host ${webHost} --port ${toString webPort}";
    };

    # A second Node wired to the fake adapter. Useful for integration work and
    # for driving the UI without touching a real agent. Not started by default —
    # run `devenv up hub node web fake-node` to get the whole stack plus it.
    fake-node = {
      exec = "go run ./cmd/node --config ./examples/fake-node.config.yaml --adapter fake --join-token \"$FLEETDECK_NODE_JOIN_TOKEN\"";
      after = [ "devenv:processes:hub" ];
      start.enable = false;
    };
  };

  # ---------------------------------------------------------------------------
  # Scripts (available inside the shell)
  # ---------------------------------------------------------------------------
  scripts = {
    # --- database -----------------------------------------------------------
    db-up.exec = "pg_isready -h ${pgHost} -p ${toString pgPort} -t 5";
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
      dropdb --if-exists -h ${pgHost} -p ${toString pgPort} -U ${dbUser} ${dbName}
      createdb -h ${pgHost} -p ${toString pgPort} -U ${dbUser} ${dbName}
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
      if [ -d "$DEVENV_ROOT/migrations" ] && pg_isready -h ${pgHost} -p ${toString pgPort} -q; then
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
    printf "  %-14s %s\n" "postgres" "${pgHost}:${toString pgPort} / ${dbName} (user ${dbUser})"
    printf "  %-14s %s\n" "hub"      "http://${hubAddr}"
    printf "  %-14s %s\n" "web"      "http://${webHost}:${toString webPort}"
    echo ""
    printf "  %-14s %s\n" "go"       "$(go version | cut -d' ' -f3)"
    printf "  %-14s %s\n" "node"     "$(node --version)"
    echo "  ─────────────────────────────────────────────────────────────"
    echo "  devenv up            start hub + node + web"
    echo "  devenv up hub node web fake-node   full stack + a Fake Adapter node"
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
    until pg_isready -h ${pgHost} -p ${toString pgPort} -q; do sleep 0.5; done
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
