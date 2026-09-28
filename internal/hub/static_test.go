package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// newSPAHandler is pure (fs in, handler out), so the routing contract is
// covered without a database: files serve, client routes fall back to the SPA
// entry point, and a UI-less build answers with a helpful 404.

func TestSPAHandlerServesFilesAndFallsBack(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":    {Data: []byte("<html>app</html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
		"config.js":     {Data: []byte("// config")},
	}
	h := newSPAHandler(fsys)

	for _, tc := range []struct {
		path, wantBody string
	}{
		{"/", "<html>app</html>"},
		{"/sessions/abc-123", "<html>app</html>"}, // client-side route
		{"/assets/app.js", "console.log(1)"},
		{"/config.js", "// config"},
	} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", tc.path, rec.Code)
			continue
		}
		if got := rec.Body.String(); got != tc.wantBody {
			t.Errorf("GET %s body = %q, want %q", tc.path, got, tc.wantBody)
		}
	}

	// ServeFileFS canonicalizes /index.html to the app root.
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/index.html", nil))
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "./" {
		t.Fatalf("GET /index.html = %d Location %q, want 301 to ./", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSPAHandlerWithoutBuiltUI(t *testing.T) {
	h := newSPAHandler(fstest.MapFS{})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "web UI not built") {
		t.Fatalf("body = %q, want it to mention the missing UI", body)
	}
}
