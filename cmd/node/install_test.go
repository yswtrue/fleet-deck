package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveBinDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Setenv("XDG_BIN_HOME", "/opt/fleet/bin")
	if got, err := resolveBinDir(""); err != nil || got != "/opt/fleet/bin" {
		t.Fatalf("absolute XDG_BIN_HOME = %q, %v; want /opt/fleet/bin", got, err)
	}

	t.Setenv("XDG_BIN_HOME", "relative/bin")
	want := filepath.Join(home, ".local", "bin")
	if got, err := resolveBinDir(""); err != nil || got != want {
		t.Fatalf("relative XDG_BIN_HOME = %q, %v; want %q", got, err, want)
	}

	if got, err := resolveBinDir("custom"); err != nil || !filepath.IsAbs(got) || filepath.Base(got) != "custom" {
		t.Fatalf("flag bin dir = %q, %v; want absolute .../custom", got, err)
	}
}

func TestSystemdUnit(t *testing.T) {
	unit := systemdUnit("/home/a/.local/bin/fleetdeck-node", "/home/a/.config/fleetdeck/node.yaml")
	for _, want := range []string{
		"ExecStart=/home/a/.local/bin/fleetdeck-node --config /home/a/.config/fleetdeck/node.yaml\n",
		"Restart=always\n",
		"WantedBy=default.target\n",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}

	spaced := systemdUnit("/opt/my apps/fleetdeck-node", "/tmp/a%b/node.yaml")
	if !strings.Contains(spaced, `ExecStart="/opt/my apps/fleetdeck-node" --config "/tmp/a%%b/node.yaml"`) {
		t.Errorf("paths with spaces/percent not escaped:\n%s", spaced)
	}
}

func TestLaunchdPlist(t *testing.T) {
	plist := launchdPlist("/Users/a/.local/bin/fleetdeck-node", "/Users/a/Library/Application Support/fleetdeck/node.yaml", "/Users/a/Library/Logs/fleetdeck-node.log")
	for _, want := range []string{
		"<string>/Users/a/.local/bin/fleetdeck-node</string>",
		"<string>--config</string>",
		"<string>/Users/a/Library/Application Support/fleetdeck/node.yaml</string>",
		"<key>KeepAlive</key>\n\t<true/>",
		"<key>RunAtLoad</key>\n\t<true/>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing %q:\n%s", want, plist)
		}
	}

	escaped := launchdPlist("/tmp/a&b", "/tmp/<x>", "/tmp/log")
	if !strings.Contains(escaped, "/tmp/a&amp;b") || !strings.Contains(escaped, "/tmp/&lt;x&gt;") {
		t.Errorf("XML escaping missing:\n%s", escaped)
	}
}

func TestRestartUserServiceWithoutUnit(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no user service on this platform")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "linux" {
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	}
	installed, restarted, err := restartUserService()
	if err != nil || installed || restarted {
		t.Fatalf("restartUserService without a unit = (%v, %v, %v); want (false, false, nil)", installed, restarted, err)
	}
}

func TestRestartUserServiceRestartsActiveSystemd(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd path")
	}
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	unitPath := filepath.Join(cfgDir, "systemd", "user", systemdUnitName)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte(systemdUnit("/bin/true", "/tmp/node.yaml")), 0o644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	calls := filepath.Join(binDir, "calls.log")
	script := "#!/bin/sh\necho \"$*\" >> " + calls + "\nif [ \"$1\" = \"--user\" ] && [ \"$2\" = \"is-active\" ]; then echo active; fi\n"
	if err := os.WriteFile(filepath.Join(binDir, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	installed, restarted, err := restartUserService()
	if err != nil || !installed || !restarted {
		t.Fatalf("restartUserService = (%v, %v, %v); want (true, true, nil)", installed, restarted, err)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "--user restart "+systemdUnitName) {
		t.Fatalf("systemctl restart was not called:\n%s", data)
	}
}

func TestInstallBinaryCopiesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	got, err := installBinary(dir)
	if err != nil {
		t.Fatalf("installBinary: %v", err)
	}
	if want := filepath.Join(dir, programName+exeSuffix()); got != want {
		t.Fatalf("installBinary = %q, want %q", got, want)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("installed binary is not executable: %v", info.Mode())
	}
	src, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if same, err := sameContent(src, got); err != nil || !same {
		t.Fatalf("copy differs from source (same=%v, err=%v)", same, err)
	}

	// A second install must not fail when the destination is already there.
	if _, err := installBinary(dir); err != nil {
		t.Fatalf("second installBinary: %v", err)
	}
}
