-- +goose Up
-- push_subscription: one browser PushManager subscription per row. Endpoints
-- are globally unique (a browser has exactly one subscription per service
-- worker scope) and are reassigned to the signed-in user that last registered
-- them. Keys are the subscription's receiver public key (uncompressed P-256)
-- and auth secret, both base64url.
CREATE TABLE push_subscription (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    endpoint     TEXT        NOT NULL,
    p256dh       TEXT        NOT NULL,
    auth         TEXT        NOT NULL,
    lang         TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT push_subscription_endpoint_unique UNIQUE (endpoint)
);

CREATE INDEX push_subscription_user_id_idx ON push_subscription (user_id);

-- push_vapid_key: the hub's single VAPID (RFC 8292) signing key. Generated on
-- first start and shared by every subscription request; rotating it invalidates
-- all existing subscriptions.
CREATE TABLE push_vapid_key (
    id          BOOLEAN     PRIMARY KEY DEFAULT TRUE,
    private_key BYTEA       NOT NULL,
    public_key  BYTEA       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT push_vapid_key_singleton CHECK (id)
);

-- +goose Down
DROP TABLE push_vapid_key;
DROP TABLE push_subscription;
