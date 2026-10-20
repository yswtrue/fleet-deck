package hub

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"fleetdeck/internal/store"
)

// joinTokenTTL is the default lifetime of a UI-minted pairing token. The
// window only needs to cover copying the command to the node machine and
// starting the daemon.
const joinTokenTTL = 15 * time.Minute

// Pairing-token lifetime bounds accepted from POST /api/nodes/join-tokens.
const (
	minJoinTokenTTL = 60 * time.Second
	maxJoinTokenTTL = time.Hour
)

var (
	// errJoinTokenRequired rejects a first-time registration that presents no
	// pairing credential.
	errJoinTokenRequired = errors.New("hub: register: a join token is required for a new node")
	// errJoinTokenInvalid covers unknown, spent and expired pairing tokens.
	errJoinTokenInvalid = errors.New("hub: register: join token is invalid, expired, or already used")
)

// handleCreateNodeJoinToken serves POST /api/nodes/join-tokens: any signed-in
// user may mint a single-use token for a machine they want to attach. The node
// created with it is bound to the token's creator, so a regular user can add
// their own machine without an admin. The plaintext token is returned exactly
// once; only its sha256 is stored.
func (h *Hub) handleCreateNodeJoinToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TTLSeconds int `json:"ttl_seconds"`
	}
	// The body is optional: an empty POST uses the default TTL.
	if r.ContentLength > 0 {
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	ttl := joinTokenTTL
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
		if ttl < minJoinTokenTTL {
			ttl = minJoinTokenTTL
		}
		if ttl > maxJoinTokenTTL {
			ttl = maxJoinTokenTTL
		}
	}

	token, err := GenerateToken()
	if err != nil {
		h.serverError(w, "generate join token", err)
		return
	}
	row, err := h.store.Queries().CreateNodeJoinToken(
		r.Context(), hashAuthToken(token), userOf(r).ID, time.Now().UTC().Add(ttl),
	)
	if err != nil {
		h.serverError(w, "create join token", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      token,
		"expires_at": row.ExpiresAt,
	})
}

// authorizeJoin validates the pairing credential presented in a registration
// handshake. It returns the token row (with its creator) plus whether the
// credential came from the static FLEETDECK_NODE_JOIN_TOKEN instead of the
// database. Callers must run it inside the transaction that creates the node,
// so a failed insert does not burn a single-use token.
func (h *Hub) authorizeJoin(ctx context.Context, q *store.Queries, presented string) (store.NodeJoinToken, bool, error) {
	if presented == "" {
		return store.NodeJoinToken{}, false, errJoinTokenRequired
	}
	// A deployment-level token (automation, dev stacks) needs no database row
	// and does not bind an owner.
	if h.joinToken != "" &&
		subtle.ConstantTimeCompare([]byte(presented), []byte(h.joinToken)) == 1 {
		return store.NodeJoinToken{}, true, nil
	}
	jt, err := q.GetOpenNodeJoinToken(ctx, hashAuthToken(presented))
	if errors.Is(err, pgx.ErrNoRows) {
		return store.NodeJoinToken{}, false, errJoinTokenInvalid
	}
	if err != nil {
		return store.NodeJoinToken{}, false, err
	}
	return jt, false, nil
}

// linkJoinToken binds the claiming user to the node and burns the single-use
// token. Static env tokens have no row and no owner, so they are a no-op.
func linkJoinToken(ctx context.Context, q *store.Queries, jt store.NodeJoinToken, static bool, nodeID string) error {
	if static {
		return nil
	}
	if err := q.AddNodeMember(ctx, jt.CreatedBy, nodeID); err != nil {
		return err
	}
	return q.MarkNodeJoinTokenUsed(ctx, jt.ID, nodeID)
}
