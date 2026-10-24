package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"fleetdeck/internal/protocol"
	"fleetdeck/internal/store/sqlcgen"
)

// CreateNodeParams are the fields required to register a new node. LastSeenAt
// is set to now() by the database.
type CreateNodeParams struct {
	Name       string
	Host       string
	Status     protocol.NodeStatus
	AgentTypes []string
	// DefaultAdapter is the node's configured default agent; empty allowed.
	DefaultAdapter string
	TokenHash      string
	// MachineID is the stable machine fingerprint (already hashed by the
	// caller). Empty stores NULL, which the partial unique index ignores.
	MachineID string
}

// CreateNode inserts a node and returns the stored row.
func (q *Queries) CreateNode(ctx context.Context, p CreateNodeParams) (protocol.Node, error) {
	agentTypes := p.AgentTypes
	if agentTypes == nil {
		agentTypes = []string{}
	}
	row, err := q.q.CreateNode(ctx, sqlcgen.CreateNodeParams{
		Name:           p.Name,
		Host:           p.Host,
		Status:         string(p.Status),
		AgentTypes:     agentTypes,
		DefaultAdapter: p.DefaultAdapter,
		TokenHash:      p.TokenHash,
		MachineID:      pgtype.Text{String: p.MachineID, Valid: p.MachineID != ""},
	})
	if err != nil {
		return protocol.Node{}, fmt.Errorf("store: create node: %w", err)
	}
	return nodeFromRow(row), nil
}

// GetNode returns a node by id.
func (q *Queries) GetNode(ctx context.Context, id string) (protocol.Node, error) {
	row, err := q.q.GetNode(ctx, id)
	if err != nil {
		return protocol.Node{}, fmt.Errorf("store: get node: %w", err)
	}
	return nodeFromRow(row), nil
}

// GetNodeByName returns a node by its unique-ish display name.
func (q *Queries) GetNodeByName(ctx context.Context, name string) (protocol.Node, error) {
	row, err := q.q.GetNodeByName(ctx, name)
	if err != nil {
		return protocol.Node{}, fmt.Errorf("store: get node by name: %w", err)
	}
	return nodeFromRow(row), nil
}

// GetNodeByMachineID resolves the node that claimed a machine fingerprint.
func (q *Queries) GetNodeByMachineID(ctx context.Context, machineID string) (protocol.Node, error) {
	row, err := q.q.GetNodeByMachineID(ctx, pgtype.Text{String: machineID, Valid: true})
	if err != nil {
		return protocol.Node{}, fmt.Errorf("store: get node by machine id: %w", err)
	}
	return nodeFromRow(row), nil
}

// GetNodeMachineID returns a node's claimed machine fingerprint, or "" when
// it has not claimed one yet.
func (q *Queries) GetNodeMachineID(ctx context.Context, id string) (string, error) {
	raw, err := q.q.GetNodeMachineID(ctx, id)
	if err != nil {
		return "", fmt.Errorf("store: get node machine id: %w", err)
	}
	if !raw.Valid {
		return "", nil
	}
	return raw.String, nil
}

// UpdateNodeMachineID claims a machine fingerprint for a node.
func (q *Queries) UpdateNodeMachineID(ctx context.Context, id, machineID string) error {
	if err := q.q.UpdateNodeMachineID(ctx, sqlcgen.UpdateNodeMachineIDParams{
		ID:        id,
		MachineID: pgtype.Text{String: machineID, Valid: true},
	}); err != nil {
		return fmt.Errorf("store: update node machine id: %w", err)
	}
	return nil
}

// ListNodes returns all nodes ordered by name.
func (q *Queries) ListNodes(ctx context.Context) ([]protocol.Node, error) {
	rows, err := q.q.ListNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: list nodes: %w", err)
	}
	return nodesFromRows(rows), nil
}

// UpdateNodeAgents replaces a node's registered adapter list and default.
func (q *Queries) UpdateNodeAgents(ctx context.Context, id string, agentTypes []string, defaultAdapter string) error {
	if agentTypes == nil {
		agentTypes = []string{}
	}
	if err := q.q.UpdateNodeAgents(ctx, sqlcgen.UpdateNodeAgentsParams{
		ID:             id,
		AgentTypes:     agentTypes,
		DefaultAdapter: defaultAdapter,
	}); err != nil {
		return fmt.Errorf("store: update node agents: %w", err)
	}
	return nil
}

// UpdateNodeStatus sets a node's connectivity status and last-seen timestamp.
func (q *Queries) UpdateNodeStatus(ctx context.Context, id string, status protocol.NodeStatus, lastSeenAt time.Time) error {
	err := q.q.UpdateNodeStatus(ctx, sqlcgen.UpdateNodeStatusParams{
		ID:         id,
		Status:     string(status),
		LastSeenAt: lastSeenAt,
	})
	if err != nil {
		return fmt.Errorf("store: update node status: %w", err)
	}
	return nil
}

// MarkNodesOffline flips every non-offline node row to offline. The hub calls
// it at startup: a process that died while nodes were connected never gets
// their disconnect frames, so the persisted status would otherwise claim those
// nodes are online forever. It deliberately does not touch sessions — a node
// that reconnects keeps its live adapters.
func (q *Queries) MarkNodesOffline(ctx context.Context) error {
	if err := q.q.MarkNodesOffline(ctx); err != nil {
		return fmt.Errorf("store: mark nodes offline: %w", err)
	}
	return nil
}

// UpdateNodeTokenHash rotates a node's stored auth token hash.
func (q *Queries) UpdateNodeTokenHash(ctx context.Context, id, hash string) error {
	err := q.q.UpdateNodeTokenHash(ctx, sqlcgen.UpdateNodeTokenHashParams{
		ID:        id,
		TokenHash: hash,
	})
	if err != nil {
		return fmt.Errorf("store: update node token hash: %w", err)
	}
	return nil
}

// GetNodeByTokenHash resolves the node presenting the given auth token hash.
//
// NOTE: This is not the node auth path. node.token_hash stores a salted bcrypt
// digest, so equal tokens never produce equal hashes and lookup by hash cannot
// authenticate; registration compares against the hash of a known node instead.
// Kept for tests and future token revocation.
func (q *Queries) GetNodeByTokenHash(ctx context.Context, hash string) (protocol.Node, error) {
	row, err := q.q.GetNodeByTokenHash(ctx, hash)
	if err != nil {
		return protocol.Node{}, fmt.Errorf("store: get node by token hash: %w", err)
	}
	return nodeFromRow(row), nil
}
