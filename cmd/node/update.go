package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// releaseRepo is the GitHub repository that publishes node binaries. Each
// release carries fleetdeck-node_<tag>_<os>_<arch>.{tar.gz,zip} plus
// SHA256SUMS (see .github/workflows/release.yml).
const releaseRepo = "yswtrue/fleet-deck"

// githubAPIBase is overridden in tests to point at an httptest server.
var githubAPIBase = "https://api.github.com"

// runUpdate implements `node update`: download the latest (or a pinned)
// release, verify it against the release's SHA256SUMS, and atomically replace
// the installed binary. An installed user service is restarted so it runs the
// new build.
func runUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	binFlag := fs.String("bin", "", "binary to replace (default: the running executable)")
	versionFlag := fs.String("version", "", "release tag to install (default: the latest release)")
	repoFlag := fs.String("repo", releaseRepo, "GitHub repository owner/name")
	noRestart := fs.Bool("no-restart", false, "do not restart an installed user service")
	check := fs.Bool("check", false, "report the available release without downloading it")
	tokenFlag := fs.String("token", "", "GitHub API token (default: $GITHUB_TOKEN, then $GH_TOKEN)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("update: unexpected argument %q", fs.Arg(0))
	}

	target := *binFlag
	if target == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("update: locate the running binary: %w", err)
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		target = exe
	}
	target, err := filepath.Abs(target)
	if err != nil {
		return err
	}

	token := *tokenFlag
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 5 * time.Minute}

	rel, err := fetchRelease(ctx, client, *repoFlag, *versionFlag, token)
	if err != nil {
		return err
	}
	archiveName, err := releaseAssetName(rel.TagName, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	archive, ok := findAsset(rel, archiveName)
	if !ok {
		return fmt.Errorf("update: release %s has no %s (available: %s)", rel.TagName, archiveName, assetNames(rel))
	}
	sums, ok := findAsset(rel, "SHA256SUMS")
	if !ok {
		return fmt.Errorf("update: release %s has no SHA256SUMS; refusing to install an unverified binary", rel.TagName)
	}

	if *check {
		fmt.Printf("current: %s\n", version)
		fmt.Printf("latest:  %s\n", rel.TagName)
		if version == rel.TagName {
			fmt.Println("up to date")
		}
		return nil
	}

	sumData, err := downloadBytes(ctx, client, sums.BrowserDownloadURL, token, 4<<20)
	if err != nil {
		return fmt.Errorf("update: download SHA256SUMS: %w", err)
	}
	want, err := checksumFor(string(sumData), archiveName)
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "fleetdeck-node-update-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	archivePath := filepath.Join(tmpDir, archiveName)
	if _, err := downloadFile(ctx, client, archive.BrowserDownloadURL, archivePath, token); err != nil {
		return fmt.Errorf("update: download %s: %w", archiveName, err)
	}
	got, err := sha256File(archivePath)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("update: %s checksum mismatch: got %s, want %s", archiveName, got, want)
	}

	// Extract next to the target so the final rename is atomic within one
	// filesystem; a running service keeps its old inode until restarted.
	tmp, err := os.CreateTemp(filepath.Dir(target), ".fleetdeck-node-update-*")
	if err != nil {
		return fmt.Errorf("update: create a temporary binary next to %s: %w", target, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := extractBinary(archivePath, programName+exeSuffix(), tmp); err != nil {
		tmp.Close()
		return fmt.Errorf("update: extract %s: %w", archiveName, err)
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

	if same, err := sameContent(target, tmpName); err == nil && same {
		fmt.Printf("Already at %s (%s); nothing to do.\n", rel.TagName, target)
		return nil
	}
	if err := os.Rename(tmpName, target); err != nil {
		hint := ""
		if runtime.GOOS == "windows" {
			hint = " (stop the node first; Windows cannot replace a running executable)"
		}
		return fmt.Errorf("update: replace %s: %w%s", target, err, hint)
	}
	fmt.Printf("Updated %s: %s -> %s\n", target, version, rel.TagName)

	if *noRestart {
		return nil
	}
	installed, restarted, err := restartUserService()
	if err != nil {
		return fmt.Errorf("update: binary installed, but the service restart failed: %w", err)
	}
	switch {
	case restarted:
		fmt.Println("Restarted the user service.")
	case installed:
		fmt.Println("The user service is installed but not running; start it to run the new binary.")
	default:
		fmt.Printf("No installed user service found; restart `%s` to run the new version.\n", programName)
	}
	return nil
}

// releaseAsset is one downloadable file on a GitHub release.
type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// release is the subset of the GitHub release payload the updater needs.
type release struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

// fetchRelease resolves the latest release, or one pinned tag.
func fetchRelease(ctx context.Context, client *http.Client, repo, tag, token string) (*release, error) {
	endpoint := githubAPIBase + "/repos/" + repo + "/releases/latest"
	if tag != "" {
		endpoint = githubAPIBase + "/repos/" + repo + "/releases/tags/" + url.PathEscape(tag)
	}
	req, err := newGitHubRequest(ctx, endpoint, token)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: query %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("update: %s: %s: %s", endpoint, resp.Status, strings.TrimSpace(string(body)))
	}
	var rel release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("update: decode release: %w", err)
	}
	if rel.TagName == "" {
		return nil, errors.New("update: release without a tag name")
	}
	return &rel, nil
}

// releaseAssetName is the packaged binary name produced by the release
// workflow: fleetdeck-node_<tag>_<os>_<arch>.tar.gz (zip on Windows).
func releaseAssetName(tag, goos, goarch string) (string, error) {
	switch goos {
	case "linux", "darwin":
	case "windows":
	default:
		return "", fmt.Errorf("update: no release binary for %s", goos)
	}
	switch goarch {
	case "amd64", "arm64":
	default:
		return "", fmt.Errorf("update: no release binary for %s/%s", goos, goarch)
	}
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("%s_%s_%s_%s%s", programName, tag, goos, goarch, ext), nil
}

func findAsset(rel *release, name string) (releaseAsset, bool) {
	for _, a := range rel.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return releaseAsset{}, false
}

func assetNames(rel *release) string {
	names := make([]string, 0, len(rel.Assets))
	for _, a := range rel.Assets {
		names = append(names, a.Name)
	}
	return strings.Join(names, ", ")
}

// checksumFor parses `sha256sum` output (hex, optional `*` prefix on the file
// name) and returns the digest for name.
func checksumFor(sums, name string) (string, error) {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		digest := strings.ToLower(fields[0])
		if len(digest) != 64 {
			return "", fmt.Errorf("SHA256SUMS: malformed digest for %s", name)
		}
		return digest, nil
	}
	return "", fmt.Errorf("%s is not listed in SHA256SUMS", name)
}

func newGitHubRequest(ctx context.Context, endpoint, token string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", programName+"/"+version)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}

func downloadBytes(ctx context.Context, client *http.Client, endpoint, token string, limit int64) ([]byte, error) {
	req, err := newGitHubRequest(ctx, endpoint, token)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", endpoint, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: response larger than %d bytes", endpoint, limit)
	}
	return data, nil
}

func downloadFile(ctx context.Context, client *http.Client, endpoint, dst, token string) (int64, error) {
	req, err := newGitHubRequest(ctx, endpoint, token)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s: %s", endpoint, resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(f, resp.Body)
	syncErr := f.Sync()
	closeErr := f.Close()
	switch {
	case copyErr != nil:
		return n, copyErr
	case syncErr != nil:
		return n, syncErr
	default:
		return n, closeErr
	}
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sameContent(a, b string) (bool, error) {
	ha, err := sha256File(a)
	if err != nil {
		return false, err
	}
	hb, err := sha256File(b)
	if err != nil {
		return false, err
	}
	return ha == hb, nil
}

// extractBinary copies the regular file named name out of a .tar.gz or .zip
// archive into w.
func extractBinary(archivePath, name string, w io.Writer) error {
	if strings.HasSuffix(archivePath, ".zip") {
		return extractZip(archivePath, name, w)
	}
	return extractTarGz(archivePath, name, w)
}

func extractTarGz(archivePath, name string, w io.Writer) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if !hdr.FileInfo().Mode().IsRegular() || filepath.Base(hdr.Name) != name {
			continue
		}
		_, err = io.Copy(w, tr)
		return err
	}
	return fmt.Errorf("archive does not contain %s", name)
}

func extractZip(archivePath, name string, w io.Writer) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		if f.FileInfo().IsDir() || filepath.Base(f.Name) != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(w, rc)
		closeErr := rc.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	return fmt.Errorf("archive does not contain %s", name)
}
