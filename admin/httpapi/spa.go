package httpapi

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// assetExts are the extensions of build artifacts; a missing file with one
// of them is a real 404 rather than a client route.
var assetExts = map[string]bool{
	".js": true, ".mjs": true, ".css": true, ".map": true, ".png": true, ".jpg": true, ".jpeg": true,
	".gif": true, ".svg": true, ".ico": true, ".webp": true, ".woff": true, ".woff2": true,
	".ttf": true, ".json": true, ".txt": true, ".webmanifest": true,
}

// spaHandler serves static files from assets, falling back to index.html
// for any path that is not a file (client-side routing).
func spaHandler(assets fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		if assets == nil {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if st, err := fs.Stat(assets, name); err != nil || st.IsDir() {
			// A missing file with a known static extension is a real 404; other
			// dotted paths (e.g. /actions/claims.list) are client routes.
			if assetExts[path.Ext(name)] {
				http.NotFound(w, r)
				return
			}
			name = "index.html"
		}
		b, err := fs.ReadFile(assets, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if name == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
	})
}
