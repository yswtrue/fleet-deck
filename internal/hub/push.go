package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"fleetdeck/internal/protocol"
	"fleetdeck/internal/push"
	"fleetdeck/internal/store"
)

// Push delivery tuning. Status changes are low-volume; the queue absorbs a
// burst and the semaphore bounds concurrent outbound requests so one slow push
// service cannot stall the worker.
const (
	pushQueueDepth    = 64
	pushConcurrency   = 8
	pushSendTimeout   = 15 * time.Second
	pushLookupTimeout = 10 * time.Second
	// maxPushEndpoint / maxPushKey bound untrusted subscription input.
	maxPushEndpoint = 2048
	maxPushKey      = 512
)

// pushPayload is the JSON body the service worker receives. Copy is localized
// in the worker (which knows the subscription's language); the hub sends only
// structured data. URL is an in-app route for notification clicks.
type pushPayload struct {
	Type   string `json:"type"`
	TaskID string `json:"task_id"`
	Title  string `json:"title"`
	From   string `json:"from"`
	To     string `json:"to"`
	URL    string `json:"url"`
	Lang   string `json:"lang,omitempty"`
}

// initPush loads (or creates) the VAPID key, starts the delivery worker, and
// subscribes to the broadcast stream. Any failure disables push notifications
// with a warning: the hub must still serve the WebSocket/UI paths.
func (h *Hub) initPush(ctx context.Context) {
	candidatePriv, candidatePub, err := push.GenerateKeys()
	if err != nil {
		h.log.Warn("push notifications disabled", "err", err)
		return
	}
	priv, pub, err := h.store.Queries().LoadOrCreateVAPIDKey(ctx, candidatePriv, candidatePub)
	if err != nil {
		h.log.Warn("push notifications disabled: VAPID key", "err", err)
		return
	}
	sender, err := push.NewSender(priv, pub, os.Getenv("FLEETDECK_VAPID_SUBJECT"))
	if err != nil {
		h.log.Warn("push notifications disabled: VAPID key invalid", "err", err)
		return
	}
	h.pushSender = sender
	h.pushQueue = make(chan protocol.Event, pushQueueDepth)
	h.pushStop = make(chan struct{})
	h.pushSem = make(chan struct{}, pushConcurrency)

	// The listener only enqueues; dropping when the queue is full is
	// deliberate (push is best-effort and must never back-pressure events).
	h.bus.AddListener(func(evt protocol.Event) {
		if evt.Type != protocol.EvTaskStatusChanged {
			return
		}
		select {
		case h.pushQueue <- evt:
		default:
			h.log.Debug("push queue full; dropping task status event")
		}
	})
	go h.pushWorker()
}

func (h *Hub) pushWorker() {
	for {
		select {
		case <-h.pushStop:
			return
		case evt := <-h.pushQueue:
			h.deliverTaskStatus(evt)
		}
	}
}

// deliverTaskStatus fans one task.status_changed event out to every
// subscription allowed to see the task's node.
func (h *Hub) deliverTaskStatus(evt protocol.Event) {
	if h.pushSender == nil {
		return
	}
	var d protocol.TaskStatusChanged
	if !decodeEventPayload(evt.Data, &d) || d.TaskID == "" || d.From == d.To {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), pushLookupTimeout)
	defer cancel()

	task, err := h.store.Queries().GetTask(ctx, d.TaskID)
	if err != nil {
		h.log.Debug("push: task disappeared", "task_id", d.TaskID, "err", err)
		return
	}
	nodeID := h.workspaceNodeID(ctx, task.WorkspaceID)
	if nodeID == "" {
		return
	}
	subs, err := h.store.Queries().ListPushSubscriptionsForNode(ctx, nodeID)
	if err != nil {
		h.log.Warn("push: list subscriptions", "node_id", nodeID, "err", err)
		return
	}
	if len(subs) == 0 {
		return
	}

	title := strings.TrimSpace(task.Title)
	if title == "" {
		title = d.TaskID[:min(8, len(d.TaskID))]
	}
	target := "/workspaces/" + url.PathEscape(task.WorkspaceID)
	if d.To == protocol.TaskAwaitingUser && task.SessionID != "" {
		target = "/sessions/" + url.PathEscape(task.SessionID)
	}

	for _, sub := range subs {
		payload, err := json.Marshal(pushPayload{
			Type:   protocol.EvTaskStatusChanged,
			TaskID: d.TaskID,
			Title:  title,
			From:   string(d.From),
			To:     string(d.To),
			URL:    target,
			Lang:   sub.Lang,
		})
		if err != nil {
			continue
		}
		h.sendPush(sub, payload)
	}
}

// sendPush dispatches one delivery without blocking the worker. A full
// semaphore drops the message rather than queueing unbounded goroutines.
func (h *Hub) sendPush(sub store.PushSubscription, payload []byte) {
	select {
	case h.pushSem <- struct{}{}:
	default:
		h.log.Debug("push: delivery limit reached; dropping")
		return
	}
	go func() {
		defer func() { <-h.pushSem }()
		ctx, cancel := context.WithTimeout(context.Background(), pushSendTimeout)
		defer cancel()
		status, err := h.pushSender.Send(ctx, push.Subscription{
			Endpoint: sub.Endpoint,
			P256dh:   sub.P256dh,
			Auth:     sub.Auth,
		}, payload)
		if err != nil {
			if status == http.StatusNotFound || status == http.StatusGone {
				// The browser dropped (or rotated) the subscription: prune it.
				if derr := h.store.Queries().DeletePushSubscriptionByEndpoint(ctx, sub.Endpoint); derr != nil {
					h.log.Warn("push: prune subscription", "err", derr)
				}
			}
			h.log.Warn("push: delivery failed",
				"endpoint_host", endpointHost(sub.Endpoint), "status", status, "err", err)
		}
	}()
}

// handlePushPublicKey serves GET /api/push/public-key: the VAPID
// applicationServerKey the browser passes to PushManager.subscribe.
func (h *Hub) handlePushPublicKey(w http.ResponseWriter, _ *http.Request) {
	if h.pushSender == nil {
		writeError(w, http.StatusServiceUnavailable, "push notifications are not available")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"public_key": h.pushSender.PublicKey()})
}

type pushSubscriptionRequest struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
	Lang     string `json:"lang"`
}

// handleCreatePushSubscription serves POST /api/push/subscriptions. The
// endpoint is unique across users, so a browser that signs in as someone else
// and enables notifications is re-bound to the new account.
func (h *Hub) handleCreatePushSubscription(w http.ResponseWriter, r *http.Request) {
	var req pushSubscriptionRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	req.P256dh = strings.TrimSpace(req.P256dh)
	req.Auth = strings.TrimSpace(req.Auth)
	if req.Endpoint == "" || req.P256dh == "" || req.Auth == "" {
		writeError(w, http.StatusBadRequest, "endpoint, p256dh and auth are required")
		return
	}
	if len(req.Endpoint) > maxPushEndpoint || len(req.P256dh) > maxPushKey || len(req.Auth) > maxPushKey {
		writeError(w, http.StatusBadRequest, "push subscription fields are too long")
		return
	}
	if !validPushEndpoint(req.Endpoint) {
		writeError(w, http.StatusBadRequest, "endpoint must be an http(s) URL")
		return
	}
	lang := strings.TrimSpace(req.Lang)
	if len(lang) > 16 {
		lang = lang[:16]
	}
	if err := h.store.Queries().UpsertPushSubscription(r.Context(), userOf(r).ID, store.PushSubscription{
		Endpoint: req.Endpoint,
		P256dh:   req.P256dh,
		Auth:     req.Auth,
		Lang:     lang,
	}); err != nil {
		h.serverError(w, "save push subscription", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeletePushSubscription serves DELETE /api/push/subscriptions?endpoint=.
// Scoped to the caller so one account cannot remove another's registration.
func (h *Hub) handleDeletePushSubscription(w http.ResponseWriter, r *http.Request) {
	endpoint := strings.TrimSpace(r.URL.Query().Get("endpoint"))
	if endpoint == "" {
		writeError(w, http.StatusBadRequest, "endpoint is required")
		return
	}
	if err := h.store.Queries().DeletePushSubscription(r.Context(), userOf(r).ID, endpoint); err != nil {
		h.serverError(w, "delete push subscription", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validPushEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	return (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// endpointHost keeps capability URLs (which are bearer secrets) out of logs.
func endpointHost(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "invalid"
	}
	return u.Host
}
