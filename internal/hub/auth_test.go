package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"fleetdeck/internal/store"

	"golang.org/x/crypto/bcrypt"
)

// testAuth carries the credentials newTestHub provisions for each test: an
// admin user plus its bearer token. The user is removed on cleanup (tokens and
// node bindings cascade).
type testAuth struct {
	user  store.User
	token string
}

// setupUserWithRole creates a user with the given role and mints a bearer
// token for it, returning the user and the plaintext token.
func setupUserWithRole(t *testing.T, st *store.Store, role string) (store.User, string) {
	t.Helper()
	email := uniqueName("test-user") + "@example.com"

	hash, err := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	ctx := context.Background()
	user, err := st.Queries().CreateUser(ctx, store.CreateUserParams{
		Email:        email,
		DisplayName:  "Test User",
		PasswordHash: string(hash),
		Role:         role,
	})
	if err != nil {
		t.Fatalf("create test user: %v", err)
	}
	t.Cleanup(func() {
		bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := st.Pool().Exec(bg, "DELETE FROM users WHERE id = $1", user.ID); err != nil {
			t.Logf("cleanup: delete user: %v", err)
		}
	})

	// Tests share one database, so /api/auth/setup (empty-table only) cannot
	// provision users here; mint the token directly through the store.
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	if _, err := st.Queries().CreateAuthToken(ctx, store.CreateAuthTokenParams{
		UserID:    user.ID,
		TokenHash: hashAuthToken(token),
		ExpiresAt: time.Now().UTC().Add(authTokenTTL),
	}); err != nil {
		t.Fatalf("mint test token: %v", err)
	}
	return user, token
}

// doRequest issues one HTTP request against the test server with an optional
// JSON body and an optional bearer token ("" sends no Authorization header).
func doRequest(t *testing.T, method, token, url string, body any) (*http.Response, []byte) {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body for %s %s: %v", method, url, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, url, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body from %s %s: %v", method, url, err)
	}
	return resp, data
}

// postRaw posts a JSON body as the test admin and returns the response plus
// its raw bytes. Pass an explicit token (possibly "") via doRequest to test
// unauthenticated paths.
func postRaw(t *testing.T, token, url string, body any) (*http.Response, []byte) {
	t.Helper()
	return doRequest(t, http.MethodPost, token, url, body)
}

// getRaw GETs a URL as the test admin and returns the response plus its raw
// bytes.
func getRaw(t *testing.T, token, url string) (*http.Response, []byte) {
	t.Helper()
	return doRequest(t, http.MethodGet, token, url, nil)
}

// patchRaw PATCHes a JSON body and returns the response plus its raw bytes.
func patchRaw(t *testing.T, token, url string, body any) (*http.Response, []byte) {
	t.Helper()
	return doRequest(t, http.MethodPatch, token, url, body)
}

func trimScheme(url string) string {
	if u, ok := strings.CutPrefix(url, "http"); ok {
		return u
	}
	if u, ok := strings.CutPrefix(url, "https"); ok {
		return u
	}
	return url
}

// deleteReq DELETEs a URL and returns the response plus its raw bytes.
func deleteReq(t *testing.T, token, url string) (*http.Response, []byte) {
	t.Helper()
	return doRequest(t, http.MethodDelete, token, url, nil)
}

// decodeOK unmarshals a 2xx response body into out, failing the test with the
// raw body on decode errors.
func decodeOK[T any](t *testing.T, url string, resp *http.Response, raw []byte) T {
	t.Helper()
	var out T
	if resp.StatusCode >= 300 {
		t.Fatalf("request %s: status %d: %s", url, resp.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode response from %s: %v (%s)", url, err, raw)
	}
	return out
}

// dialWebWS opens an authenticated /ws/web connection for tests.
func dialWebWS(t *testing.T, ts *httptest.Server, token string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + trimScheme(ts.URL) + "/ws/web?token=" + token
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial web ws: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
