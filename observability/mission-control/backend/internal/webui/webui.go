// Package webui serves the built Mission Control UI (web/dist) at "/".
package webui

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

var staticExt = map[string]bool{
	".js": true, ".css": true, ".svg": true, ".png": true, ".ico": true, ".woff2": true,
	".map": true, ".json": true, ".txt": true, ".webmanifest": true,
}

// Handler serves static files from dir with a fallback to index.html for
// extension-less, non-/api paths (client-side routes). It returns false when
// dir holds no index.html, so a backend run without a built UI still works.
func Handler(dir string) (http.Handler, bool) {
	fsys := os.DirFS(dir) // rejects ".." elements, so a request cannot leave dir
	if st, err := fs.Stat(fsys, "index.html"); err != nil || st.IsDir() {
		return nil, false
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		if p == "/api" || strings.HasPrefix(p, "/api/") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(p, "/")
		if name != "" {
			if st, err := fs.Stat(fsys, name); err == nil && !st.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // content-hashed names
				}
				http.ServeFileFS(w, r, fsys, name) //nolint:gosec // G703: name is path.Clean'ed and fsys is os.DirFS, which cannot leave dir (TestNoTraversal)
				return
			}
			// A missing asset or file of a known static type is a 404, not the app; any other
			// dotted last segment (an order id such as del_1.2) is a client route.
			if strings.HasPrefix(name, "assets/") || staticExt[strings.ToLower(path.Ext(name))] {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, fsys, "index.html")
	}), true
}
