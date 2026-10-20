package node

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

const (
	// appConfigDir is the per-app directory under the platform config root.
	appConfigDir = "fleetdeck"
	// defaultConfigName is the file the node reads when --config is omitted.
	defaultConfigName = "node.yaml"
	// defaultTokenName is the issued node credential inside the same root.
	defaultTokenName = "node-token"
)

// configTemplate seeds a node that was started without a config file. The
// placeholder paths are the resolved locations on this machine.
const configTemplate = `# FleetDeck node configuration (created automatically).
#
# Choose the agents this machine offers:
#   fleetdeck-node config --config "%[1]s"
#
# First start (the hub URL is written back into this file afterwards):
#   fleetdeck-node --config "%[1]s" --hub-url ws://<hub>:7710/ws/node --join-token <token>
name: %[2]s
%[3]s`

// DefaultConfigPath returns the per-user config file. XDG_CONFIG_HOME wins on
// every OS when it holds an absolute path; otherwise the platform convention
// applies:
//
//   - Linux/BSD: ~/.config/fleetdeck/node.yaml
//   - macOS:     ~/Library/Application Support/fleetdeck/node.yaml
//   - Windows:   %AppData%\fleetdeck\node.yaml
//
// It is empty when no home/config directory resolves.
func DefaultConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(base) {
		dir, err := os.UserConfigDir()
		if err != nil {
			return ""
		}
		base = dir
	}
	return filepath.Join(base, appConfigDir, defaultConfigName)
}

// DefaultTokenFile returns where the issued node credential is stored.
// XDG_STATE_HOME wins on every OS when absolute; otherwise:
//
//   - Linux/BSD: ~/.local/state/fleetdeck/node-token
//   - macOS:     ~/Library/Application Support/fleetdeck/node-token
//   - Windows:   %LocalAppData%\fleetdeck\node-token
//
// It is empty when no such directory resolves.
func DefaultTokenFile() string {
	if dir := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, appConfigDir, defaultTokenName)
	}
	switch runtime.GOOS {
	case "windows":
		// os.UserCacheDir is %LocalAppData% on Windows: per-user, not roamed.
		if dir, err := os.UserCacheDir(); err == nil {
			return filepath.Join(dir, appConfigDir, defaultTokenName)
		}
	case "darwin":
		if dir, err := os.UserConfigDir(); err == nil {
			return filepath.Join(dir, appConfigDir, defaultTokenName)
		}
	default:
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, ".local", "state", appConfigDir, defaultTokenName)
		}
		if dir, err := os.UserConfigDir(); err == nil {
			return filepath.Join(dir, appConfigDir, defaultTokenName)
		}
	}
	return ""
}

// ConfigPath resolves the config file to use: an explicit --config path as
// given, otherwise the platform default, created from a template when it does
// not exist yet. created reports the template write so callers can say so.
//
// An explicit path that does not exist is NOT created: a typo must not silently
// start a second, empty node configuration.
func ConfigPath(explicit string) (path string, created bool, err error) {
	if explicit != "" {
		return explicit, false, nil
	}
	path = DefaultConfigPath()
	if path == "" {
		return "", false, errors.New("node: cannot resolve a config directory; pass --config <file>")
	}
	if _, err := os.Stat(path); err == nil {
		return path, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	if err := createDefaultConfig(path); err != nil {
		return "", false, fmt.Errorf("create %s: %w", path, err)
	}
	return path, true, nil
}

// createDefaultConfig writes the template with this machine's hostname and the
// default token location. The parent directory is created 0700; the file is
// 0600 and never overwrites an existing one.
func createDefaultConfig(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	name, err := os.Hostname()
	if err != nil || name == "" {
		name = "node"
	}
	tokenLine := ""
	if token := DefaultTokenFile(); token != "" {
		tokenLine = "token_file: " + token + "\n"
	}
	body := fmt.Sprintf(configTemplate, path, strconv.Quote(name), tokenLine)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(body); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
