package store

import (
	"context"
	"fmt"
	"time"

	"fleetdeck/internal/protocol"
	"fleetdeck/internal/store/sqlcgen"
)

// InsertTokenUsageParams are the fields of one reported turn's usage. The
// cache counters are nil when the agent reported neither category.
type InsertTokenUsageParams struct {
	SessionID         string
	TaskID            string
	Model             string
	PromptTokens      int64
	CompletionTokens  int64
	TotalTokens       int64
	CachedReadTokens  *int64
	CachedWriteTokens *int64
}

// InsertTokenUsage records one reported turn's usage and returns the row.
func (q *Queries) InsertTokenUsage(ctx context.Context, p InsertTokenUsageParams) (TokenUsage, error) {
	row, err := q.q.InsertTokenUsage(ctx, sqlcgen.InsertTokenUsageParams{
		SessionID:         p.SessionID,
		TaskID:            p.TaskID,
		Model:             p.Model,
		PromptTokens:      p.PromptTokens,
		CompletionTokens:  p.CompletionTokens,
		TotalTokens:       p.TotalTokens,
		CachedReadTokens:  p.CachedReadTokens,
		CachedWriteTokens: p.CachedWriteTokens,
	})
	if err != nil {
		return TokenUsage{}, fmt.Errorf("store: insert token usage: %w", err)
	}
	return tokenUsageFromRow(row), nil
}

// ListTokenUsageBySession returns a session's usage rows, oldest first.
func (q *Queries) ListTokenUsageBySession(ctx context.Context, sessionID string) ([]TokenUsage, error) {
	rows, err := q.q.ListTokenUsageBySession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: list token usage by session: %w", err)
	}
	return tokenUsagesFromRows(rows), nil
}

// TokenUsageFilter narrows AggregateTokenUsage. Empty ids and nil times mean
// "no restriction"; MemberUserID, when set, keeps only rows whose task belongs
// to a workspace on a node the user is bound to (non-admin visibility).
type TokenUsageFilter struct {
	SessionID    string
	WorkspaceID  string
	NodeID       string
	From         *time.Time
	To           *time.Time
	MemberUserID string
}

// AggregateTokenUsage sums usage per model over the rows matching filter,
// sorted by total tokens descending (model name breaks ties). The caller sums
// the returned rows for the cross-model total.
func (q *Queries) AggregateTokenUsage(ctx context.Context, f TokenUsageFilter) ([]protocol.TokenUsageByModel, error) {
	params := sqlcgen.AggregateTokenUsageParams{
		From: f.From,
		To:   f.To,
	}
	if f.SessionID != "" {
		params.SessionID = &f.SessionID
	}
	if f.WorkspaceID != "" {
		params.WorkspaceID = &f.WorkspaceID
	}
	if f.NodeID != "" {
		params.NodeID = &f.NodeID
	}
	if f.MemberUserID != "" {
		params.MemberUserID = &f.MemberUserID
	}
	rows, err := q.q.AggregateTokenUsage(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("store: aggregate token usage: %w", err)
	}
	out := make([]protocol.TokenUsageByModel, 0, len(rows))
	for _, r := range rows {
		out = append(out, protocol.TokenUsageByModel{
			Model:             r.Model,
			PromptTokens:      r.PromptTokens,
			CompletionTokens:  r.CompletionTokens,
			TotalTokens:       r.TotalTokens,
			CachedReadTokens:  r.CachedReadTokens,
			CachedWriteTokens: r.CachedWriteTokens,
			CachePromptTokens: r.CachePromptTokens,
			CacheReports:      r.CacheReports,
			Turns:             r.Turns,
		})
	}
	return out, nil
}
