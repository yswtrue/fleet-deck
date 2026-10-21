package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"fleetdeck/internal/protocol"
)

// fsRequestTimeout bounds one proxied directory-listing round trip. The node
// answers from local disk, so a few seconds is generous; a wedged node must
// not hold the HTTP request open.
const fsRequestTimeout = 10 * time.Second

// dirListing is the web-facing shape of GET /api/nodes/{id}/fs. It drops the
// internal ReqID so node-internal correlation ids never reach the UI.
type dirListing struct {
	Path    string                `json:"path"`
	Parent  string                `json:"parent,omitempty"`
	Entries []protocol.FSDirEntry `json:"entries"`
}

// handleNodeDirs serves GET /api/nodes/{id}/fs?path=...: it proxies one
// directory listing to the node that owns the filesystem. Visibility follows
// the node binding (invisible node -> 404); an offline node is 503 because the
// listing is inherently remote, and a path the node cannot read is 400 with
// the node's message.
//
// An empty path lists the node user's home directory, so the picker has a
// useful starting point without the hub guessing at remote paths.
func (h *Hub) handleNodeDirs(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("id")
	ctx := r.Context()
	if ok, err := h.canReadNode(ctx, userOf(r), nodeID); err != nil || !ok {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}

	reqCtx, cancel := context.WithTimeout(ctx, fsRequestTimeout)
	defer cancel()
	resp, err := h.requestFSList(reqCtx, nodeID, r.URL.Query().Get("path"))
	if err != nil {
		if errors.Is(err, errNodeOffline) {
			writeError(w, http.StatusServiceUnavailable, "node is offline")
			return
		}
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if resp.Error != "" {
		writeError(w, http.StatusBadRequest, resp.Error)
		return
	}
	writeJSON(w, http.StatusOK, dirListing{
		Path:    resp.Path,
		Parent:  resp.Parent,
		Entries: resp.Entries,
	})
}

// errNodeOffline marks a request that could not be handed to the node because
// no live connection exists.
var errNodeOffline = errors.New("node offline")

// requestFSList sends one fs.list_req to a node and waits for the matching
// fs.list_resp. It is the directory-browsing twin of requestSessionHistory.
func (h *Hub) requestFSList(ctx context.Context, nodeID, path string) (protocol.FSListResponse, error) {
	req := protocol.FSListRequest{
		ReqID: fmt.Sprintf("fs-%d", time.Now().UnixNano()),
		Path:  path,
	}
	ch := make(chan protocol.FSListResponse, 1)
	h.fsMu.Lock()
	h.fsWait[req.ReqID] = ch
	h.fsMu.Unlock()
	defer func() {
		h.fsMu.Lock()
		delete(h.fsWait, req.ReqID)
		h.fsMu.Unlock()
	}()

	if !h.sendToNode(nodeID, protocol.NewEnvelope(protocol.MsgFSList, req)) {
		return protocol.FSListResponse{}, fmt.Errorf("%w: %s", errNodeOffline, nodeID)
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		return protocol.FSListResponse{}, fmt.Errorf("directory listing timed out: %w", ctx.Err())
	}
}

// handleFSListResponse routes a node's listing to its waiter. A response
// nobody waits for (already timed out) is dropped, same as history responses.
func (h *Hub) handleFSListResponse(resp protocol.FSListResponse) {
	h.fsMu.Lock()
	ch := h.fsWait[resp.ReqID]
	h.fsMu.Unlock()
	if ch == nil {
		h.log.Debug("dropping fs response with no waiter", "req_id", resp.ReqID)
		return
	}
	select {
	case ch <- resp:
	default:
	}
}
