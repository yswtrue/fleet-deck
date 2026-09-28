package acp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- resolveInWorkspace (the fs/* sandbox) ----------------------------------

func TestResolveInWorkspace(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	inside := filepath.Join(workspace, "sub")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	// The guard resolves symlinks (on macOS t.TempDir lives under the /tmp ->
	// /private/tmp symlink), so expected paths are the resolved form.
	resolvedWs, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}

	// A symlink escaping the workspace must be rejected, both as an existing
	// leaf and as a parent of a new file.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(workspace, "leak.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "leakdir")); err != nil {
		t.Fatal(err)
	}

	ok := map[string]string{
		"hello.txt":                     filepath.Join(resolvedWs, "hello.txt"),
		"./hello.txt":                   filepath.Join(resolvedWs, "hello.txt"),
		"sub/../hello.txt":              filepath.Join(resolvedWs, "hello.txt"),
		filepath.Join(workspace, "sub"): filepath.Join(resolvedWs, "sub"),
		"new-file.txt":                  filepath.Join(resolvedWs, "new-file.txt"),
		"sub/new-file.txt":              filepath.Join(resolvedWs, "sub", "new-file.txt"),
	}
	for input, want := range ok {
		got, err := resolveInWorkspace(workspace, input)
		if err != nil {
			t.Errorf("resolveInWorkspace(%q) error = %v, want ok", input, err)
			continue
		}
		if got != want {
			t.Errorf("resolveInWorkspace(%q) = %q, want %q", input, got, want)
		}
	}

	rejected := []string{
		"",
		"   ",
		"..",
		"../escape.txt",
		"sub/../../escape.txt",
		filepath.Join(outside, "secret.txt"), // absolute path outside
		"leak.txt",                           // symlink leaf pointing outside
		"leakdir/secret.txt",                 // symlink parent pointing outside
		"leakdir/new.txt",                    // new file under a symlinked parent
	}
	for _, input := range rejected {
		if got, err := resolveInWorkspace(workspace, input); err == nil {
			t.Errorf("resolveInWorkspace(%q) = %q, want error", input, got)
		}
	}

	if _, err := resolveInWorkspace("", "hello.txt"); err == nil {
		t.Error("resolveInWorkspace with no workspace: want error")
	}
}

func TestSliceLines(t *testing.T) {
	content := "a\nb\nc\nd"
	cases := []struct {
		line, limit int
		want        string
	}{
		{0, 0, content},
		{2, 0, "b\nc\nd"},
		{2, 2, "b\nc"},
		{1, 1, "a"},
		{9, 0, ""},
		{0, 2, "a\nb"},
	}
	for _, tc := range cases {
		if got := sliceLines(content, tc.line, tc.limit); got != tc.want {
			t.Errorf("sliceLines(%q, %d, %d) = %q, want %q", content, tc.line, tc.limit, got, tc.want)
		}
	}
	// Trailing newlines survive the round trip through the line window.
	if got := sliceLines("x\n", 0, 0); got != "x\n" {
		t.Errorf("sliceLines trailing newline = %q, want %q", got, "x\n")
	}
}

// ---- end-to-end over the fake agent -----------------------------------------

// TestFSRequests drives fs/* through the JSON-RPC layer: the fake agent reads
// a seeded file, writes a new one, and attempts a workspace escape. The
// adapter must serve the first two and refuse the third.
func TestFSRequests(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "hello.txt"), []byte("hello-content"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, rec := startTestAdapter(t, adapterOpts{scenario: "fs", workspace: workspace})
	rec.waitDone(t)

	joined := strings.Join(rec.texts(), "")
	for _, want := range []string{"read:hello-content", "write:ok", "escape:refused"} {
		if !strings.Contains(joined, want) {
			t.Errorf("agent output %q missing %q", joined, want)
		}
	}
	if strings.Contains(joined, "escape:leaked") {
		t.Errorf("workspace escape was served: %q", joined)
	}

	written, err := os.ReadFile(filepath.Join(workspace, "out.txt"))
	if err != nil {
		t.Fatalf("fs/write_text_file did not create out.txt: %v", err)
	}
	if string(written) != "written-by-agent" {
		t.Fatalf("out.txt = %q, want %q", written, "written-by-agent")
	}
}
