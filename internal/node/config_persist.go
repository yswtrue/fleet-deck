package node

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// hubURLKey matches the top-level hub_url line and captures everything after
// the colon, so a trailing comment survives the rewrite (the value never spans
// lines in this schema).
var hubURLKey = regexp.MustCompile(`(?m)^hub_url[ \t]*:[ \t]*(.*)$`)

// persistHubURL writes hubURL into the config file at path while leaving every
// other byte (comments, ordering, spacing) untouched, replacing the file
// atomically. A missing file is an error: the daemon never invents a config the
// operator did not point it at.
func persistHubURL(path, hubURL string) error {
	if path == "" {
		return errors.New("no config file to update (start with --config <file>)")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	updated, changed := setHubURL(raw, hubURL)
	if !changed {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".node-config-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(updated); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err := os.Chmod(tmp.Name(), info.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// setHubURL returns raw with its hub_url value replaced (appending the key when
// it is absent) and whether the content changed.
func setHubURL(raw []byte, hubURL string) ([]byte, bool) {
	text := string(raw)
	if loc := hubURLKey.FindStringSubmatchIndex(text); loc != nil {
		value := text[loc[2]:loc[3]]
		comment := ""
		if i := strings.Index(value, " #"); i >= 0 {
			comment = value[i:]
		}
		line := "hub_url: " + hubURL + comment
		if line == text[loc[0]:loc[1]] {
			return raw, false
		}
		return []byte(text[:loc[0]] + line + text[loc[1]:]), true
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return []byte(text + "hub_url: " + hubURL + "\n"), true
}
