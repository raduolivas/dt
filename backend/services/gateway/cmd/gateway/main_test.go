package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
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
)

var testPepper = []byte("test-only-pepper-0123456789abcdef")

func TestRunRefusesBadConfigBeforeIO(t *testing.T) {
	// No DATABASE_URL etc.: a config error must come first and never echo
	// the value.
	for _, tc := range []struct{ name, pepper, proxies, want string }{
		{"missing pepper", "", "", "APIKEY_PEPPER"},
		{"short pepper", base64.StdEncoding.EncodeToString([]byte("short-pepper-value")), "", "APIKEY_PEPPER"},
		{"bad proxies", base64.StdEncoding.EncodeToString(testPepper), "10.0.0.7", "TRUSTED_PROXIES"},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
		})
	}
}

func TestGatewayStartsAndServesMe(t *testing.T) {
	pg := testkit.StartPostgres(t)
	redisURL := testkit.StartRedis(t)
	natsURL := testkit.StartNATS(t)

	t.Setenv("PORT", "0")
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("DATABASE_URL", pg.URL("gateway"))
	t.Setenv("REDIS_URL", redisURL)
	t.Setenv("NATS_URL", natsURL)
	t.Setenv("APIKEY_PEPPER", base64.StdEncoding.EncodeToString(testPepper))
	t.Setenv("TRUSTED_PROXIES", "")

	ctx, cancel := context.WithCancel(context.Background())
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

	// accounts' side: an apikey.created event for a freshly minted key.
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

	me := func() (int, []byte) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/me", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+key.Plaintext)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, body
	}
	deadline := time.Now().Add(2 * time.Second)
	status, body := me()
	for status != http.StatusOK && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		status, body = me()
	}
	if status != http.StatusOK {
		t.Fatalf("GET /v1/me = %d %s, want 200 within 2 s of the event", status, body)
	}
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

	ready, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/readyz", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(ready)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz = %d, want 200 with db, redis and nats", resp.StatusCode)
	}
}
