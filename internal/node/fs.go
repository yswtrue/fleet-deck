package node

import (
	"os"
	"path/filepath"

	"fleetdeck/internal/protocol"
)

// listDir answers a hub fs.list_req by enumerating the directories under the
// requested path (the node user's home directory when the request is empty).
//
// This backs the web UI's folder picker for workspace creation. Only
// directories are reported — a workspace path must be a directory — and the
// requested path is resolved (absolute + symlinks) so the user persists the
// canonical path the agent sandbox and adapters will use. Any failure is
// carried in the response payload, never in the read loop.
func (n *Node) listDir(req protocol.FSListRequest) {
	n.enqueue(protocol.NewEnvelope(protocol.MsgFSListResp, listDirs(req)))
}

// listDirs builds the response for one directory listing. It is split from
// listDir so filesystem behavior is testable without a hub connection.
func listDirs(req protocol.FSListRequest) protocol.FSListResponse {
	resp := protocol.FSListResponse{ReqID: req.ReqID, Entries: []protocol.FSDirEntry{}}

	path := req.Path
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			resp.Error = "cannot resolve the node home directory: " + err.Error()
			return resp
		}
		path = home
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		resp.Error = err.Error()
		return resp
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		resp.Error = err.Error()
		return resp
	}
	info, err := os.Stat(resolved)
	if err != nil {
		resp.Error = err.Error()
		return resp
	}
	if !info.IsDir() {
		resp.Error = resolved + " is not a directory"
		return resp
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		resp.Error = err.Error()
		return resp
	}

	resp.Path = resolved
	if parent := filepath.Dir(resolved); parent != resolved {
		resp.Parent = parent
	}
	for _, entry := range entries {
		full := filepath.Join(resolved, entry.Name())
		// os.ReadDir reports link metadata for symlinks; follow them so a
		// symlinked checkout is browsable (the next request resolves it).
		if entry.IsDir() || isDirSymlink(entry, full) {
			resp.Entries = append(resp.Entries, protocol.FSDirEntry{Name: entry.Name(), Path: full})
		}
	}
	return resp
}

// isDirSymlink reports whether entry is a symlink pointing at a directory.
func isDirSymlink(entry os.DirEntry, full string) bool {
	if entry.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(full)
	return err == nil && info.IsDir()
}
