package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseAssetName(t *testing.T) {
	cases := []struct {
		tag, goos, goarch, want string
		wantErr                 bool
	}{
		{"v1.2.3", "linux", "amd64", "fleetdeck-node_v1.2.3_linux_amd64.tar.gz", false},
		{"v1.2.3", "darwin", "arm64", "fleetdeck-node_v1.2.3_darwin_arm64.tar.gz", false},
		{"v1.2.3", "windows", "amd64", "fleetdeck-node_v1.2.3_windows_amd64.zip", false},
		{"v1.2.3", "freebsd", "amd64", "", true},
		{"v1.2.3", "linux", "riscv64", "", true},
	}
	for _, c := range cases {
		got, err := releaseAssetName(c.tag, c.goos, c.goarch)
		if c.wantErr {
			if err == nil {
				t.Errorf("releaseAssetName(%s/%s) = %q, want error", c.goos, c.goarch, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("releaseAssetName(%s/%s) = %q, %v; want %q", c.goos, c.goarch, got, err, c.want)
		}
	}
}

func TestChecksumFor(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	sums := "0000  other.tar.gz\n" + digest + "  fleetdeck-node_v1.tar.gz\n" + digest + " *fleetdeck-node_v2.zip\n"
	if got, err := checksumFor(sums, "fleetdeck-node_v1.tar.gz"); err != nil || got != digest {
		t.Fatalf("checksumFor = %q, %v; want %q", got, err, digest)
	}
	if got, err := checksumFor(sums, "fleetdeck-node_v2.zip"); err != nil || got != digest {
		t.Fatalf("star-prefixed checksumFor = %q, %v; want %q", got, err, digest)
	}
	if _, err := checksumFor(sums, "missing.tar.gz"); err == nil {
		t.Fatal("checksumFor accepted a missing file")
	}
}

func TestExtractBinaryTarGz(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "release.tar.gz")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	writeTarFile(t, tw, "fleetdeck-node", "new binary")
	writeTarFile(t, tw, "node.config.example.yaml", "name: x")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := extractBinary(archive, "fleetdeck-node", &out); err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	if out.String() != "new binary" {
		t.Fatalf("extracted %q, want %q", out.String(), "new binary")
	}
	if err := extractBinary(archive, "absent", &out); err == nil {
		t.Fatal("extractBinary accepted an archive without the member")
	}
}

func TestExtractBinaryZip(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "release.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("fleetdeck-node.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("windows binary")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := extractBinary(archive, "fleetdeck-node.exe", &out); err != nil {
		t.Fatalf("extractBinary zip: %v", err)
	}
	if out.String() != "windows binary" {
		t.Fatalf("extracted %q", out.String())
	}
}

func TestRunUpdateDownloadsAndReplaces(t *testing.T) {
	const tag = "v9.9.9"
	payload := []byte("#!/bin/sh\necho new\n")
	archive := archiveBytesForTest(t, payload)
	digest := sha256.Sum256(archive)
	sums := hex.EncodeToString(digest[:]) + "  " + archiveNameForTest(tag) + "\n"

	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/repos/test/repo/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": tag,
			"assets": []map[string]string{
				{"name": "SHA256SUMS", "browser_download_url": base + "/SHA256SUMS"},
				{"name": archiveNameForTest(tag), "browser_download_url": base + "/archive"},
			},
		})
	})
	mux.HandleFunc("/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(sums)) })
	mux.HandleFunc("/archive", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL

	oldBase := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = oldBase }()

	target := filepath.Join(t.TempDir(), "fleetdeck-node")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runUpdate([]string{"--repo", "test/repo", "--bin", target, "--no-restart"}); err != nil {
		t.Fatalf("runUpdate: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("target content = %q, want %q", got, payload)
	}
}

func TestRunUpdateRejectsChecksumMismatch(t *testing.T) {
	const tag = "v9.9.9"
	archive := archiveBytesForTest(t, []byte("new binary"))
	sums := strings.Repeat("00", 32) + "  " + archiveNameForTest(tag) + "\n"

	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/repos/test/repo/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": tag,
			"assets": []map[string]string{
				{"name": "SHA256SUMS", "browser_download_url": base + "/SHA256SUMS"},
				{"name": archiveNameForTest(tag), "browser_download_url": base + "/archive"},
			},
		})
	})
	mux.HandleFunc("/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(sums)) })
	mux.HandleFunc("/archive", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL

	oldBase := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = oldBase }()

	target := filepath.Join(t.TempDir(), "fleetdeck-node")
	old := []byte("old binary")
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}
	err := runUpdate([]string{"--repo", "test/repo", "--bin", target, "--no-restart"})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("runUpdate error = %v, want checksum mismatch", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, old) {
		t.Fatalf("target was modified on a failed update: %q", got)
	}
}

func archiveNameForTest(tag string) string {
	name, _ := releaseAssetName(tag, runtime.GOOS, runtime.GOARCH)
	return name
}

func archiveBytesForTest(t *testing.T, content []byte) []byte {
	t.Helper()
	if runtime.GOOS != "windows" {
		return tarGzBytes(t, programName, content)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(programName + ".exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeTarFile(t *testing.T, tw *tar.Writer, name, content string) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
}

func tarGzBytes(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
