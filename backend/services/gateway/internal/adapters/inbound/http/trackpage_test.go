package httpapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httpapi "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/http"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

var logoPNG = append([]byte("\x89PNG\r\n\x1a\n"), 9, 8, 7)

func logoSHA() string {
	s := sha256.Sum256(logoPNG)
	return hex.EncodeToString(s[:])
}

func (v *viewerEdge) putSettings(t *testing.T, origins []string, b *domain.Branding, logo []byte) {
	t.Helper()
	if err := v.settings.Put(context.Background(), tenantID, domain.ViewerSettings{EmbedOrigins: origins, Branding: b}, logo, viewerNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func pageReq(tok string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/t/"+tok, nil)
}

func csp(rec *httptest.ResponseRecorder) string { return rec.Header().Get("Content-Security-Policy") }

func TestTrackPageKnownLinkUsesTenantOrigins(t *testing.T) {
	t.Parallel()
	v := newViewerEdge(t, nil)
	v.putSettings(t, []string{"https://shop.example", "https://app.example"}, nil, nil)
	_, tok := v.issue(t)
	rec := v.do(pageReq(tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := csp(rec); !strings.HasSuffix(got, "frame-ancestors https://shop.example https://app.example") ||
		!strings.Contains(got, "script-src https://cdn.example;") {
		t.Fatalf("CSP = %s", got)
	}
}

func TestTrackPageUnknownTokenIsNoneAndSameBody(t *testing.T) {
	t.Parallel()
	v := newViewerEdge(t, nil)
	v.putSettings(t, []string{"https://shop.example"}, nil, nil)
	l, tok := v.issue(t)
	known := v.do(pageReq(tok))

	unknownTok, _ := domain.NewLinkToken()
	cases := map[string]string{"unknown": unknownTok, "malformed": "short", "odd": "%3Cscript%3E"}
	for name, c := range cases {
		rec := v.do(pageReq(c))
		if rec.Code != http.StatusOK || !strings.HasSuffix(csp(rec), "frame-ancestors 'none'") {
			t.Fatalf("%s: %d, CSP %s", name, rec.Code, csp(rec))
		}
		if rec.Body.String() != known.Body.String() {
			t.Fatalf("%s: body differs from the known token's", name)
		}
	}

	// A known link that is revoked is answered exactly like a usable one:
	// the page's own session call shows the inactive card.
	if _, _, err := v.links.Rotate(context.Background(), domain.Principal{TenantID: tenantID, Env: domain.EnvTest}, delID); err != nil {
		t.Fatal(err)
	}
	if _, err := v.links.Resolve(context.Background(), tok, viewerNow); err == nil {
		t.Fatalf("link %s should be revoked", l.ID)
	}
	rec := v.do(pageReq(tok))
	if rec.Code != http.StatusOK || rec.Body.String() != known.Body.String() || csp(rec) != csp(known) {
		t.Fatalf("revoked link: %d, CSP %s", rec.Code, csp(rec))
	}
}

func TestTrackPageHeaders(t *testing.T) {
	t.Parallel()
	v := newViewerEdge(t, nil)
	_, tok := v.issue(t)
	rec := v.do(pageReq(tok))
	want := map[string]string{
		"Referrer-Policy":        "no-referrer",
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
		"Content-Type":           "text/html; charset=utf-8",
		"Content-Security-Policy": "default-src 'none'; script-src https://cdn.example; style-src https://cdn.example 'unsafe-inline'; " +
			"img-src https://cdn.example 'self' data: blob:; connect-src https://api.example wss://api.example https://cdn.example https://cdn.example https://tiles.openfreemap.org; " +
			"worker-src blob:; font-src https://cdn.example; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
	}
	for k, w := range want {
		if got := rec.Header().Get(k); got != w {
			t.Errorf("%s = %q, want %q", k, got, w)
		}
	}
	b := rec.Body.String()
	for _, w := range []string{`href="https://cdn.example/page/entry.css"`, `<script type="module" src="https://cdn.example/page/entry.js">`,
		`<script type="application/json" id="dt-config">`} {
		if !strings.Contains(b, w) {
			t.Errorf("body lacks %s:\n%s", w, b)
		}
	}
	// The config element is JSON the page can parse.
	i := strings.Index(b, `id="dt-config">`) + len(`id="dt-config">`)
	var cfg map[string]string
	if err := json.Unmarshal([]byte(b[i:i+strings.Index(b[i:], "</script>")]), &cfg); err != nil ||
		cfg["api"] != "https://api.example/v1" || cfg["tiles"] != "https://cdn.example/t.pmtiles" || cfg["cdn"] != "https://cdn.example" {
		t.Fatalf("config = %v, %v", cfg, err)
	}
	// HEAD and other methods follow the mux: HEAD is served, POST is not.
	if h := v.do(httptest.NewRequest(http.MethodHead, "/t/"+tok, nil)); h.Code != http.StatusOK {
		t.Errorf("HEAD = %d", h.Code)
	}
	if p := v.do(httptest.NewRequest(http.MethodPost, "/t/"+tok, nil)); p.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d", p.Code)
	}
}

func TestTrackPageHasNoTokenInBody(t *testing.T) {
	t.Parallel()
	v := newViewerEdge(t, nil)
	v.putSettings(t, []string{"https://shop.example"}, &domain.Branding{DisplayName: "Pizza Co"}, nil)
	_, tok := v.issue(t)
	rec := v.do(pageReq(tok))
	for _, secret := range []string{tok, tenantID, delID, "Pizza Co"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("body contains %q", secret)
		}
	}
	for _, h := range rec.Header() {
		for _, val := range h {
			if strings.Contains(val, tok) {
				t.Fatalf("header contains the token: %q", val)
			}
		}
	}
	if strings.Contains(v.logs.String(), tok) {
		t.Fatalf("token in logs: %s", v.logs.String())
	}
}

// Values in the JSON config cannot close the script element.
func TestTrackPageConfigCannotBreakOutOfScript(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	links := &app.TrackingLinks{Store: nil, Hasher: nil}
	httpapi.MountTrackPage(mux, httpapi.TrackPageDeps{Page: &app.TrackPage{Links: links},
		Config: httpapi.TrackPageConfig{API: `https://a.example/</script><script>alert(1)</script>`, Tiles: `x"y<!--`, CDN: "&<>'"}})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, pageReq("short"))
	b := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Count(b, "</script>") != 2 || strings.Contains(b, "alert(1)</script>") ||
		strings.Contains(b, "<!--") {
		t.Fatalf("breakout: %d\n%s", rec.Code, b)
	}
	i := strings.Index(b, `id="dt-config">`) + len(`id="dt-config">`)
	var cfg map[string]string
	if err := json.Unmarshal([]byte(b[i:i+strings.Index(b[i:], "</script>")]), &cfg); err != nil ||
		cfg["api"] != `https://a.example/</script><script>alert(1)</script>` || cfg["tiles"] != `x"y<!--` {
		t.Fatalf("config did not round-trip: %v, %v", cfg, err)
	}
}

func TestTrackPageRateLimitedPerIP(t *testing.T) {
	t.Parallel()
	v := newViewerEdge(t, &burstLimiterMem{n: map[string]int{}})
	_, tok := v.issue(t)
	for i := range app.ViewerIP.Burst {
		if rec := v.do(pageReq(tok)); rec.Code != http.StatusOK {
			t.Fatalf("request %d = %d", i, rec.Code)
		}
	}
	rec := v.do(pageReq(tok))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("over the limit: %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	// The 429 and the mux's 405 are logged without the token too.
	if p := v.do(httptest.NewRequest(http.MethodPost, "/t/"+tok, nil)); p.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d", p.Code)
	}
	if strings.Contains(v.logs.String(), tok) || !strings.Contains(v.logs.String(), `"path":"/t/{token}"`) {
		t.Fatalf("token in logs or path not redacted: %s", v.logs.String())
	}
}

func TestTrackPageStoreFailureIs503(t *testing.T) {
	t.Parallel()
	v := newViewerEdge(t, nil)
	v.putSettings(t, []string{"https://shop.example"}, nil, nil)
	_, tok := v.issue(t)
	v.settings.Fail(context.DeadlineExceeded)
	rec := v.do(pageReq(tok))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("%d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestLogoServedImmutableAndUnknown404(t *testing.T) {
	t.Parallel()
	v := newViewerEdge(t, nil)
	sha := logoSHA()
	v.putSettings(t, nil, &domain.Branding{LogoSHA256: sha, LogoContentType: "image/png"}, logoPNG)

	rec := v.do(httptest.NewRequest(http.MethodGet, "/logos/"+tenantID+"/"+sha, nil))
	if rec.Code != http.StatusOK || rec.Body.String() != string(logoPNG) {
		t.Fatalf("logo = %d %q", rec.Code, rec.Body.String())
	}
	want := map[string]string{"Content-Type": "image/png", "Cache-Control": "public, max-age=31536000, immutable",
		"Access-Control-Allow-Origin": "*", "X-Content-Type-Options": "nosniff"}
	for k, w := range want {
		if got := rec.Header().Get(k); got != w {
			t.Errorf("%s = %q, want %q", k, got, w)
		}
	}
	other := strings.Repeat("a", 64)
	for name, path := range map[string]string{
		"unknown sha":    "/logos/" + tenantID + "/" + other,
		"unknown tenant": "/logos/ten_nope/" + sha,
		"not hex":        "/logos/" + tenantID + "/" + strings.Repeat("Z", 64),
		"short":          "/logos/" + tenantID + "/abc",
	} {
		if rec := v.do(httptest.NewRequest(http.MethodGet, path, nil)); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d", name, rec.Code)
		}
	}
}

func TestSessionReturnsBrandingOrNull(t *testing.T) {
	t.Parallel()
	v := newViewerEdge(t, nil)
	_, tok := v.issue(t)

	var none struct {
		Branding json.RawMessage `json:"branding"`
	}
	req := sessionReq(tok)
	rec := v.do(req)
	checkResponse(t, req, rec)
	if err := json.Unmarshal(rec.Body.Bytes(), &none); err != nil || string(none.Branding) != "null" {
		t.Fatalf("no settings: branding = %s, %v", none.Branding, err)
	}

	sha := logoSHA()
	v.putSettings(t, nil, &domain.Branding{DisplayName: "Pizza Co", PrimaryColor: "#112233", Theme: "dark",
		LogoSHA256: sha, LogoContentType: "image/png"}, logoPNG)
	req = sessionReq(tok)
	rec = v.do(req)
	checkResponse(t, req, rec)
	var got struct {
		Branding map[string]any `json:"branding"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"display_name": "Pizza Co", "primary_color": "#112233", "theme": "dark",
		"logo_url": "https://t.example/logos/" + tenantID + "/" + sha}
	if len(got.Branding) != len(want) {
		t.Fatalf("branding = %v", got.Branding)
	}
	for k, w := range want {
		if got.Branding[k] != w {
			t.Errorf("branding.%s = %v, want %v", k, got.Branding[k], w)
		}
	}

	// Colour-only branding: no logo, no name, theme defaults to auto.
	v2 := newViewerEdge(t, nil)
	_, tok2 := v2.issue(t)
	v2.putSettings(t, nil, &domain.Branding{PrimaryColor: "#abcdef"}, nil)
	req = sessionReq(tok2)
	rec = v2.do(req)
	checkResponse(t, req, rec)
	var c struct {
		Branding map[string]any `json:"branding"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if c.Branding["display_name"] != nil || c.Branding["logo_url"] != nil || c.Branding["primary_color"] != "#abcdef" || c.Branding["theme"] != "auto" {
		t.Fatalf("branding = %v", c.Branding)
	}
}
