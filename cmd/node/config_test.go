package main

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "node.config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readConfig(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestParseSelection(t *testing.T) {
	cases := []struct {
		in      string
		n       int
		want    []int
		wantErr bool
	}{
		{in: "1,3", n: 5, want: []int{0, 2}},
		{in: "1 3", n: 5, want: []int{0, 2}},
		{in: "3,1,3", n: 5, want: []int{2, 0}},
		{in: "all", n: 3, want: []int{0, 1, 2}},
		{in: "NONE", n: 3, want: nil},
		{in: "0", n: 3, wantErr: true},
		{in: "4", n: 3, wantErr: true},
		{in: "fake", n: 3, wantErr: true},
	}
	for _, tc := range cases {
		got, err := parseSelection(tc.in, tc.n)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("parseSelection(%q) = %v, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseSelection(%q): %v", tc.in, err)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("parseSelection(%q) = %v, want %v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("parseSelection(%q) = %v, want %v", tc.in, got, tc.want)
			}
		}
	}
}

// Every registered adapter is selectable by name; the built-in fake one proves
// the registry wiring without depending on installed CLIs.
func TestSelectByName(t *testing.T) {
	choices := detectAgents([]string{"fake", "opencode"})
	selected, err := selectByName(choices, "opencode,fake,opencode")
	if err != nil {
		t.Fatalf("selectByName: %v", err)
	}
	if got := types(selected); strings.Join(got, ",") != "opencode,fake" {
		t.Fatalf("selected = %v, want [opencode fake]", got)
	}
	if _, err := selectByName(choices, "nope"); err == nil {
		t.Fatal("expected an error for an unknown agent")
	}
	all, err := selectByName(choices, "all")
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %v, %v", types(all), err)
	}
	none, err := selectByName(choices, "none")
	if err != nil || len(none) != 0 {
		t.Fatalf("none = %v, %v", types(none), err)
	}
}

func TestConfigureNonInteractiveRebindsAgents(t *testing.T) {
	path := writeConfig(t, `# my node
name: my-machine
tags:
  - custom-label
agent_types:
  - fake
default_adapter: fake
token_file: ./node-token
`)
	var out bytes.Buffer
	err := configure(bufio.NewReader(strings.NewReader("")), &out, path, "fake,opencode", "opencode", false)
	if err != nil {
		t.Fatalf("configure: %v\n%s", err, out.String())
	}

	text := readConfig(t, path)
	for _, want := range []string{"# my node", "name: my-machine", "custom-label", "token_file: ./node-token"} {
		if !strings.Contains(text, want) {
			t.Fatalf("config lost %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "agent_types:\n  - fake\n  - opencode\n") {
		t.Fatalf("agent_types not rewritten:\n%s", text)
	}
	if !strings.Contains(text, "default_adapter: opencode") {
		t.Fatalf("default_adapter not rewritten:\n%s", text)
	}
	if !strings.Contains(out.String(), "Wrote agent_types=[fake opencode]") {
		t.Fatalf("unexpected output:\n%s", out.String())
	}
}

func TestConfigureRejectsAgentOutsideSelection(t *testing.T) {
	path := writeConfig(t, "name: my-machine\nagent_types: [fake]\n")
	err := configure(bufio.NewReader(strings.NewReader("")), &bytes.Buffer{}, path, "fake", "opencode", false)
	if err == nil || !strings.Contains(err.Error(), "not among the selected agents") {
		t.Fatalf("err = %v, want a default-outside-selection error", err)
	}
	if text := readConfig(t, path); !strings.Contains(text, "agent_types: [fake]") {
		t.Fatalf("config modified despite the error:\n%s", text)
	}
}

// Enter keeps the current binding and the current default, so re-running the
// wizard to inspect the choices is a no-op write.
func TestConfigureInteractiveEnterKeepsCurrent(t *testing.T) {
	path := writeConfig(t, "name: my-machine\nagent_types:\n  - fake\ndefault_adapter: fake\n")
	var out bytes.Buffer
	if err := configure(bufio.NewReader(strings.NewReader("\n")), &out, path, "", "", true); err != nil {
		t.Fatalf("configure: %v\n%s", err, out.String())
	}
	text := readConfig(t, path)
	if !strings.Contains(text, "agent_types:\n  - fake\n") || !strings.Contains(text, "default_adapter: fake") {
		t.Fatalf("binding changed on Enter:\n%s", text)
	}
	if !strings.Contains(out.String(), "Agent types this node can run:") {
		t.Fatalf("wizard did not list the choices:\n%s", out.String())
	}
}

// Selecting every agent then accepting the default prompt binds the whole
// registry, which is how a newly installed CLI gets added.
func TestConfigureInteractiveSelectAll(t *testing.T) {
	path := writeConfig(t, "name: my-machine\nagent_types:\n  - fake\ndefault_adapter: fake\n")
	var out bytes.Buffer
	if err := configure(bufio.NewReader(strings.NewReader("all\n\n")), &out, path, "", "", true); err != nil {
		t.Fatalf("configure: %v\n%s", err, out.String())
	}
	text := readConfig(t, path)
	for _, agent := range []string{"fake", "opencode", "codex", "claude", "pi", "omp"} {
		if !strings.Contains(text, "  - "+agent+"\n") {
			t.Fatalf("%s missing from the binding:\n%s", agent, text)
		}
	}
	if !strings.Contains(text, "default_adapter: fake") {
		t.Fatalf("current default should survive:\n%s", text)
	}
}

// `node config` without --config creates the XDG file and binds into it.
func TestRunConfigCreatesXDGConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))

	if err := runConfig([]string{"--agents", "fake"}); err != nil {
		t.Fatalf("runConfig: %v", err)
	}

	path := filepath.Join(dir, "config", "fleetdeck", "node.yaml")
	text := readConfig(t, path)
	if !strings.Contains(text, "agent_types:\n  - fake\n") {
		t.Fatalf("binding not written:\n%s", text)
	}
	tokenFile := filepath.Join(dir, "state", "fleetdeck", "node-token")
	if !strings.Contains(text, "token_file: "+tokenFile) {
		t.Fatalf("token file is not the XDG state path:\n%s", text)
	}
}

// First start: the wizard runs before the daemon connects, so the node never
// registers with an empty agent binding.
func TestSetupAgentsRunsWizardOnFirstStart(t *testing.T) {
	path := writeConfig(t, "name: my-machine\n")
	var out bytes.Buffer
	configured, err := setupAgents(path, true, "", true, strings.NewReader("all\n\n"), &out)
	if err != nil {
		t.Fatalf("setupAgents: %v\n%s", err, out.String())
	}
	if !configured {
		t.Fatal("first start did not run the wizard")
	}
	text := readConfig(t, path)
	// Types() is sorted, so the default prompt keeps the first bound agent.
	if !strings.Contains(text, "agent_types:\n  - claude\n") ||
		!strings.Contains(text, "default_adapter: claude") {
		t.Fatalf("binding not written on first start:\n%s", text)
	}
}

// An existing config is never re-prompted: an operator may have deliberately
// bound no agents.
func TestSetupAgentsLeavesExistingConfigAlone(t *testing.T) {
	body := "name: my-machine\nagent_types: [fake]\ndefault_adapter: fake\n"
	path := writeConfig(t, body)
	configured, err := setupAgents(path, false, "", true, strings.NewReader("all\n\n"), io.Discard)
	if err != nil {
		t.Fatalf("setupAgents: %v", err)
	}
	if configured {
		t.Fatal("existing config was rewritten")
	}
	if got := readConfig(t, path); got != body {
		t.Fatalf("config changed:\n%s", got)
	}
}

// systemd / containers have no terminal: start must not block or fail.
func TestSetupAgentsDoesNotPromptWithoutTTY(t *testing.T) {
	body := "name: my-machine\n"
	path := writeConfig(t, body)
	configured, err := setupAgents(path, true, "", false, strings.NewReader(""), io.Discard)
	if err != nil {
		t.Fatalf("setupAgents: %v", err)
	}
	if configured {
		t.Fatal("non-interactive start rewrote the config")
	}
	if got := readConfig(t, path); got != body {
		t.Fatalf("config changed:\n%s", got)
	}
}

// --adapter already picks the binding, so the wizard is skipped.
func TestSetupAgentsSkipsWithAdapterFlag(t *testing.T) {
	body := "name: my-machine\n"
	path := writeConfig(t, body)
	configured, err := setupAgents(path, true, "fake", true, strings.NewReader("all\n\n"), io.Discard)
	if err != nil {
		t.Fatalf("setupAgents: %v", err)
	}
	if configured {
		t.Fatal("--adapter start still ran the wizard")
	}
	if got := readConfig(t, path); got != body {
		t.Fatalf("config changed:\n%s", got)
	}
}
