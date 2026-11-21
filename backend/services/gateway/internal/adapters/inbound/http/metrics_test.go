package httpapi_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"go.opentelemetry.io/otel/attribute"

	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/pkg/otelx"
	"github.com/raduolivas/dt/backend/pkg/otelx/otelxtest"
	httpapi "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/http"
	redislimit "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/redis"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// testMeter swaps the package meter for one backed by a manual reader built
// with the production views. The calling test must not be parallel.
func testMeter(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithView(otelx.Views()...))
	t.Cleanup(httpapi.SetMeterForTest(mp.Meter("test")))
	return reader
}

// counts collects the reader, asserts the allow-list, and returns the points
// of the named counter as "label=value" -> value.
func counts(t *testing.T, reader *sdkmetric.ManualReader, name, label string) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	otelxtest.AssertAllowed(t, rm)
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s is %T", name, m.Data)
			}
			for _, dp := range sum.DataPoints {
				if dp.Attributes.Len() != 1 {
					t.Fatalf("%s attributes = %v", name, dp.Attributes)
				}
				v, ok := dp.Attributes.Value(attribute.Key(label))
				if !ok {
					t.Fatalf("%s lacks %s: %v", name, label, dp.Attributes)
				}
				out[v.AsString()] += dp.Value
			}
		}
	}
	return out
}

func TestSurfaceOf(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]string{
		"/v1/courier/token":            "courier",
		"/v1/courier/locations":        "courier",
		"/v1/tracking/abc":             "viewer",
		"/v1/tracking/abc/stream":      "viewer",
		"/v1/deliveries":               "partner",
		"/v1/courier-sessions":         "partner",
		"/v1/couriers/cou_1/locations": "partner",
		"/v1/couriersx":                "partner",
		"/":                            "partner",
		"":                             "partner",
		"/v1/courier":                  "partner",
		"/v1/trackingx/abc":            "partner",
	} {
		if got := httpapi.SurfaceOf(path); got != want {
			t.Errorf("surfaceOf(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestRateLimitedCountsSurface(t *testing.T) {
	reader := testMeter(t)
	e := newCourierEdge(t, &burstLimiterMem{n: map[string]int{}})
	for i := 1; i <= 20; i++ {
		wantProblem(t, e.do(tokenReq(`{"grant_type":"link_code","code":"guess-`+strconv.Itoa(i)+`"}`)), http.StatusUnauthorized, "link_code_invalid")
	}
	if got := counts(t, reader, "dt.ratelimit.rejections", "surface"); len(got) != 0 {
		t.Fatalf("rejections before the limit = %v", got)
	}
	wantProblem(t, e.do(tokenReq(`{"grant_type":"link_code","code":"guess-21"}`)), http.StatusTooManyRequests, "rate_limited")
	got := counts(t, reader, "dt.ratelimit.rejections", "surface")
	if len(got) != 1 || got["courier"] != 1 {
		t.Fatalf("rejections = %v, want courier=1", got)
	}
}

func TestPartnerRateLimitedCountsSurface(t *testing.T) {
	reader := testMeter(t)
	k := mint(t, apikey.Test, "ak_metric", domain.ScopeDeliveriesRead)
	e := limited(t, slowRefill{redislimit.NewLimiter(redisClient(t))}, k)
	for range app.PartnerKey.Burst {
		if rec := e.do(get("/v1/me", "Bearer "+k.raw)); rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	wantProblem(t, e.do(get("/v1/me", "Bearer "+k.raw)), http.StatusTooManyRequests, "rate_limited")
	got := counts(t, reader, "dt.ratelimit.rejections", "surface")
	if len(got) != 1 || got["partner"] != 1 {
		t.Fatalf("rejections = %v, want partner=1", got)
	}
}

func TestCourierSessionsStartedCountsMode(t *testing.T) {
	reader := testMeter(t)
	g := newSessionGW(t, nil)
	g.do(t, g.req("POST", "/v1/courier-sessions", sdkBody()), http.StatusCreated)
	g.do(t, g.req("POST", "/v1/courier-sessions", `{"courier_id":"`+couID+`","mode":"link"}`), http.StatusCreated)
	g.do(t, g.req("POST", "/v1/courier-sessions", sdkBody()), http.StatusCreated)
	// A refused start is not counted.
	g.do(t, g.req("POST", "/v1/courier-sessions", `{"courier_id":"`+couOther+`","mode":"sdk"}`), http.StatusNotFound)
	got := counts(t, reader, "dt.courier.sessions.started", "mode")
	if len(got) != 2 || got["sdk"] != 2 || got["link"] != 1 {
		t.Fatalf("started = %v, want sdk=2 link=1", got)
	}
}

func TestCourierRefreshOutcomes(t *testing.T) {
	reader := testMeter(t)
	e := newCourierEdge(t, nil)
	refreshReq := func(tok string) *http.Request {
		return tokenReq(`{"grant_type":"refresh_token","refresh_token":"` + tok + `"}`)
	}

	// ok
	first := e.sdk(partnerTest)
	rec := e.do(refreshReq(first.Tokens.RefreshToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh = %d: %s", rec.Code, rec.Body.String())
	}
	// reuse: the superseded token
	wantProblem(t, e.do(refreshReq(first.Tokens.RefreshToken)), http.StatusUnauthorized, "refresh_token_invalid")
	// invalid: unknown token
	wantProblem(t, e.do(refreshReq("guess")), http.StatusUnauthorized, "refresh_token_invalid")
	// expired: the session has ended
	second := e.sdk(partnerTest)
	if _, err := e.svc.End(context.Background(), partnerTest, second.Session.ID, string(domain.EndPartner)); err != nil {
		t.Fatal(err)
	}
	wantProblem(t, e.do(refreshReq(second.Tokens.RefreshToken)), http.StatusUnauthorized, "session_ended")
	// A link-code failure is not a refresh.
	wantProblem(t, e.do(tokenReq(`{"grant_type":"link_code","code":"nope"}`)), http.StatusUnauthorized, "link_code_invalid")

	got := counts(t, reader, "dt.courier.refresh", "outcome")
	want := map[string]int64{"ok": 1, "reuse": 1, "invalid": 1, "expired": 1}
	if len(got) != len(want) {
		t.Fatalf("refresh = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("refresh = %v, want %v", got, want)
		}
	}
}
