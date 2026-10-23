package hub

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"fleetdeck/internal/protocol"
)

// fakePushEndpoint receives push POSTs and can decrypt the aes128gcm body with
// the receiver key it registered.
type fakePushEndpoint struct {
	srv    *httptest.Server
	p256dh string
	auth   []byte
	uaPriv *ecdh.PrivateKey
	status atomic.Int32
	bodies chan []byte

	encoding atomic.Value // string
	ttl      atomic.Value // string
	authz    atomic.Value // string
}

func newFakePushEndpoint(t *testing.T, status int) *fakePushEndpoint {
	t.Helper()
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate receiver key: %v", err)
	}
	auth := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, auth); err != nil {
		t.Fatalf("auth secret: %v", err)
	}
	f := &fakePushEndpoint{
		p256dh: base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes()),
		auth:   auth,
		uaPriv: uaPriv,
		bodies: make(chan []byte, 8),
	}
	f.status.Store(int32(status))
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.encoding.Store(r.Header.Get("Content-Encoding"))
		f.ttl.Store(r.Header.Get("TTL"))
		f.authz.Store(r.Header.Get("Authorization"))
		select {
		case f.bodies <- body:
		default:
		}
		w.WriteHeader(int(f.status.Load()))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePushEndpoint) endpoint() string { return f.srv.URL + "/push/test" }

func (f *fakePushEndpoint) register(t *testing.T, token string, ts *httptest.Server, lang string) {
	t.Helper()
	resp, raw := postRaw(t, token, ts.URL+"/api/push/subscriptions", map[string]string{
		"endpoint": f.endpoint(),
		"p256dh":   f.p256dh,
		"auth":     base64.RawURLEncoding.EncodeToString(f.auth),
		"lang":     lang,
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("register subscription: status %d: %s", resp.StatusCode, raw)
	}
}

// decryptPush decodes the RFC 8291 body using the registered receiver key.
func (f *fakePushEndpoint) decryptPush(t *testing.T, record []byte) []byte {
	t.Helper()
	if len(record) < 21 {
		t.Fatalf("push body too short: %d", len(record))
	}
	salt := record[:16]
	rs := binary.BigEndian.Uint32(record[16:20])
	if rs != 4096 {
		t.Fatalf("record size = %d", rs)
	}
	idLen := int(record[20])
	asPub, err := ecdh.P256().NewPublicKey(record[21 : 21+idLen])
	if err != nil {
		t.Fatalf("ephemeral key: %v", err)
	}
	shared, err := f.uaPriv.ECDH(asPub)
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}
	prk, err := hkdf.Extract(sha256.New, shared, f.auth)
	if err != nil {
		t.Fatal(err)
	}
	info := "WebPush: info\x00" + string(f.uaPriv.PublicKey().Bytes()) + string(asPub.Bytes())
	ikm, err := hkdf.Expand(sha256.New, prk, info, 32)
	if err != nil {
		t.Fatal(err)
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := gcm.Open(nil, nonce, record[21+idLen:], nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if len(plaintext) == 0 || plaintext[len(plaintext)-1] != 0x02 {
		t.Fatalf("missing padding delimiter")
	}
	return plaintext[:len(plaintext)-1]
}

func (f *fakePushEndpoint) waitPayload(t *testing.T, timeout time.Duration) []byte {
	t.Helper()
	select {
	case body := <-f.bodies:
		return f.decryptPush(t, body)
	case <-time.After(timeout):
		t.Fatal("timed out waiting for push delivery")
		return nil
	}
}

func (f *fakePushEndpoint) assertNoDelivery(t *testing.T, wait time.Duration) {
	t.Helper()
	select {
	case <-f.bodies:
		t.Fatal("unexpected push delivery")
	case <-time.After(wait):
	}
}

// TestPushTaskStatusDelivery is the end-to-end contract: a registered browser
// subscription receives an encrypted, node-scoped notification when a task
// status changes, and an unbound user's subscription receives nothing.
func TestPushTaskStatusDelivery(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}
	h, st, ts, auth := newTestHub(t)
	if h.pushSender == nil {
		t.Fatal("push sender not initialized")
	}

	// The VAPID public key endpoint feeds PushManager.subscribe.
	resp, raw := getRaw(t, auth.token, ts.URL+"/api/push/public-key")
	keyResp := decodeOK[struct {
		PublicKey string `json:"public_key"`
	}](t, "GET /api/push/public-key", resp, raw)
	if keyResp.PublicKey == "" {
		t.Fatal("empty VAPID public key")
	}
	if resp, _ := postRaw(t, auth.token, ts.URL+"/api/push/subscriptions", map[string]string{
		"endpoint": "not-a-url",
		"p256dh":   "x",
		"auth":     "y",
	}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid endpoint status = %d, want 400", resp.StatusCode)
	}

	ctx := context.Background()
	node := seedNode(t, st, uniqueName("push-node"))

	boundUser, boundToken := setupUserWithRole(t, st, "user")
	if err := st.Queries().AddNodeMember(ctx, boundUser.ID, node.ID); err != nil {
		t.Fatalf("bind user: %v", err)
	}
	_, unboundToken := setupUserWithRole(t, st, "user")

	adminEP := newFakePushEndpoint(t, http.StatusCreated)
	boundEP := newFakePushEndpoint(t, http.StatusCreated)
	unboundEP := newFakePushEndpoint(t, http.StatusCreated)
	adminEP.register(t, auth.token, ts, "en")
	boundEP.register(t, boundToken, ts, "zh-CN")
	unboundEP.register(t, unboundToken, ts, "en")

	task := createWorkspaceAndTask(t, auth.token, ts, node.ID)
	fresh, err := st.Queries().GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if fresh.Status != protocol.TaskWaiting {
		t.Fatalf("offline-node task status = %q, want waiting", fresh.Status)
	}
	h.moveTaskTo(ctx, fresh, protocol.TaskRunning)

	for _, ep := range []*fakePushEndpoint{adminEP, boundEP} {
		payload := ep.waitPayload(t, 5*time.Second)
		var msg pushPayload
		if err := json.Unmarshal(payload, &msg); err != nil {
			t.Fatalf("payload JSON: %v (%s)", err, payload)
		}
		if msg.Type != protocol.EvTaskStatusChanged || msg.To != string(protocol.TaskRunning) {
			t.Fatalf("payload = %+v", msg)
		}
		if msg.Title != task.Title {
			t.Fatalf("title = %q, want %q", msg.Title, task.Title)
		}
		if msg.URL != "/workspaces/"+task.WorkspaceID {
			t.Fatalf("url = %q", msg.URL)
		}
	}
	unboundEP.assertNoDelivery(t, 300*time.Millisecond)

	// Wire contract assertions from the delivered body.
	if enc, _ := boundEP.encoding.Load().(string); enc != "aes128gcm" {
		t.Fatalf("content-encoding = %q", enc)
	}
	if ttl, _ := boundEP.ttl.Load().(string); ttl != "86400" {
		t.Fatalf("ttl = %q", ttl)
	}
	if authz, _ := boundEP.authz.Load().(string); authz == "" {
		t.Fatal("missing VAPID authorization header")
	}

	// A gone endpoint (410) is pruned so future status changes stay cheap.
	boundEP.status.Store(http.StatusGone)
	fresh, err = st.Queries().GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	h.moveTaskTo(ctx, fresh, protocol.TaskDone)
	boundEP.waitPayload(t, 5*time.Second)

	deadline := time.Now().Add(3 * time.Second)
	for {
		var count int
		if err := st.Pool().QueryRow(ctx,
			"SELECT count(*) FROM push_subscription WHERE endpoint = $1", boundEP.endpoint(),
		).Scan(&count); err != nil {
			t.Fatalf("count subscriptions: %v", err)
		}
		if count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("410 subscription was not pruned")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestPushSubscriptionIsUserScoped covers DELETE authorization: only the
// owning user can remove a registration.
func TestPushSubscriptionIsUserScoped(t *testing.T) {
	if testing.Short() {
		t.Skip("hub integration test needs a live Postgres; skipped under -short")
	}
	_, st, ts, auth := newTestHub(t)
	_, otherToken := setupUserWithRole(t, st, "user")

	ep := newFakePushEndpoint(t, http.StatusCreated)
	ep.register(t, auth.token, ts, "en")

	count := func() int {
		t.Helper()
		var n int
		if err := st.Pool().QueryRow(context.Background(),
			"SELECT count(*) FROM push_subscription WHERE endpoint = $1", ep.endpoint(),
		).Scan(&n); err != nil {
			t.Fatalf("count subscriptions: %v", err)
		}
		return n
	}

	resp, _ := deleteReq(t, otherToken, ts.URL+"/api/push/subscriptions?endpoint="+ep.endpoint())
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
	if got := count(); got != 1 {
		t.Fatalf("non-owner delete removed the row (count %d)", got)
	}

	resp, _ = deleteReq(t, auth.token, ts.URL+"/api/push/subscriptions?endpoint="+ep.endpoint())
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("owner delete status = %d", resp.StatusCode)
	}
	if got := count(); got != 0 {
		t.Fatalf("owner delete left %d rows", got)
	}
}
