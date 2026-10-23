package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"fleetdeck/internal/store/sqlcgen"
)

// PushSubscription is one browser Web Push subscription. The keys are the
// base64url values the browser's PushManager reports; Lang is the UI language
// to use for the notification copy.
type PushSubscription struct {
	Endpoint string
	P256dh   string
	Auth     string
	Lang     string
}

// UpsertPushSubscription registers (or re-owns) a browser subscription. The
// endpoint is globally unique: a browser holds one subscription per service
// worker scope, so re-registering after a user switch rebinds it.
func (q *Queries) UpsertPushSubscription(ctx context.Context, userID string, s PushSubscription) error {
	if err := q.q.UpsertPushSubscription(ctx, sqlcgen.UpsertPushSubscriptionParams{
		UserID:   userID,
		Endpoint: s.Endpoint,
		P256dh:   s.P256dh,
		Auth:     s.Auth,
		Lang:     s.Lang,
	}); err != nil {
		return fmt.Errorf("store: upsert push subscription: %w", err)
	}
	return nil
}

// DeletePushSubscription removes one user's subscription by endpoint. Scoping
// by user prevents one account from deleting another's registration.
func (q *Queries) DeletePushSubscription(ctx context.Context, userID, endpoint string) error {
	if err := q.q.DeletePushSubscription(ctx, sqlcgen.DeletePushSubscriptionParams{
		UserID:   userID,
		Endpoint: endpoint,
	}); err != nil {
		return fmt.Errorf("store: delete push subscription: %w", err)
	}
	return nil
}

// DeletePushSubscriptionByEndpoint removes a subscription that the push
// service reported gone (404/410).
func (q *Queries) DeletePushSubscriptionByEndpoint(ctx context.Context, endpoint string) error {
	if err := q.q.DeletePushSubscriptionByEndpoint(ctx, endpoint); err != nil {
		return fmt.Errorf("store: delete push subscription by endpoint: %w", err)
	}
	return nil
}

// ListPushSubscriptionsForNode returns every subscription that may receive a
// notification about nodeID's subtree: admins plus node-bound users.
func (q *Queries) ListPushSubscriptionsForNode(ctx context.Context, nodeID string) ([]PushSubscription, error) {
	rows, err := q.q.ListPushSubscriptionsForNode(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("store: list push subscriptions: %w", err)
	}
	out := make([]PushSubscription, 0, len(rows))
	for _, r := range rows {
		out = append(out, PushSubscription{
			Endpoint: r.Endpoint,
			P256dh:   r.P256dh,
			Auth:     r.Auth,
			Lang:     r.Lang,
		})
	}
	return out, nil
}

// LoadOrCreateVAPIDKey returns the hub's VAPID signing key, inserting the
// supplied candidate keys when the table is empty. Concurrent callers either
// win the insert or read the winner's row, so every hub instance uses one key.
func (q *Queries) LoadOrCreateVAPIDKey(ctx context.Context, privateKey, publicKey []byte) (priv []byte, pub []byte, err error) {
	if err := q.q.CreateVAPIDKey(ctx, sqlcgen.CreateVAPIDKeyParams{
		PrivateKey: privateKey,
		PublicKey:  publicKey,
	}); err != nil {
		return nil, nil, fmt.Errorf("store: create VAPID key: %w", err)
	}
	row, err := q.q.LoadVAPIDKey(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, errors.New("store: VAPID key missing after insert")
		}
		return nil, nil, fmt.Errorf("store: load VAPID key: %w", err)
	}
	return row.PrivateKey, row.PublicKey, nil
}
