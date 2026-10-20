package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetTopLevelScalar(t *testing.T) {
	const url = "ws://new.example:7710/ws/node"
	cases := []struct {
		name    string
		in      string
		want    string
		changed bool
	}{
		{
			name:    "replaces the value and keeps a trailing comment",
			in:      "name: n1\nhub_url: ws://old/ws/node # set by hand\ntoken_file: ./t\n",
			want:    "name: n1\nhub_url: " + url + " # set by hand\ntoken_file: ./t\n",
			changed: true,
		},
		{
			name:    "appends the key when absent",
			in:      "name: n1\n",
			want:    "name: n1\nhub_url: " + url + "\n",
			changed: true,
		},
		{
			name:    "appends after a file without a trailing newline",
			in:      "name: n1",
			want:    "name: n1\nhub_url: " + url + "\n",
			changed: true,
		},
		{
			name:    "leaves a matching value alone",
			in:      "hub_url: " + url + "\n",
			want:    "hub_url: " + url + "\n",
			changed: false,
		},
		{
			name:    "ignores hub_url in comments",
			in:      "# hub_url is omitted: derived from the environment\nname: n1\n",
			want:    "# hub_url is omitted: derived from the environment\nname: n1\nhub_url: " + url + "\n",
			changed: true,
		},
		{
			name:    "does not match a longer key",
			in:      "hub_urls: [ws://other]\n",
			want:    "hub_urls: [ws://other]\nhub_url: " + url + "\n",
			changed: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := setTopLevel([]byte(tc.in), "hub_url", "hub_url: "+url)
			if string(got) != tc.want {
				t.Fatalf("setTopLevel =\n%q\nwant\n%q", got, tc.want)
			}
			if changed != tc.changed {
				t.Fatalf("changed = %v, want %v", changed, tc.changed)
			}
		})
	}
}

func TestSetAgentTypesReplacesBlock(t *testing.T) {
	in := `# my node
name: n1
capabilities:
  - custom
agent_types:
  - fake # dev only
  - opencode
default_adapter: fake
token_file: ./t
`
	want := `# my node
name: n1
capabilities:
  - custom
agent_types:
  - codex
  - fake
default_adapter: codex
token_file: ./t
`
	got, changed := setAgentTypes([]byte(in), []string{"codex", "fake"}, "codex")
	if !changed {
		t.Fatal("expected a change")
	}
	if string(got) != want {
		t.Fatalf("setAgentTypes =\n%s\nwant\n%s", got, want)
	}
}

func TestSetAgentTypesInsertsAndClears(t *testing.T) {
	got, changed := setAgentTypes([]byte("name: n1\n"), []string{"opencode"}, "opencode")
	want := "name: n1\nagent_types:\n  - opencode\ndefault_adapter: opencode\n"
	if !changed || string(got) != want {
		t.Fatalf("insert =\n%s\nwant\n%s", got, want)
	}

	got, changed = setAgentTypes([]byte("agent_types:\n  - fake\n\n# trailing note\nname: n1\n"), nil, "")
	want = "agent_types: []\n\n# trailing note\nname: n1\n"
	if !changed || string(got) != want {
		t.Fatalf("clear =\n%s\nwant\n%s", got, want)
	}
}

func TestSetAgentTypesKeepsFollowingTopLevelKeys(t *testing.T) {
	in := "agent_types: [fake]\nhub_url: ws://h/ws/node\n"
	got, _ := setAgentTypes([]byte(in), []string{"fake", "claude"}, "")
	want := "agent_types:\n  - fake\n  - claude\nhub_url: ws://h/ws/node\n"
	if string(got) != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestPersistHubURLCreatesNoStrayFilesAndKeepsMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.config.yaml")
	if err := os.WriteFile(path, []byte("name: n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := persistHubURL(path, "ws://hub:7710/ws/node"); err != nil {
		t.Fatalf("persistHubURL: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "hub_url: ws://hub:7710/ws/node") {
		t.Fatalf("hub_url not written: %q", raw)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("mode = %v, want %v", after.Mode().Perm(), before.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestPersistHubURLWithoutConfigFile(t *testing.T) {
	if err := persistHubURL("", "ws://hub:7710/ws/node"); err == nil {
		t.Fatal("want an error when the daemon has no config file to update")
	}
}

func TestPersistAgents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.config.yaml")
	if err := os.WriteFile(path, []byte("name: n1\nagent_types: [fake]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PersistAgents(path, []string{"fake", "codex"}, "fake"); err != nil {
		t.Fatalf("PersistAgents: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"agent_types:\n  - fake\n  - codex\n", "default_adapter: fake"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("config missing %q:\n%s", want, raw)
		}
	}
}
