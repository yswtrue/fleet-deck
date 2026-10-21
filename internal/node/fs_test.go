package node

import (
	"os"
	"path/filepath"
	"testing"

	"fleetdeck/internal/protocol"
)

func TestListDirsEnumeratesDirectoriesOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "b-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "a-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "linked")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link-dir")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	resp := listDirs(protocol.FSListRequest{ReqID: "fs-1", Path: root})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	if resp.ReqID != "fs-1" {
		t.Fatalf("req_id = %q, want fs-1", resp.ReqID)
	}
	if resp.Path != root {
		t.Fatalf("path = %q, want %q", resp.Path, root)
	}
	if resp.Parent != filepath.Dir(root) {
		t.Fatalf("parent = %q, want %q", resp.Parent, filepath.Dir(root))
	}
	want := []string{"a-dir", "b-dir", "link-dir"} // ReadDir sorts by name; files are excluded
	if len(resp.Entries) != len(want) {
		t.Fatalf("entries = %+v, want names %v", resp.Entries, want)
	}
	for i, name := range want {
		if resp.Entries[i].Name != name {
			t.Fatalf("entry[%d] = %q, want %q", i, resp.Entries[i].Name, name)
		}
		if resp.Entries[i].Path != filepath.Join(root, name) {
			t.Fatalf("entry[%d] path = %q, want %q", i, resp.Entries[i].Path, filepath.Join(root, name))
		}
	}
}

func TestListDirsEmptyPathUsesHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, "project"), 0o755); err != nil {
		t.Fatal(err)
	}

	resp := listDirs(protocol.FSListRequest{ReqID: "fs-home"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	if resp.Path != home {
		t.Fatalf("path = %q, want home %q", resp.Path, home)
	}
	if len(resp.Entries) != 1 || resp.Entries[0].Name != "project" {
		t.Fatalf("entries = %+v, want [project]", resp.Entries)
	}
}

func TestListDirsReportsUnreadablePath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	resp := listDirs(protocol.FSListRequest{ReqID: "fs-missing", Path: missing})
	if resp.Error == "" {
		t.Fatal("expected an error for a missing directory")
	}
	if len(resp.Entries) != 0 {
		t.Fatalf("entries = %+v, want none", resp.Entries)
	}

	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp = listDirs(protocol.FSListRequest{ReqID: "fs-file", Path: file})
	if resp.Error == "" {
		t.Fatal("expected an error for a regular file")
	}
}

func TestListDirsResolvesSymlinkPath(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	resp := listDirs(protocol.FSListRequest{Path: link})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	if resp.Path != real {
		t.Fatalf("path = %q, want resolved %q", resp.Path, real)
	}
}
