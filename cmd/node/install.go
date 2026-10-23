package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"fleetdeck/internal/node"
)

const (
	programName     = "fleetdeck-node"
	systemdUnitName = "fleetdeck-node.service"
	launchdLabel    = "com.fleetdeck.node"
)

// runInstall implements `node install`: copy the running binary into a
// per-user bin directory and register a user-scoped service that starts it at
// login and restarts it when it exits (systemd --user on Linux, launchd on
// macOS). On other platforms the binary is installed but no service is
// created (Windows has no equivalent user-scoped autostart with restart).
func runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	configFlag := fs.String("config", "", "node config file (default: the platform config file, created when missing)")
	binDirFlag := fs.String("bin-dir", "", "install directory (default: $XDG_BIN_HOME, then ~/.local/bin)")
	noStart := fs.Bool("no-start", false, "enable the user service but do not start it now")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("install: unexpected argument %q", fs.Arg(0))
	}

	cfgPath, _, err := node.ConfigPath(*configFlag)
	if err != nil {
		return err
	}
	cfgPath, err = filepath.Abs(cfgPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(cfgPath); err != nil {
		return fmt.Errorf("install: config %s: %w (run `%s config` first?)", cfgPath, err, programName)
	}
	cfg, err := node.LoadConfig(cfgPath)
	if err != nil {
		return err
	}

	binPath, err := installBinary(*binDirFlag)
	if err != nil {
		return err
	}
	fmt.Printf("Installed %s %s to %s\n", programName, version, binPath)

	// Starting without a hub endpoint or without the node credential would
	// make the service exit and restart on a loop; enable it, but say what is
	// missing instead of starting it.
	var blocker string
	switch {
	case cfg.HubURL == "":
		blocker = "the config has no hub_url yet"
	case !fileExists(cfg.TokenFile):
		blocker = fmt.Sprintf("no node credential at %s yet", cfg.TokenFile)
	}
	start := !*noStart && blocker == ""
	if err := installUserService(binPath, cfgPath, start); err != nil {
		return err
	}

	if blocker != "" {
		fmt.Println()
		fmt.Printf("The user service is registered but not started: %s.\n", blocker)
		fmt.Println("Register this node once, then start the service:")
		if cfg.HubURL == "" {
			fmt.Printf("  %s --config %s --hub-url ws://<hub>:7710/ws/node --join-token <token from the UI>\n", binPath, cfgPath)
		} else {
			fmt.Printf("  %s --config %s --join-token <token from the UI>   # Ctrl-C after \"registered\"\n", binPath, cfgPath)
		}
		switch runtime.GOOS {
		case "linux":
			fmt.Printf("  systemctl --user start %s\n", systemdUnitName)
		case "darwin":
			fmt.Printf("  launchctl kickstart -k gui/%d/%s\n", os.Getuid(), launchdLabel)
		}
	}
	return nil
}

// installBinary copies the running executable into dir (the caller may be
// running the binary from a build directory or a download). The copy is atomic
// so an already-installed service never sees a partial file.
func installBinary(dirFlag string) (string, error) {
	src, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("install: locate the running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(src); err == nil {
		src = resolved
	}
	dir, err := resolveBinDir(dirFlag)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("install: create %s: %w", dir, err)
	}
	dst := filepath.Join(dir, programName+exeSuffix())
	if samePath(src, dst) {
		return dst, nil
	}
	if err := copyExecutable(src, dst); err != nil {
		hint := ""
		if runtime.GOOS == "windows" {
			hint = " (stop the running node first; Windows cannot replace a running executable)"
		}
		return "", fmt.Errorf("install: copy to %s: %w%s", dst, err, hint)
	}
	warnIfNotOnPath(dir)
	return dst, nil
}

// resolveBinDir picks the per-user install directory: the flag, then an
// absolute $XDG_BIN_HOME, then ~/.local/bin.
func resolveBinDir(flagValue string) (string, error) {
	if flagValue != "" {
		return filepath.Abs(flagValue)
	}
	if dir := os.Getenv("XDG_BIN_HOME"); filepath.IsAbs(dir) {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("install: cannot resolve the home directory; pass --bin-dir")
	}
	return filepath.Join(home, ".local", "bin"), nil
}

// copyExecutable writes src to dst via a temporary file in dst's directory and
// renames it into place, so dst is either the old or the complete new binary.
func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+programName+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after the rename

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

func warnIfNotOnPath(dir string) {
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if entry == "" {
			continue
		}
		if abs, err := filepath.Abs(entry); err == nil && samePath(abs, dir) {
			return
		}
	}
	fmt.Fprintf(os.Stderr, "Warning: %s is not on PATH; add it in your shell profile to run `%s` directly.\n", dir, programName)
}

// installUserService registers (and optionally starts) the platform's
// user-scoped autostart unit. Unsupported platforms get the binary only.
func installUserService(binPath, cfgPath string, start bool) error {
	switch runtime.GOOS {
	case "linux":
		return installSystemd(binPath, cfgPath, start)
	case "darwin":
		return installLaunchd(binPath, cfgPath, start)
	default:
		fmt.Println("No user service installed: automatic start on login is supported on Linux (systemd) and macOS (launchd).")
		fmt.Printf("Run %s at login with your platform's scheduler (e.g. Task Scheduler on Windows).\n", binPath)
		return nil
	}
}

func installSystemd(binPath, cfgPath string, start bool) error {
	unitPath, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return fmt.Errorf("install: create %s: %w", filepath.Dir(unitPath), err)
	}
	if err := os.WriteFile(unitPath, []byte(systemdUnit(binPath, cfgPath)), 0o644); err != nil {
		return fmt.Errorf("install: write %s: %w", unitPath, err)
	}
	fmt.Printf("Wrote %s\n", unitPath)

	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return fmt.Errorf("install: systemd not found (%v); enable the unit manually: systemctl --user enable --now %s", err, systemdUnitName)
	}
	if out, err := runCmd(systemctl, "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %v: %s", err, out)
	}
	if out, err := runCmd(systemctl, "--user", "enable", systemdUnitName); err != nil {
		return fmt.Errorf("systemctl --user enable %s: %v: %s", systemdUnitName, err, out)
	}
	enableLinger()

	if !start {
		fmt.Printf("Service %s enabled. Start it with: systemctl --user start %s\n", systemdUnitName, systemdUnitName)
		return nil
	}
	if out, err := runCmd(systemctl, "--user", "restart", systemdUnitName); err != nil {
		return fmt.Errorf("systemctl --user restart %s: %v: %s", systemdUnitName, err, out)
	}
	if out, err := runCmd(systemctl, "--user", "is-active", systemdUnitName); err != nil || strings.TrimSpace(out) != "active" {
		fmt.Fprintf(os.Stderr, "Warning: %s was started but is not active (%s); check: journalctl --user -u %s -n 50\n", systemdUnitName, strings.TrimSpace(out), systemdUnitName)
		return nil
	}
	fmt.Printf("Service %s enabled and running. Logs: journalctl --user -u %s -f\n", systemdUnitName, systemdUnitName)
	return nil
}

// enableLinger lets the user service run while the user is not logged in.
// Failure is a warning: some systems disable lingering by policy.
func enableLinger() {
	name := os.Getenv("USER")
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	loginctl, err := exec.LookPath("loginctl")
	if err != nil || name == "" {
		return
	}
	if out, err := runCmd(loginctl, "show-user", name, "--property=Linger"); err == nil && strings.Contains(out, "Linger=yes") {
		return
	}
	if out, err := runCmd(loginctl, "enable-linger", name); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: loginctl enable-linger %s failed (%v: %s); the service starts at login only.\n", name, err, out)
	}
}

func installLaunchd(binPath, cfgPath string, start bool) error {
	plistPath, err := launchdPlistPath()
	if err != nil {
		return err
	}
	logPath, err := launchdLogPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("install: create %s: %w", filepath.Dir(plistPath), err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("install: create %s: %w", filepath.Dir(logPath), err)
	}
	if err := os.WriteFile(plistPath, []byte(launchdPlist(binPath, cfgPath, logPath)), 0o644); err != nil {
		return fmt.Errorf("install: write %s: %w", plistPath, err)
	}
	fmt.Printf("Wrote %s\n", plistPath)

	launchctl, err := exec.LookPath("launchctl")
	if err != nil {
		return fmt.Errorf("install: launchctl not found (%v); load the agent manually: launchctl bootstrap gui/%d %s", err, os.Getuid(), plistPath)
	}
	target := fmt.Sprintf("gui/%d", os.Getuid())
	if !start {
		fmt.Printf("Launch agent written; RunAtLoad starts it at the next login. Logs: %s\n", logPath)
		return nil
	}
	// A reinstall must replace the loaded definition; bootout on a job that is
	// not loaded fails harmlessly.
	_, _ = runCmd(launchctl, "bootout", target+"/"+launchdLabel)
	if out, err := runCmd(launchctl, "bootstrap", target, plistPath); err != nil {
		return fmt.Errorf("launchctl bootstrap %s: %v: %s", plistPath, err, out)
	}
	fmt.Printf("Launch agent loaded and running. Logs: %s\n", logPath)
	return nil
}

// restartUserService restarts an installed user service after `update` replaced
// the binary. installed reports whether a service unit exists; restarted
// reports whether a running job was restarted (a stopped one is left alone).
func restartUserService() (installed, restarted bool, err error) {
	switch runtime.GOOS {
	case "linux":
		unitPath, err := systemdUnitPath()
		if err != nil || !fileExists(unitPath) {
			return false, false, nil
		}
		systemctl, err := exec.LookPath("systemctl")
		if err != nil {
			return true, false, fmt.Errorf("systemd not found (%v); restart %s manually", err, systemdUnitName)
		}
		if out, err := runCmd(systemctl, "--user", "is-active", systemdUnitName); err != nil || strings.TrimSpace(out) != "active" {
			return true, false, nil
		}
		if out, err := runCmd(systemctl, "--user", "restart", systemdUnitName); err != nil {
			return true, false, fmt.Errorf("systemctl --user restart %s: %v: %s", systemdUnitName, err, out)
		}
		return true, true, nil
	case "darwin":
		plistPath, err := launchdPlistPath()
		if err != nil || !fileExists(plistPath) {
			return false, false, nil
		}
		launchctl, err := exec.LookPath("launchctl")
		if err != nil {
			return true, false, fmt.Errorf("launchctl not found (%v); restart %s manually", err, launchdLabel)
		}
		target := fmt.Sprintf("gui/%d/%s", os.Getuid(), launchdLabel)
		if _, err := runCmd(launchctl, "print", target); err != nil {
			return true, false, nil // written but not loaded in this session
		}
		if out, err := runCmd(launchctl, "kickstart", "-k", target); err != nil {
			return true, false, fmt.Errorf("launchctl kickstart -k %s: %v: %s", target, err, out)
		}
		return true, true, nil
	}
	return false, false, nil
}

func systemdUnitPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("install: resolve the config directory: %w", err)
	}
	return filepath.Join(dir, "systemd", "user", systemdUnitName), nil
}

func launchdPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("install: resolve the home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
}

func launchdLogPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("install: resolve the home directory: %w", err)
	}
	return filepath.Join(home, "Library", "Logs", programName+".log"), nil
}

// systemdUnit renders the user unit. Restart=always covers both crashes and a
// clean exit, which is what "keep the node connected" means here.
func systemdUnit(binPath, cfgPath string) string {
	return `# Generated by ` + programName + ` install; add overrides with:
#   systemctl --user edit ` + systemdUnitName + `
[Unit]
Description=FleetDeck node daemon
Documentation=https://github.com/` + releaseRepo + `
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=` + systemdArg(binPath) + ` --config ` + systemdArg(cfgPath) + `
Restart=always
RestartSec=5s
TimeoutStopSec=30s

[Install]
WantedBy=default.target
`
}

// systemdArg quotes a path for ExecStart and escapes the characters systemd
// expands there (specifiers `%` and environment `$`). Paths without them stay
// unquoted, which keeps the unit readable.
func systemdArg(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\%$") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '%':
			b.WriteString("%%")
		case '$':
			b.WriteString("$$")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func launchdPlist(binPath, cfgPath, logPath string) string {
	escape := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + launchdLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + escape.Replace(binPath) + `</string>
		<string>--config</string>
		<string>` + escape.Replace(cfgPath) + `</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>StandardOutPath</key>
	<string>` + escape.Replace(logPath) + `</string>
	<key>StandardErrorPath</key>
	<string>` + escape.Replace(logPath) + `</string>
</dict>
</plist>
`
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func samePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return absA == absB
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func runCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
