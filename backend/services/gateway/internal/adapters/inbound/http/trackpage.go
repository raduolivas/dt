package httpapi

import (
	"bytes"
	_ "embed"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/netip"
	"regexp"
	"strings"

	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

//go:embed trackpage.html
var trackPageHTML string

var trackPageTmpl = template.Must(template.New("trackpage").Parse(trackPageHTML))

// TrackPageConfig is the hosted page's static configuration (TRACKING_PAGE_*).
type TrackPageConfig struct {
	// Base is the URL prefix of the page's entry.css and entry.js; it ends
	// with "/". Empty means "/page/".
	Base string
	// API is the public API base the page calls; Tiles the map's TileJSON or
	// PMTiles URL; CDN the origin the assets and tiles are served from (empty:
	// the page's own origin). Extra are further connect-src origins (dev).
	API, Tiles, CDN string
	Extra           []string
	// Origins are the origins of API, Tiles, CDN and Extra as the CSP names
	// them (scheme://host[:port], no path: a CSP source with a path only
	// matches that exact path). Empty means no CDN/API/tiles sources.
	Origins domain.PageOrigins
}

// pageJSON is the dt-config element the page script reads.
type pageJSON struct {
	API   string `json:"api"`
	Tiles string `json:"tiles"`
	CDN   string `json:"cdn"`
}

// TrackPageDeps configures MountTrackPage.
type TrackPageDeps struct {
	// Page decides frame-ancestors and serves logos. Required.
	Page *app.TrackPage
	// Config is the page's static configuration.
	Config TrackPageConfig
	// Limiter applies app.ViewerIP per client IP to the page (nil disables
	// it; a failing limiter lets requests through).
	Limiter        app.Limiter
	TrustedProxies []netip.Prefix
	// Log receives server-side failures. Nil means slog.Default().
	Log *slog.Logger
}

// MountTrackPage registers the hosted tracking page and the tenant logos,
// outside /v1 and the partner authorization matrix (no credential is
// involved):
//
//	GET /t/{token}                  the page shell, always 200
//	GET /logos/{tenant_id}/{sha256} a tenant's logo, immutable
//
// The shell is byte-identical for every token and carries no token, tenant or
// delivery data; only its Content-Security-Policy differs, with the tenant's
// embed origins as frame-ancestors for a token that names a link (usable or
// not) and 'none' otherwise. The page runs ClientIP → rl:viewer:ip:<ip>.
func MountTrackPage(mux *http.ServeMux, d TrackPageDeps) {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	base := d.Config.Base
	if base == "" {
		base = "/page/"
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	var body bytes.Buffer
	cfg := pageJSON{API: d.Config.API, Tiles: d.Config.Tiles, CDN: d.Config.CDN}
	if err := trackPageTmpl.Execute(&body, map[string]any{"Base": base, "Config": cfg}); err != nil {
		panic("httpapi: track page template: " + err.Error())
	}
	origins := d.Config.Origins
	a := &trackPageAPI{page: d.Page, shell: body.Bytes(), origins: origins, log: log}

	var h http.Handler = http.HandlerFunc(a.serve)
	h = viewerIPLimit(d.Limiter, log)(h)
	h = ClientIP(d.TrustedProxies)(h)
	mux.Handle("GET /t/{token}", h)
	mux.Handle("GET /logos/{tenant_id}/{sha256}", http.HandlerFunc(a.logo))
}

type trackPageAPI struct {
	page    *app.TrackPage
	shell   []byte
	origins domain.PageOrigins
	log     *slog.Logger
}

func (a *trackPageAPI) serve(w http.ResponseWriter, r *http.Request) {
	fa, err := a.page.FrameAncestors(r.Context(), r.PathValue("token"))
	if err != nil {
		a.log.WarnContext(r.Context(), "track_page_unavailable",
			"error", err.Error(), "request_id", httpx.RequestIDFrom(r.Context()))
		w.Header().Set("Retry-After", "1")
		httpx.WriteProblem(w, r, unavailableProblem())
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", domain.PageCSP(fa, a.origins))
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(a.shell)
}

var hexSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (a *trackPageAPI) logo(w http.ResponseWriter, r *http.Request) {
	sha := r.PathValue("sha256")
	if !hexSHA256.MatchString(sha) {
		http.NotFound(w, r)
		return
	}
	b, ct, err := a.page.Logo(r.Context(), r.PathValue("tenant_id"), sha)
	switch {
	case errors.Is(err, app.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		a.log.ErrorContext(r.Context(), "logo_failed", "error", err.Error(), "request_id", httpx.RequestIDFrom(r.Context()))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("X-Content-Type-Options", "nosniff")
	// An image validated by accounts (PNG/WebP, <= 100 KiB), served with its own
	// type and nosniff: never interpreted as HTML.
	_, _ = w.Write(b) //nolint:gosec // G705: see above
}
