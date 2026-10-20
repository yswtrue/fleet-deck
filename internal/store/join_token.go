package store

import (
	"context"
	"fmt"
	"time"

	"fleetdeck/internal/store/sqlcgen"
)

// NodeJoinToken is one single-use pairing token. The plaintext token is never
// persisted; TokenHash is the sha256 hex of it.
type NodeJoinToken struct {
	ID         string
	TokenHash  string
	CreatedBy  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	UsedAt     *time.Time
	UsedNodeID *string
}

func nodeJoinTokenFromRow(r sqlcgen.NodeJoinToken) NodeJoinToken {
	return NodeJoinToken{
		ID:         r.ID,
		TokenHash:  r.TokenHash,
		CreatedBy:  r.CreatedBy,
		CreatedAt:  r.CreatedAt,
		ExpiresAt:  r.ExpiresAt,
		UsedAt:     r.UsedAt,
		UsedNodeID: r.UsedNodeID,
	}
}

// CreateNodeJoinToken stores a hashed pairing token that expires at expiresAt.
func (q *Queries) CreateNodeJoinToken(ctx context.Context, tokenHash, createdBy string, expiresAt time.Time) (NodeJoinToken, error) {
	row, err := q.q.CreateNodeJoinToken(ctx, sqlcgen.CreateNodeJoinTokenParams{
		TokenHash: tokenHash,
		CreatedBy: createdBy,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return NodeJoinToken{}, fmt.Errorf("store: create node join token: %w", err)
	}
	return nodeJoinTokenFromRow(row), nil
}

// GetOpenNodeJoinToken locks and returns an unused, unexpired pairing token by
// hash. It must run inside a transaction (WithTx) so the claim is atomic with
// node creation; concurrent claims for the same token serialize on the row.
func (q *Queries) GetOpenNodeJoinToken(ctx context.Context, tokenHash string) (NodeJoinToken, error) {
	row, err := q.q.GetOpenNodeJoinToken(ctx, tokenHash)
	if err != nil {
		return NodeJoinToken{}, fmt.Errorf("store: get open node join token: %w", err)
	}
	return nodeJoinTokenFromRow(row), nil
}

// MarkNodeJoinTokenUsed burns a pairing token and links it to the node it
// registered.
func (q *Queries) MarkNodeJoinTokenUsed(ctx context.Context, id, nodeID string) error {
	if err := q.q.MarkNodeJoinTokenUsed(ctx, sqlcgen.MarkNodeJoinTokenUsedParams{
		ID:         id,
		UsedNodeID: &nodeID,
	}); err != nil {
		return fmt.Errorf("store: mark node join token used: %w", err)
	}
	return nil
}
