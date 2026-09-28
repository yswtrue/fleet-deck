// Command fleetctl is the FleetDeck developer CLI. Currently it exports the
// protocol package's Go structs as JSON Schema for the web UI's TypeScript
// codegen. The Go structs in internal/protocol are the single source of truth.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/invopop/jsonschema"

	"fleetdeck/internal/protocol"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "schema":
		schemaCmd(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: fleetctl schema export [--out DIR]")
}

func schemaCmd(args []string) {
	// The flag package stops parsing at the first positional argument, so
	// `export --out DIR` used to silently ignore --out. Pull the subcommand out
	// first, then parse the remaining flags in any order. A bare `export`
	// token that is the *value* of --out stays a value.
	var flagArgs []string
	sawExport := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-out" || a == "--out" {
			flagArgs = append(flagArgs, a)
			if i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
			continue
		}
		if a == "export" {
			sawExport = true
			continue
		}
		flagArgs = append(flagArgs, a)
	}
	if !sawExport {
		usage()
		os.Exit(2)
	}

	fs := flag.NewFlagSet("schema", flag.ExitOnError)
	out := fs.String("out", "./web/src/gen/protocol", "output directory")
	_ = fs.Parse(flagArgs)
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "fleetctl: unexpected argument %q\n", fs.Arg(0))
		usage()
		os.Exit(2)
	}

	// Every type that crosses the wire or is exposed to the web UI, keyed by
	// its exported name.
	types := map[string]any{
		"Node":                      protocol.Node{},
		"Workspace":                 protocol.Workspace{},
		"Task":                      protocol.Task{},
		"Subtask":                   protocol.Subtask{},
		"Session":                   protocol.Session{},
		"Message":                   protocol.Message{},
		"Event":                     protocol.Event{},
		"NodeStatusChanged":         protocol.NodeStatusChanged{},
		"TaskCreated":               protocol.TaskCreated{},
		"TaskStatusChanged":         protocol.TaskStatusChanged{},
		"SubtaskUpdated":            protocol.SubtaskUpdated{},
		"SessionDelta":              protocol.SessionDelta{},
		"SessionMessage":            protocol.SessionMessage{},
		"SessionStateChanged":       protocol.SessionStateChanged{},
		"SessionUserInputRequested": protocol.SessionUserInputRequested{},
		"SessionUsage":              protocol.SessionUsage{},
		"TokenUsageStats":           protocol.TokenUsageStats{},
		"TokenUsageTotals":          protocol.TokenUsageTotals{},
		"TokenUsageByModel":         protocol.TokenUsageByModel{},
		"TaskStats":                 protocol.TaskStats{},
		"TaskStatsStatusCount":      protocol.TaskStatsStatusCount{},
		"RegisterRequest":           protocol.RegisterRequest{},
		"RegisterResponse":          protocol.RegisterResponse{},
		"TaskStartCommand":          protocol.TaskStartCommand{},
		"SendMessageCommand":        protocol.SendMessageCommand{},
		"CancelCommand":             protocol.CancelCommand{},
		"Todo":                      protocol.Todo{},
		"TodoUpdate":                protocol.TodoUpdate{},
	}

	r := new(jsonschema.Reflector)
	r.ExpandedStruct = true

	// Build one combined schema so cross-type $refs (e.g. TaskCreated -> Task)
	// resolve for the web codegen step.
	combined := &jsonschema.Schema{
		Type:        "object",
		Definitions: map[string]*jsonschema.Schema{},
	}
	for name, t := range types {
		combined.Definitions[name] = r.Reflect(t)
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal(err)
	}
	b, err := json.MarshalIndent(combined, "", "  ")
	if err != nil {
		fatal(err)
	}
	path := filepath.Join(*out, "protocol.schema.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("exported %d definitions to %s\n", len(types), path)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "fleetctl:", err)
	os.Exit(1)
}
