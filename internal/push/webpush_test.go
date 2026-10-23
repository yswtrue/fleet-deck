package push

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// RFC 8291 §5 example: keys, salt, payload and the resulting aes128gcm body.
const (
	rfc8291Plaintext = "When I grow up, I want to be a watermelon"
	rfc8291UAPublic  = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfc8291ASPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfc8291Auth      = "BTBZMqHH6r4Tts7J_aSIgg"
	rfc8291Salt      = "DGv6ra1nlYgDCS1FRnbzlw"
	rfc8291Record    = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

func mustDecode(t *testing.T, v string) []byte {
	t.Helper()
	out, err := base64.RawURLEncoding.DecodeString(strings.ReplaceAll(v, "\n", ""))
	if err != nil {
		t.Fatalf("decode %q: %v", v, err)
	}
	return out
}

// decryptRecord is the user-agent half of RFC 8291, used to verify what the
// sender produced.
func decryptRecord(t *testing.T, record []byte, uaPriv *ecdh.PrivateKey, authSecret []byte) []byte {
	t.Helper()
	if len(record) < 21 {
		t.Fatalf("record too short: %d", len(record))
	}
	salt := record[:16]
	rs := binary.BigEndian.Uint32(record[16:20])
	if rs != recordSize {
		t.Fatalf("record size = %d, want %d", rs, recordSize)
	}
	idLen := int(record[20])
	if len(record) < 21+idLen {
		t.Fatalf("record truncated: id length %d", idLen)
	}
	asPub, err := ecdh.P256().NewPublicKey(record[21 : 21+idLen])
	if err != nil {
		t.Fatalf("parse ephemeral key: %v", err)
	}
	shared, err := uaPriv.ECDH(asPub)
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}
	cek, nonce, err := deriveKeys(shared, authSecret, uaPriv.PublicKey().Bytes(), asPub.Bytes(), salt)
	if err != nil {
		t.Fatalf("derive keys: %v", err)
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	plaintext, err := gcm.Open(nil, nonce, record[21+idLen:], nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(plaintext) == 0 || plaintext[len(plaintext)-1] != paddingDelimiter {
		t.Fatalf("missing padding delimiter")
	}
	return plaintext[:len(plaintext)-1]
}

// TestRFC8291Vector pins the aes128gcm writer to the RFC's published record.
func TestRFC8291Vector(t *testing.T) {
	uaPub, err := ecdh.P256().NewPublicKey(mustDecode(t, rfc8291UAPublic))
	if err != nil {
		t.Fatal(err)
	}
	eph, err := ecdh.P256().NewPrivateKey(mustDecode(t, rfc8291ASPrivate))
	if err != nil {
		t.Fatal(err)
	}
	record, err := encryptRecord(
		[]byte(rfc8291Plaintext),
		uaPub,
		mustDecode(t, rfc8291Auth),
		eph,
		mustDecode(t, rfc8291Salt),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := mustDecode(t, rfc8291Record)
	if string(record) != string(want) {
		t.Fatalf("record mismatch\n got: %s\nwant: %s",
			base64.RawURLEncoding.EncodeToString(record), rfc8291Record)
	}
}

// TestEncryptRoundTrip checks the randomized path with a fresh UA key pair.
func TestEncryptRoundTrip(t *testing.T) {
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := []byte("0123456789abcdef")
	payload := []byte(`{"type":"task.status_changed"}`)
	record, err := Encrypt(payload,
		base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes()),
		base64.RawURLEncoding.EncodeToString(auth),
	)
	if err != nil {
		t.Fatal(err)
	}
	got := decryptRecord(t, record, uaPriv, auth)
	if string(got) != string(payload) {
		t.Fatalf("payload = %q, want %q", got, payload)
	}
}

func TestEncryptRejectsBadSubscription(t *testing.T) {
	if _, err := Encrypt([]byte("x"), "not-base64!", "AAAAAAAAAAAAAAAAAAAAAA"); err == nil {
		t.Fatal("expected decode error")
	}
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	pub := base64.RawURLEncoding.EncodeToString(ua.PublicKey().Bytes())
	if _, err := Encrypt([]byte("x"), pub, base64.RawURLEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("expected auth length error")
	}
}

// TestSendHeadersAndPayload verifies the HTTP contract: VAPID authorization,
// TTL, content coding, and a body the receiver can decrypt.
func TestSendHeadersAndPayload(t *testing.T) {
	priv, pub, err := GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	sender, err := NewSender(priv, pub, "mailto:ops@example.com")
	if err != nil {
		t.Fatal(err)
	}

	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := []byte("0123456789abcdef")
	payload := []byte(`{"title":"done"}`)

	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if enc := r.Header.Get("Content-Encoding"); enc != "aes128gcm" {
			t.Errorf("content-encoding = %q", enc)
		}
		if ttl := r.Header.Get("TTL"); ttl != "86400" {
			t.Errorf("ttl = %q", ttl)
		}
		authz := r.Header.Get("Authorization")
		if !strings.HasPrefix(authz, "vapid t=") || !strings.Contains(authz, ", k="+sender.PublicKey()) {
			t.Errorf("authorization = %q", authz)
		}
		verifyVAPID(t, authz, &sender.key.PublicKey, "http://"+r.Host, sender.subject)
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer ts.Close()

	status, err := sender.Send(context.Background(), Subscription{
		Endpoint: ts.URL + "/push/abc",
		P256dh:   base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(auth),
	}, payload)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if status != http.StatusCreated {
		t.Fatalf("status = %d", status)
	}
	got := decryptRecord(t, gotBody, uaPriv, auth)
	if string(got) != string(payload) {
		t.Fatalf("payload = %q, want %q", got, payload)
	}
}

// verifyVAPID parses the header and checks the ES256 signature + claims.
func verifyVAPID(t *testing.T, header string, pub *ecdsa.PublicKey, audience, subject string) {
	t.Helper()
	_, rest, ok := strings.Cut(header, "vapid t=")
	if !ok {
		t.Fatalf("malformed authorization %q", header)
	}
	jwt, _, _ := strings.Cut(rest, ",")
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt parts = %d", len(parts))
	}
	signingInput := parts[0] + "." + parts[1]
	digest := sha256.Sum256([]byte(signingInput))
	sig := mustDecode(t, parts[2])
	if len(sig) != 64 {
		t.Fatalf("signature length = %d", len(sig))
	}
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		t.Fatal("VAPID signature does not verify")
	}
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(mustDecode(t, parts[1]), &claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	if claims.Aud != audience {
		t.Errorf("aud = %q, want %q", claims.Aud, audience)
	}
	if claims.Sub != subject {
		t.Errorf("sub = %q, want %q", claims.Sub, subject)
	}
	if claims.Exp == 0 {
		t.Error("missing exp")
	}
}

// TestNewSenderRejectsMismatchedKeys guards the persisted-key load path.
func TestNewSenderRejectsMismatchedKeys(t *testing.T) {
	priv, _, err := GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	_, otherPub, err := GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSender(priv, otherPub, ""); err == nil {
		t.Fatal("expected key mismatch error")
	}
}
