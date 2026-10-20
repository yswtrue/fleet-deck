package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"fleetdeck/internal/adapters"
	"fleetdeck/internal/adapters/acp"
	"fleetdeck/internal/node"
)

// agentChoice is one agent type the node can advertise, plus whether its
// launcher is actually installed on this machine.
type agentChoice struct {
	Type    string
	Missing string // first launcher executable that is not on PATH
	Builtin bool   // runs in-process, no external launcher (fake)
}

// detectAgents probes the launchers of every registered agent type.
func detectAgents(types []string) []agentChoice {
	choices := make([]agentChoice, 0, len(types))
	for _, t := range types {
		choice := agentChoice{Type: t}
		required := acp.Requirements(t)
		if len(required) == 0 {
			// No external launcher (fake, or an in-process adapter).
			choice.Builtin = true
			choices = append(choices, choice)
			continue
		}
		for _, bin := range required {
			if _, err := exec.LookPath(bin); err != nil {
				choice.Missing = bin
				break
			}
		}
		choices = append(choices, choice)
	}
	return choices
}

// runConfig implements `node config`: bind the agent types this node
// advertises, interactively by default. Re-running it after installing a new
// agent CLI is the documented way to add that agent.
func runConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	configPath := fs.String("config", "", "node config file (default: the platform config file, created when missing)")
	agentsFlag := fs.String("agents", "", "comma-separated agent types to bind (skips the prompt)")
	defaultFlag := fs.String("default", "", "default adapter (must be one of --agents)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path, created, err := node.ConfigPath(*configPath)
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(os.Stdout, "Created %s\n", path)
	}
	interactive := *agentsFlag == ""
	if interactive && !isTerminal(os.Stdin) {
		return errors.New("config: stdin is not a terminal; pass --agents opencode,codex (and optionally --default)")
	}
	return configure(bufio.NewReader(os.Stdin), os.Stdout, path, *agentsFlag, *defaultFlag, interactive)
}

// configure is the testable wizard body: it reads prompts from in and reports
// on out.
func configure(in *bufio.Reader, out io.Writer, path, agentsFlag, defaultFlag string, interactive bool) error {
	cfg, err := node.LoadConfig(path)
	if err != nil {
		return fmt.Errorf("%w (start from deploy/node.config.example.yaml)", err)
	}
	choices := detectAgents(adapters.Default.Types())

	selected, err := chooseAgents(in, out, choices, cfg.AgentTypes, agentsFlag, interactive)
	if err != nil {
		return err
	}
	defaultAdapter, err := chooseDefault(in, out, selected, cfg.DefaultAdapter, defaultFlag, interactive)
	if err != nil {
		return err
	}

	names := types(selected)
	if err := node.PersistAgents(path, names, defaultAdapter); err != nil {
		return err
	}

	fmt.Fprintf(out, "Wrote agent_types=%v to %s", names, path)
	if defaultAdapter != "" {
		fmt.Fprintf(out, " (default_adapter=%s)", defaultAdapter)
	}
	fmt.Fprintln(out)
	for _, choice := range selected {
		if choice.Missing != "" {
			fmt.Fprintf(out, "warning: %s needs %q on PATH; tasks for it will fail until the CLI is installed\n", choice.Type, choice.Missing)
		}
	}
	// The path may contain spaces (macOS Application Support), so quote it in
	// the copy-ready command.
	fmt.Fprintf(out, "Restart the node to advertise them: fleetdeck-node --config \"%s\"\n", path)
	return nil
}

// chooseAgents resolves the binding: --agents when given, otherwise a prompt
// that keeps the current selection on Enter.
func chooseAgents(in *bufio.Reader, out io.Writer, choices []agentChoice, current []string, agentsFlag string, interactive bool) ([]agentChoice, error) {
	if agentsFlag != "" {
		return selectByName(choices, agentsFlag)
	}
	if !interactive {
		return nil, errors.New("config: no agent selection")
	}

	fmt.Fprintln(out, "Agent types this node can run:")
	for i, choice := range choices {
		fmt.Fprintf(out, "  %d) %-10s %s\n", i+1, choice.Type, availability(choice))
	}
	fmt.Fprintf(out, "Current binding: agent_types=%v", current)
	if len(current) == 0 {
		fmt.Fprint(out, " (none)")
	}
	fmt.Fprintln(out)
	fmt.Fprint(out, `Select agents by number ("all", "none"; Enter keeps the current binding): `)

	line, err := readLine(in)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(line) == "" {
		selected := make([]agentChoice, 0, len(current))
		for _, t := range current {
			for _, choice := range choices {
				if choice.Type == t {
					selected = append(selected, choice)
					break
				}
			}
		}
		return selected, nil
	}
	indices, err := parseSelection(line, len(choices))
	if err != nil {
		return nil, err
	}
	selected := make([]agentChoice, 0, len(indices))
	for _, i := range indices {
		selected = append(selected, choices[i])
	}
	return selected, nil
}

// chooseDefault resolves default_adapter: the flag wins, otherwise the current
// default survives when still bound, and more than one selection asks.
func chooseDefault(in *bufio.Reader, out io.Writer, selected []agentChoice, current, defaultFlag string, interactive bool) (string, error) {
	bound := func(name string) bool {
		for _, choice := range selected {
			if choice.Type == name {
				return true
			}
		}
		return false
	}
	if defaultFlag != "" {
		if !bound(defaultFlag) {
			return "", fmt.Errorf("config: --default %s is not among the selected agents %v", defaultFlag, types(selected))
		}
		return defaultFlag, nil
	}
	if len(selected) == 0 {
		return "", nil
	}
	if len(selected) == 1 {
		return selected[0].Type, nil
	}
	pick := selected[0].Type
	if bound(current) {
		pick = current
	}
	if !interactive {
		return pick, nil
	}
	fmt.Fprintf(out, "Default adapter [%s]: ", pick)
	line, err := readLine(in)
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return pick, nil
	}
	if !bound(line) {
		return "", fmt.Errorf("config: %s is not among the selected agents %v", line, types(selected))
	}
	return line, nil
}

// selectByName resolves a comma/space separated agent list (or "all"/"none").
func selectByName(choices []agentChoice, list string) ([]agentChoice, error) {
	fields := splitList(list)
	if len(fields) == 0 {
		return nil, errors.New("config: empty --agents")
	}
	if len(fields) == 1 && strings.EqualFold(fields[0], "all") {
		return choices, nil
	}
	if len(fields) == 1 && strings.EqualFold(fields[0], "none") {
		return nil, nil
	}
	selected := make([]agentChoice, 0, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, name := range fields {
		found := false
		for _, choice := range choices {
			if choice.Type != name {
				continue
			}
			found = true
			if !seen[name] {
				seen[name] = true
				selected = append(selected, choice)
			}
			break
		}
		if !found {
			return nil, fmt.Errorf("config: unknown agent %q (known: %s)", name, strings.Join(types(choices), ", "))
		}
	}
	return selected, nil
}

// parseSelection parses "1,3" / "1 3" / "all" / "none" into 0-based indices.
func parseSelection(input string, n int) ([]int, error) {
	fields := splitList(input)
	if len(fields) == 1 && strings.EqualFold(fields[0], "all") {
		all := make([]int, n)
		for i := range all {
			all[i] = i
		}
		return all, nil
	}
	if len(fields) == 1 && strings.EqualFold(fields[0], "none") {
		return nil, nil
	}
	seen := make(map[int]bool, len(fields))
	indices := make([]int, 0, len(fields))
	for _, field := range fields {
		num, err := strconv.Atoi(field)
		if err != nil || num < 1 || num > n {
			return nil, fmt.Errorf("config: %q is not a number between 1 and %d", field, n)
		}
		if seen[num-1] {
			continue
		}
		seen[num-1] = true
		indices = append(indices, num-1)
	}
	return indices, nil
}

func splitList(input string) []string {
	return strings.FieldsFunc(input, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
}

func types(choices []agentChoice) []string {
	out := make([]string, len(choices))
	for i, choice := range choices {
		out[i] = choice.Type
	}
	return out
}

func availability(choice agentChoice) string {
	switch {
	case choice.Builtin:
		return "built-in"
	case choice.Missing != "":
		return "missing: " + choice.Missing
	default:
		return "ready"
	}
}

func readLine(in *bufio.Reader) (string, error) {
	line, err := in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return line, nil
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
