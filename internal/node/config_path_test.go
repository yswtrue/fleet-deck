package node

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// platformConfigDir is what DefaultConfigPath must fall back to when no XDG
// variable is set.
func platformConfigDir(t *testing.T) string {
	t.Helper()
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Skip("platform reports no config directory")
	}
	return dir
}

func TestDefaultConfigPathHonorsXDGOnEveryOS(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if got, want := DefaultConfigPath(), filepath.Join(dir, "fleetdeck", "node.yaml"); got != want {
		t.Fatalf("DefaultConfigPath = %q, want %q", got, want)
	}
}

func TestDefaultConfigPathPlatformDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	want := filepath.Join(platformConfigDir(t), "fleetdeck", "node.yaml")
	if got := DefaultConfigPath(); got != want {
		t.Fatalf("DefaultConfigPath = %q, want %q", got, want)
	}
	// The XDG spec only honors absolute values; a relative one is ignored.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join("relative", "dir"))
	if got := DefaultConfigPath(); got != want {
		t.Fatalf("relative XDG_CONFIG_HOME was used: %q", got)
	}
}

func TestDefaultTokenFileHonorsXDGOnEveryOS(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	if got, want := DefaultTokenFile(), filepath.Join(dir, "fleetdeck", "node-token"); got != want {
		t.Fatalf("DefaultTokenFile = %q, want %q", got, want)
	}
}

func TestDefaultTokenFilePlatformDefault(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	var want string
	switch runtime.GOOS {
	case "windows":
		dir, err := os.UserCacheDir()
		if err != nil {
			t.Skip("no local app data directory")
		}
		want = filepath.Join(dir, "fleetdeck", "node-token")
	case "darwin":
		want = filepath.Join(platformConfigDir(t), "fleetdeck", "node-token")
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("no home directory")
		}
		want = filepath.Join(home, ".local", "state", "fleetdeck", "node-token")
	}
	if got := DefaultTokenFile(); got != want {
		t.Fatalf("DefaultTokenFile = %q, want %q", got, want)
	}
}

func TestConfigPathCreatesDefaultTemplate(t *testing.T) {
	dir := t.TempDir()
	configHome := filepath.Join(dir, "config")
	stateHome := filepath.Join(dir, "state")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)

	path, created, err := ConfigPath("")
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if !created {
		t.Fatal("created = false, want the template to be written")
	}
	if want := filepath.Join(configHome, "fleetdeck", "node.yaml"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(stateHome, "fleetdeck", "node-token")
	if !strings.Contains(string(raw), "token_file: "+tokenFile) {
		t.Fatalf("template missing the token file:\n%s", raw)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %v, want 0600", perm)
	}

	// The generated file must parse, carry this machine's name, and use the
	// resolved token path instead of the repo-relative dev default.
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig on the template: %v", err)
	}
	if cfg.Name == "" {
		t.Fatal("template has no node name")
	}
	if cfg.TokenFile != tokenFile {
		t.Fatalf("TokenFile = %q, want %q", cfg.TokenFile, tokenFile)
	}

	// Second resolution reuses the file instead of overwriting it.
	if _, created, err = ConfigPath(""); err != nil || created {
		t.Fatalf("second ConfigPath: created=%v err=%v", created, err)
	}
}

func TestConfigPathDoesNotCreateExplicitPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "typo.yaml")
	got, created, err := ConfigPath(path)
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if created || got != path {
		t.Fatalf("got path=%q created=%v, want the explicit path untouched", got, created)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("explicit path was created: %v", err)
	}
}

func TestConfigPathWithoutHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix HOME-based fallback")
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if DefaultConfigPath() != "" {
		t.Skip("environment still reports a config directory")
	}
	if _, _, err := ConfigPath(""); err == nil {
		t.Fatal("want an error when no config path can be resolved")
	}
}
