package node

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// detectMachineID returns a stable, hashed fingerprint of the computer the
// daemon runs on. The hub permits at most one node per fingerprint.
//
// Sources, in order: an OS-provided machine identifier (Linux machine-id,
// macOS IOPlatformUUID, Windows MachineGuid), then a generated id persisted in
// the user's config directory. The raw identifier is hashed so the hub (and
// logs) never store a hardware serial.
func detectMachineID() string {
	machineIDOnce.Do(func() {
		if raw, kind := osMachineID(); raw != "" {
			machineIDValue = hashMachineID(kind, raw)
			return
		}
		if raw := generatedMachineID(); raw != "" {
			machineIDValue = hashMachineID("generated", raw)
			return
		}
		// Last resort: hostname. Less stable (renames split the identity), but
		// it still prevents accidental duplicate registrations.
		if host, err := os.Hostname(); err == nil && host != "" {
			machineIDValue = hashMachineID("hostname", host)
		}
	})
	return machineIDValue
}

var (
	machineIDOnce  sync.Once
	machineIDValue string
)

func hashMachineID(kind, raw string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + raw))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// osMachineID reads the platform's stable machine identifier.
func osMachineID() (raw, kind string) {
	switch runtime.GOOS {
	case "linux":
		for _, path := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			if value := strings.TrimSpace(string(data)); value != "" {
				return value, "linux"
			}
		}
	case "darwin":
		// IOPlatformUUID is the hardware UUID; it survives OS reinstalls.
		out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
		if err == nil {
			if value := ioPlatformUUID(string(out)); value != "" {
				return value, "darwin"
			}
		}
	case "windows":
		out, err := exec.Command("reg", "query", `HKLM\SOFTWARE\Microsoft\Cryptography`, "/v", "MachineGuid").Output()
		if err == nil {
			// "    MachineGuid    REG_SZ    xxxx-..."
			fields := strings.Fields(string(out))
			if len(fields) >= 3 {
				return strings.TrimSpace(fields[len(fields)-1]), "windows"
			}
		}
	}
	return "", ""
}

var ioPlatformUUIDPattern = regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([^"]+)"`)

func ioPlatformUUID(ioregOutput string) string {
	match := ioPlatformUUIDPattern.FindStringSubmatch(ioregOutput)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

// generatedMachineID persists a random id under the user's config directory.
// Tests and ephemeral containers may not have one; the hostname fallback then
// keeps registration from failing outright.
func generatedMachineID() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return ""
	}
	path := filepath.Join(dir, "fleetdeck", "machine-id")
	if data, err := os.ReadFile(path); err == nil {
		if value := strings.TrimSpace(string(data)); value != "" {
			return value
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return ""
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	value := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		return ""
	}
	return value
}
