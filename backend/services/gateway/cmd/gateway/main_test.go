package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/pkg/ids"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/pkg/testkit"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

const testRPCToken = "test-only-internal-rpc-token-0123456789"

var testPepper = []byte("test-only-pepper-0123456789abcdef")

// testDataKeys is a test-only DATA_KEYS value (id "t1").
var testDataKeys = "t1:" + base64.StdEncoding.EncodeToString([]byte("test-only-gateway-data-key-01234"))

// testViewerKeys is a test-only VIEWER_SIGNING_KEYS value (id "v1").
var testViewerKeys = "v1:" + base64.StdEncoding.EncodeToString([]byte("test-only-viewer-signing-seed-01"))

func setViewerEnv(t *testing.T) {
	t.Helper()
	t.Setenv("VIEWER_SIGNING_KEYS", testViewerKeys)
	t.Setenv("VIEWER_SIGNING_KEY_CURRENT", "v1")
	t.Setenv("TRACKING_URL_BASE", "http://localhost:8080")
	t.Setenv("TRACKING_RPC_URL", "http://127.0.0.1:1")
}

func TestRunRefusesBadConfigBeforeIO(t *testing.T) {
	// No DATABASE_URL etc.: a config error must come first and never echo
	// the value.
	for _, tc := range []struct{ name, pepper, proxies, want string }{
		{"missing pepper", "", "", "APIKEY_PEPPER"},
		{"short pepper", base64.StdEncoding.EncodeToString([]byte("short-pepper-value")), "", "APIKEY_PEPPER"},
		{"bad proxies", base64.StdEncoding.EncodeToString(testPepper), "10.0.0.7", "TRUSTED_PROXIES"},
		{"missing rpc url", base64.StdEncoding.EncodeToString(testPepper), "", "DELIVERIES_RPC_URL"},
		{"short rpc token", base64.StdEncoding.EncodeToString(testPepper), "", "INTERNAL_RPC_TOKEN"},
		{"missing data keys", base64.StdEncoding.EncodeToString(testPepper), "", "DATA_KEYS"},
		{"unknown current data key", base64.StdEncoding.EncodeToString(testPepper), "", "DATA_KEY_CURRENT"},
		{"missing viewer keys", base64.StdEncoding.EncodeToString(testPepper), "", "VIEWER_SIGNING_KEYS"},
		{"unknown current viewer key", base64.StdEncoding.EncodeToString(testPepper), "", "VIEWER_SIGNING_KEY_CURRENT"},
		{"bad tracking url base", base64.StdEncoding.EncodeToString(testPepper), "", "TRACKING_URL_BASE"},
		{"missing tracking rpc url", base64.StdEncoding.EncodeToString(testPepper), "", "TRACKING_RPC_URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DELIVERIES_RPC_URL", "http://deliveries:8081")
			t.Setenv("INTERNAL_RPC_TOKEN", testRPCToken)
			t.Setenv("DATA_KEYS", testDataKeys)
			t.Setenv("DATA_KEY_CURRENT", "t1")
			setViewerEnv(t)
			switch tc.name {
			case "missing viewer keys":
				t.Setenv("VIEWER_SIGNING_KEYS", "")
			case "unknown current viewer key":
				t.Setenv("VIEWER_SIGNING_KEY_CURRENT", "v9")
			case "bad tracking url base":
				t.Setenv("TRACKING_URL_BASE", "ftp://track")
			case "missing tracking rpc url":
				t.Setenv("TRACKING_RPC_URL", "")
			case "missing rpc url":
				t.Setenv("DELIVERIES_RPC_URL", "")
			case "short rpc token":
				t.Setenv("INTERNAL_RPC_TOKEN", "short")
			case "missing data keys":
				t.Setenv("DATA_KEYS", "")
			case "unknown current data key":
				t.Setenv("DATA_KEY_CURRENT", "t9")
			}
			t.Setenv("APIKEY_PEPPER", tc.pepper)
			t.Setenv("TRUSTED_PROXIES", tc.proxies)
			t.Setenv("DATABASE_URL", "")
			err := run(context.Background(), nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run = %v, want a %s error", err, tc.want)
			}
			if tc.pepper != "" && strings.Contains(err.Error(), tc.pepper) {
				t.Fatalf("error echoes the pepper: %v", err)
			}
			if strings.Contains(err.Error(), testDataKeys[3:]) || strings.Contains(err.Error(), testViewerKeys[3:]) {
				t.Fatalf("error echoes a data key: %v", err)
			}
		})
	}
}

// startGateway runs the gateway via run() against real Postgres and NATS and
// the given Redis until the test ends. It returns the base URL, the NATS URL
// and a context cancelled at cleanup.
func startGateway(t *testing.T, redisURL string) (string, string, context.Context) {
	t.Helper()
	pg := testkit.StartPostgres(t)
	natsURL := testkit.StartNATS(t)

	t.Setenv("PORT", "0")
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("DATABASE_URL", pg.URL("gateway"))
	t.Setenv("MIGRATIONS_DATABASE_URL", pg.MigratorURL("gateway"))
	t.Setenv("REDIS_URL", redisURL)
	t.Setenv("NATS_URL", natsURL)
	t.Setenv("APIKEY_PEPPER", base64.StdEncoding.EncodeToString(testPepper))
	t.Setenv("TRUSTED_PROXIES", "")
	t.Setenv("DELIVERIES_RPC_URL", "http://127.0.0.1:1") // never called here
	t.Setenv("INTERNAL_RPC_TOKEN", testRPCToken)
	t.Setenv("DATA_KEYS", testDataKeys)
	t.Setenv("DATA_KEY_CURRENT", "t1")
	setViewerEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	// Play accounts' side: it owns ACCOUNTS and creates it before the
	// gateway's consumer starts waiting for it.
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.EnsureStream(ctx, js, outbox.StreamAccounts); err != nil {
		t.Fatal(err)
	}
	addr := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, func(a string) { addr <- a }) }()
	var base string
	select {
	case a := <-addr:
		base = "http://" + a
	case err := <-done:
		t.Fatalf("run returned early: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("gateway did not start")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("run = %v after cancel, want nil", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("run did not return after cancel")
		}
	})
	return base, natsURL, ctx
}

// publishKey plays accounts' side: it publishes an apikey.created event for
// a freshly minted test key and returns the key, its tenant and its id.
func publishKey(t *testing.T, ctx context.Context, natsURL string) (apikey.Key, string, string) {
	t.Helper()
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.EnsureStream(ctx, js, outbox.StreamAccounts); err != nil {
		t.Fatal(err)
	}
	key, err := apikey.Generate(apikey.Test)
	if err != nil {
		t.Fatal(err)
	}
	tenant, keyID := ids.New(ids.Tenant), ids.New(ids.APIKey)
	now := time.Now()
	env, err := outbox.NewEnvelope("apikey.created", tenant, eventsv1.Environment_ENVIRONMENT_TEST, "accounts", nil,
		&eventsv1.ApiKeyCreated{
			KeyId: keyID, TenantId: tenant, Environment: eventsv1.Environment_ENVIRONMENT_TEST,
			Lookup: key.Lookup, Hash: apikey.Hash(testPepper, key.Plaintext),
			Scopes: []string{"deliveries:read"}, CreatedAt: timestamppb.New(now),
		}, now)
	if err != nil {
		t.Fatal(err)
	}
	data, err := proto.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	msg := &nats.Msg{Subject: outbox.SubjectFor(outbox.StreamAccounts, "apikey.created"), Data: data, Header: nats.Header{}}
	msg.Header.Set(jetstream.MsgIDHeader, env.GetEventId())
	if _, err := js.PublishMsg(ctx, msg); err != nil {
		t.Fatal(err)
	}
	return key, tenant, keyID
}

// getTimed sends GET base+path (with a bearer key unless empty) and returns
// the status, body and how long the round trip took.
func getTimed(t *testing.T, ctx context.Context, url, bearer string) (int, []byte, time.Duration) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body, time.Since(start)
}

// waitForMe polls GET /v1/me until the key from the event authenticates
// (200) or 2 s pass, failing if any single request takes maxLatency or more.
func waitForMe(t *testing.T, ctx context.Context, base, key string, maxLatency time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		status, body, took := getTimed(t, ctx, base+"/v1/me", key)
		if took >= maxLatency {
			t.Fatalf("GET /v1/me took %v (status %d), want < %v", took, status, maxLatency)
		}
		if status == http.StatusOK {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET /v1/me = %d %s, want 200 within 2 s of the event", status, body)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestGatewayStartsAndServesMe(t *testing.T) {
	base, natsURL, ctx := startGateway(t, testkit.StartRedis(t))
	key, tenant, keyID := publishKey(t, ctx, natsURL)

	body := waitForMe(t, ctx, base, key.Plaintext, 30*time.Second)
	var got struct {
		TenantID    string `json:"tenant_id"`
		Environment string `json:"environment"`
		Credential  struct{ Type, ID string }
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.TenantID != tenant || got.Environment != "test" || got.Credential.ID != keyID {
		t.Fatalf("me = %s, want tenant %s key %s", body, tenant, keyID)
	}

	if status, body, _ := getTimed(t, ctx, base+"/readyz", ""); status != http.StatusOK {
		t.Fatalf("readyz = %d %s, want 200 with db, redis and nats", status, body)
	}
}

// hangingRedis accepts connections and never answers (a hung Redis: no
// reset, no reply). It returns a redis:// URL for it.
func hangingRedis(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
			go func() { _, _ = io.Copy(io.Discard, c) }()
		}
	}()
	return "redis://" + ln.Addr().String() + "/0"
}

// TestGatewayServesWhenRedisHangs is R21 end to end: with a Redis that
// accepts and never replies, the limiter fails open within its budget, the
// API keeps answering and /readyz stays 200 (Redis is non-fatal for the
// gateway), so an outage does not drain the replicas.
func TestGatewayServesWhenRedisHangs(t *testing.T) {
	base, natsURL, ctx := startGateway(t, hangingRedis(t))
	key, _, _ := publishKey(t, ctx, natsURL)

	waitForMe(t, ctx, base, key.Plaintext, time.Second)
	status, body, took := getTimed(t, ctx, base+"/v1/me", key.Plaintext)
	t.Logf("GET /v1/me with a hung redis: %d after %v", status, took)
	if status != http.StatusOK || took >= time.Second {
		t.Fatalf("GET /v1/me = %d %s after %v, want 200 in < 1s", status, body, took)
	}

	status, body, took = getTimed(t, ctx, base+"/readyz", "")
	t.Logf("GET /readyz with a hung redis: %d after %v", status, took)
	if status != http.StatusOK {
		t.Fatalf("readyz = %d %s, want 200 (redis is non-fatal for the gateway)", status, body)
	}
}

func TestTrackPageConfigReducesCSPToOrigins(t *testing.T) {
	t.Setenv("TRACKING_PAGE_BASE", "http://localhost:8090/page/dev/")
	t.Setenv("TRACKING_PAGE_CDN", "http://localhost:8090")
	t.Setenv("TRACKING_PAGE_TILES", "http://localhost:8090/tiles/amsterdam-z14.pmtiles")
	t.Setenv("TRACKING_PAGE_API", "http://localhost:8080/v1")
	t.Setenv("TRACKING_PAGE_CONNECT_EXTRA", "https://tiles.openfreemap.org")
	c, err := trackPageConfig("http://localhost:8080")
	if err != nil {
		t.Fatal(err)
	}
	if c.API != "http://localhost:8080/v1" || c.Tiles != "http://localhost:8090/tiles/amsterdam-z14.pmtiles" {
		t.Fatalf("JSON config lost its full URLs: %+v", c)
	}
	csp := domain.PageCSP("'none'", c.Origins)
	for _, want := range []string{
		"connect-src http://localhost:8080 ws://localhost:8080 http://localhost:8090 http://localhost:8090 https://tiles.openfreemap.org;",
		"script-src http://localhost:8090;",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q:\n%s", want, csp)
		}
	}
	if strings.Contains(csp, "/v1") || strings.Contains(csp, ".pmtiles") {
		t.Errorf("CSP carries a path: %s", csp)
	}
}

func TestTrackPageConfigRejectsBadURLs(t *testing.T) {
	for _, env := range []string{"TRACKING_PAGE_API", "TRACKING_PAGE_TILES", "TRACKING_PAGE_CDN"} {
		t.Setenv(env, "not a url")
		if _, err := trackPageConfig("http://localhost:8080"); err == nil || !strings.Contains(err.Error(), env) {
			t.Errorf("%s: err = %v", env, err)
		}
		t.Setenv(env, "")
	}
}
