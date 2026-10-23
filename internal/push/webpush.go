// Package push implements the application-server half of Web Push: VAPID
// request authorization (RFC 8292) and aes128gcm payload encryption
// (RFC 8188 / RFC 8291). It has no database or hub dependency so the wire
// format can be tested against the RFC vectors.
package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// recordSize is the aes128gcm "rs" header field. Push messages are a
	// single record, so this only needs to exceed payload+padding+tag (4096
	// is the value the RFC examples use and every push service accepts).
	recordSize = 4096
	// pushTTL asks the push service to retain the message for a day when the
	// device is offline.
	pushTTL = 24 * time.Hour
	// vapidTTL bounds the VAPID JWT lifetime (RFC 8292 caps it at 24h).
	vapidTTL = 12 * time.Hour
	// paddingDelimiter terminates the final (and only) aes128gcm record.
	paddingDelimiter = 0x02
)

// Subscription is one browser push subscription, as reported by
// PushSubscription.toJSON(): the endpoint plus the two base64url keys.
type Subscription struct {
	Endpoint string
	P256dh   string
	Auth     string
}

// Sender encrypts and delivers push messages with one VAPID key pair.
type Sender struct {
	key       *ecdsa.PrivateKey
	publicKey string // base64url uncompressed P-256 point
	subject   string
	client    *http.Client
	now       func() time.Time
}

// GenerateKeys returns a fresh VAPID key pair: the 32-byte private scalar and
// the 65-byte uncompressed public point.
func GenerateKeys() (privateKey, publicKey []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("push: generate VAPID key: %w", err)
	}
	privateKey = make([]byte, 32)
	key.D.FillBytes(privateKey)
	publicKey = elliptic.Marshal(elliptic.P256(), key.X, key.Y)
	return privateKey, publicKey, nil
}

// NewSender rebuilds a Sender from stored key material and validates that the
// public point belongs to the private scalar. subject must be a mailto: or
// https: URL (RFC 8292 "sub" claim); empty falls back to a local value.
func NewSender(privateKey, publicKey []byte, subject string) (*Sender, error) {
	if len(privateKey) != 32 {
		return nil, fmt.Errorf("push: VAPID private key must be 32 bytes, got %d", len(privateKey))
	}
	if len(publicKey) != 65 || publicKey[0] != 0x04 {
		return nil, errors.New("push: VAPID public key must be a 65-byte uncompressed P-256 point")
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), publicKey)
	if x == nil {
		return nil, errors.New("push: VAPID public key is not on P-256")
	}
	d := new(big.Int).SetBytes(privateKey)
	if d.Sign() == 0 || d.Cmp(elliptic.P256().Params().N) >= 0 {
		return nil, errors.New("push: VAPID private scalar out of range")
	}
	ex, ey := elliptic.P256().ScalarBaseMult(privateKey)
	if ex.Cmp(x) != 0 || ey.Cmp(y) != 0 {
		return nil, errors.New("push: VAPID public key does not match private key")
	}
	if subject == "" {
		subject = "mailto:admin@fleetdeck.local"
	}
	return &Sender{
		key:       &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, D: d},
		publicKey: base64.RawURLEncoding.EncodeToString(publicKey),
		subject:   subject,
		client: &http.Client{
			Timeout: 10 * time.Second,
			// VAPID endpoints are public HTTPS URLs; redirects are not part of
			// the push protocol and would drop the signed request body.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		now: time.Now,
	}, nil
}

// PublicKey returns the base64url applicationServerKey the browser needs when
// creating a subscription.
func (s *Sender) PublicKey() string { return s.publicKey }

// Send encrypts payload for sub and POSTs it to the subscription endpoint. A
// non-2xx response is returned as an error carrying the status code (the caller
// prunes 404/410 registrations). Transport/encryption failures return status 0.
func (s *Sender) Send(ctx context.Context, sub Subscription, payload []byte) (int, error) {
	body, err := Encrypt(payload, sub.P256dh, sub.Auth)
	if err != nil {
		return 0, err
	}
	auth, err := s.authorization(sub.Endpoint)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("push: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", fmt.Sprintf("%d", int(pushTTL.Seconds())))
	req.Header.Set("Authorization", auth)

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("push: send: %w", err)
	}
	defer resp.Body.Close()
	// Drain a bounded amount so the connection can be reused; the body only
	// carries diagnostics on failures.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("push: endpoint returned %s", resp.Status)
	}
	return resp.StatusCode, nil
}

// authorization builds the RFC 8292 `vapid t=<jwt>, k=<public key>` header.
func (s *Sender) authorization(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("push: invalid subscription endpoint %q", endpoint)
	}
	now := s.now().UTC()
	claims, err := json.Marshal(map[string]any{
		"aud": u.Scheme + "://" + u.Host,
		"exp": now.Add(vapidTTL).Unix(),
		"sub": s.subject,
	})
	if err != nil {
		return "", fmt.Errorf("push: marshal VAPID claims: %w", err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	signingInput := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	r, sv, err := ecdsa.Sign(rand.Reader, s.key, digest[:])
	if err != nil {
		return "", fmt.Errorf("push: sign VAPID JWT: %w", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	sv.FillBytes(sig[32:])
	jwt := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
	return "vapid t=" + jwt + ", k=" + s.publicKey, nil
}

// Encrypt produces the aes128gcm request body for payload: a fresh ephemeral
// P-256 key and salt are generated for every message.
func Encrypt(payload []byte, p256dh, auth string) ([]byte, error) {
	if len(payload) > recordSize-86-16-1 {
		return nil, fmt.Errorf("push: payload too large (%d bytes)", len(payload))
	}
	uaBytes, err := decodeBase64URL(p256dh)
	if err != nil {
		return nil, fmt.Errorf("push: decode p256dh: %w", err)
	}
	uaPub, err := ecdh.P256().NewPublicKey(uaBytes)
	if err != nil {
		return nil, fmt.Errorf("push: invalid p256dh point: %w", err)
	}
	authSecret, err := decodeBase64URL(auth)
	if err != nil {
		return nil, fmt.Errorf("push: decode auth secret: %w", err)
	}
	if len(authSecret) != 16 {
		return nil, fmt.Errorf("push: auth secret must be 16 bytes, got %d", len(authSecret))
	}
	eph, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("push: generate ephemeral key: %w", err)
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("push: generate salt: %w", err)
	}
	return encryptRecord(payload, uaPub, authSecret, eph, salt)
}

// encryptRecord is Encrypt with caller-supplied key material, which the RFC
// vector test pins.
func encryptRecord(payload []byte, uaPub *ecdh.PublicKey, authSecret []byte, eph *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(salt) != 16 {
		return nil, fmt.Errorf("push: salt must be 16 bytes, got %d", len(salt))
	}
	shared, err := eph.ECDH(uaPub)
	if err != nil {
		return nil, fmt.Errorf("push: ECDH: %w", err)
	}
	cek, nonce, err := deriveKeys(shared, authSecret, uaPub.Bytes(), eph.PublicKey().Bytes(), salt)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, fmt.Errorf("push: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("push: gcm: %w", err)
	}

	plaintext := make([]byte, 0, len(payload)+1)
	plaintext = append(plaintext, payload...)
	plaintext = append(plaintext, paddingDelimiter)

	asPub := eph.PublicKey().Bytes()
	header := make([]byte, 0, 16+4+1+len(asPub))
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, recordSize)
	header = append(header, byte(len(asPub)))
	header = append(header, asPub...)

	return gcm.Seal(header, nonce, plaintext, nil), nil
}

// deriveKeys runs the RFC 8291 §3.4 key schedule. uaPub/asPub are the
// uncompressed points (the key_info concatenation order is receiver then
// sender).
func deriveKeys(shared, authSecret, uaPub, asPub, salt []byte) (cek, nonce []byte, err error) {
	prk, err := hkdf.Extract(sha256.New, shared, authSecret)
	if err != nil {
		return nil, nil, fmt.Errorf("push: HKDF extract: %w", err)
	}
	keyInfo := make([]byte, 0, len("WebPush: info\x00")+len(uaPub)+len(asPub))
	keyInfo = append(keyInfo, "WebPush: info\x00"...)
	keyInfo = append(keyInfo, uaPub...)
	keyInfo = append(keyInfo, asPub...)
	ikm, err := hkdf.Expand(sha256.New, prk, string(keyInfo), 32)
	if err != nil {
		return nil, nil, fmt.Errorf("push: HKDF expand key: %w", err)
	}
	cek, err = hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, nil, fmt.Errorf("push: HKDF cek: %w", err)
	}
	nonce, err = hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, nil, fmt.Errorf("push: HKDF nonce: %w", err)
	}
	return cek, nonce, nil
}

// decodeBase64URL accepts padded or unpadded base64url (browsers emit the
// latter; some clients add padding).
func decodeBase64URL(v string) ([]byte, error) {
	v = strings.TrimSpace(v)
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(v, "="))
}
