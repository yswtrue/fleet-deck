package hub

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// The embedded web UI. It is populated at build time (the Dockerfile copies
// web/dist here before `go build`; locally run `pnpm build` in web/ and copy
// the output). A build without the UI still compiles: requests then get a
// helpful 404 instead of the SPA.
//
//go:embed all:static
var embeddedUI embed.FS

var uiFS = mustSub(embeddedUI, "static")

func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

// newSPAHandler serves the embedded web UI. Existing files are served as-is;
// anything else falls back to index.html so client-side routes (e.g.
// /sessions/{id}) survive a refresh on the hub itself.
func newSPAHandler(fsys fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p != "" && p != "." {
			if st, err := fs.Stat(fsys, p); err == nil && !st.IsDir() {
				http.ServeFileFS(w, r, fsys, p)
				return
			}
		}
		if _, err := fs.Stat(fsys, "index.html"); err == nil {
			http.ServeFileFS(w, r, fsys, "index.html")
			return
		}
		http.Error(w, "fleetdeck: web UI not built — run `pnpm build` in web/ and copy dist into internal/hub/static", http.StatusNotFound)
	}
}
