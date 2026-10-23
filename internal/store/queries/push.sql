-- name: UpsertPushSubscription :exec
INSERT INTO push_subscription (user_id, endpoint, p256dh, auth, lang)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (endpoint) DO UPDATE
SET user_id      = EXCLUDED.user_id,
    p256dh       = EXCLUDED.p256dh,
    auth         = EXCLUDED.auth,
    lang         = EXCLUDED.lang,
    last_seen_at = now();

-- name: DeletePushSubscription :exec
DELETE FROM push_subscription
WHERE user_id = $1 AND endpoint = $2;

-- name: DeletePushSubscriptionByEndpoint :exec
DELETE FROM push_subscription
WHERE endpoint = $1;

-- name: ListPushSubscriptionsForNode :many
-- Recipients are admins (who see every node) plus users bound to node_id; the
-- push payload describes a task, so visibility follows the same subtree rule
-- as /ws/web scoping.
SELECT ps.* FROM push_subscription ps
JOIN users u ON u.id = ps.user_id
WHERE u.role = 'admin'
   OR EXISTS (
       SELECT 1 FROM node_members nm
       WHERE nm.user_id = ps.user_id AND nm.node_id = $1
   )
ORDER BY ps.created_at;

-- name: LoadVAPIDKey :one
SELECT * FROM push_vapid_key WHERE id;

-- name: CreateVAPIDKey :exec
INSERT INTO push_vapid_key (id, private_key, public_key)
VALUES (TRUE, $1, $2)
ON CONFLICT (id) DO NOTHING;
