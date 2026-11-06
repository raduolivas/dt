package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func site(t *testing.T) http.Handler {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "dist")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "assets"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>INDEX</html>"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "assets", "app-abc.js"), []byte("console.log(1)"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(dir), "secret.txt"), []byte("SECRET"), 0o644))
	h, ok := Handler(dir)
	require.True(t, ok)
	return h
}

func get(h http.Handler, method, path string) (int, string, http.Header) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	b, _ := io.ReadAll(rec.Body)
	return rec.Code, string(b), rec.Header()
}

func TestServesIndexAndAssets(t *testing.T) {
	h := site(t)
	code, body, hd := get(h, "GET", "/")
	require.Equal(t, 200, code)
	require.Contains(t, body, "INDEX")
	require.Equal(t, "no-cache", hd.Get("Cache-Control"))
	code, body, hd = get(h, "GET", "/assets/app-abc.js")
	require.Equal(t, 200, code)
	require.Contains(t, body, "console.log")
	require.Contains(t, hd.Get("Cache-Control"), "immutable")
}

func TestSPAFallback(t *testing.T) {
	h := site(t)
	code, body, _ := get(h, "GET", "/orders/del_1")
	require.Equal(t, 200, code)
	require.Contains(t, body, "INDEX")
}

func TestMissingFileAndAPIPathsAre404(t *testing.T) {
	h := site(t)
	for _, p := range []string{"/assets/missing.js", "/favicon.ico", "/api/nope", "/api"} {
		code, body, _ := get(h, "GET", p)
		require.Equal(t, 404, code, p)
		require.NotContains(t, body, "INDEX", p)
	}
}

func TestNoTraversalAndGETOnly(t *testing.T) {
	h := site(t)
	for _, p := range []string{"/../secret.txt", "/%2e%2e/secret.txt", "/assets/../../secret.txt"} {
		_, body, _ := get(h, "GET", p)
		require.NotContains(t, body, "SECRET", p)
	}
	code, _, _ := get(h, "POST", "/")
	require.Equal(t, 405, code)
}

func TestDottedClientRoutesFallBack(t *testing.T) {
	h := site(t)
	for _, p := range []string{"/orders/del_1.2", "/orders/ORD-2026.11.05"} {
		code, body, _ := get(h, "GET", p)
		require.Equal(t, 200, code, p)
		require.Contains(t, body, "INDEX", p)
	}
	for _, p := range []string{"/assets/missing.9", "/missing.js", "/missing.css", "/a/b.woff2", "/x.map", "/m.webmanifest", "/i.svg", "/i.png", "/f.ico", "/d.json", "/t.txt"} {
		code, body, _ := get(h, "GET", p)
		require.Equal(t, 404, code, p)
		require.NotContains(t, body, "INDEX", p)
	}
}

func TestNoIndexNoHandler(t *testing.T) {
	_, ok := Handler(t.TempDir())
	require.False(t, ok)
}
