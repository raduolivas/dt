package rpc_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"

	accountsv1 "github.com/raduolivas/dt/backend/gen/dt/accounts/v1"
	"github.com/raduolivas/dt/backend/gen/dt/accounts/v1/accountsv1connect"
	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/migratex"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/pkg/rpcauth"
	"github.com/raduolivas/dt/backend/pkg/testkit"
	"github.com/raduolivas/dt/backend/services/accounts/internal/adapters/inbound/rpc"
	"github.com/raduolivas/dt/backend/services/accounts/internal/adapters/outbound/keygen"
	"github.com/raduolivas/dt/backend/services/accounts/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/accounts/internal/app"
	"github.com/raduolivas/dt/backend/services/accounts/migrations"
)

const testToken = "test-only-internal-rpc-token-0123456789"

var (
	pepper   = []byte("test-only-pepper-0123456789abcdef")
	keyRegex = regexp.MustCompile(`^dt_sk_(test|live)_[0-9A-Za-z]{55}$`)
	owner    = &accountsv1.Actor{Type: "system", Id: "test", Role: "owner"}
	dev      = &accountsv1.Actor{Type: "console_user", Id: "usr_1", Role: "developer"}
)

// starting limits concurrent container starts: rootless Docker's port
// forwarder fails with "address already in use" when many start at once.
var starting = make(chan struct{}, 2)

type clock struct{}

func (clock) Now() time.Time { return time.Now() }

// server starts Postgres, applies the migrations and serves the RPC API over
// the real app.Service and Postgres repo.
func server(t *testing.T) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	pg := func() *testkit.Postgres {
		starting <- struct{}{}
		defer func() { <-starting }()
		return testkit.StartPostgres(t)
	}()
	ctx := context.Background()
	url := pg.URL("accounts")
	if err := migratex.Up(ctx, pg.MigratorURL("accounts"), migrations.FS); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	svc := &app.Service{Repo: postgres.New(pool, time.Now), Keys: keygen.New(pepper), Clock: clock{}}
	mux := http.NewServeMux()
	if err := rpc.Mount(mux, svc, testToken, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, pool
}

// bearer sets an Authorization header on every request.
type bearer struct {
	c     *http.Client
	value string
}

func (b bearer) Do(r *http.Request) (*http.Response, error) {
	if b.value != "" {
		r.Header.Set("Authorization", b.value)
	}
	return b.c.Do(r)
}

func client(srv *httptest.Server, authorization string) accountsv1connect.AccountsServiceClient {
	return accountsv1connect.NewAccountsServiceClient(connect.NewClient(
		connecthttp.NewTransport(bearer{srv.Client(), authorization}, srv.URL)))
}

func authed(srv *httptest.Server) accountsv1connect.AccountsServiceClient {
	return client(srv, "Bearer "+testToken)
}

func createTenant(t *testing.T, c accountsv1connect.AccountsServiceClient) string {
	t.Helper()
	res, err := c.CreateTenant(context.Background(), &accountsv1.CreateTenantRequest{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.GetTenant().GetId(), "ten_") {
		t.Fatalf("tenant id = %q", res.GetTenant().GetId())
	}
	return res.GetTenant().GetId()
}

func TestRPCRequiresInternalToken(t *testing.T) {
	t.Parallel()
	srv, _ := server(t)
	tests := []struct {
		name, auth string
		code       connect.Code
	}{
		{"no token", "", connect.CodeUnauthenticated},
		{"wrong token", "Bearer " + strings.Repeat("x", len(testToken)), connect.CodeUnauthenticated},
		{"token prefix", "Bearer " + testToken[:len(testToken)-1], connect.CodeUnauthenticated},
		{"basic scheme", "Basic " + testToken, connect.CodeUnauthenticated},
		{"bare token", testToken, connect.CodeUnauthenticated},
		{"right token", "Bearer " + testToken, 0},
		{"lowercase scheme", "bearer  " + testToken + " ", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client(srv, tc.auth).CreateTenant(context.Background(),
				&accountsv1.CreateTenantRequest{Name: "Acme"})
			if tc.code == 0 {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if got := connect.CodeOf(err); got != tc.code {
				t.Fatalf("code = %v (%v), want %v", got, err, tc.code)
			}
		})
	}
}

func TestCreateKeyReturnsSecretOnce(t *testing.T) {
	t.Parallel()
	srv, _ := server(t)
	c := authed(srv)
	ctx := context.Background()
	tid := createTenant(t, c)

	res, err := c.CreateApiKey(ctx, &accountsv1.CreateApiKeyRequest{
		Actor: dev, TenantId: tid, Environment: eventsv1.Environment_ENVIRONMENT_TEST, Name: "ci",
		AllowedIps: []string{"10.0.0.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	secret := res.GetSecret()
	if !keyRegex.MatchString(secret) {
		t.Fatalf("secret %q does not match the key format", secret)
	}
	k := res.GetApiKey()
	if !strings.HasPrefix(k.GetId(), "ak_") || k.GetTenantId() != tid || k.GetName() != "ci" ||
		k.GetEnvironment() != eventsv1.Environment_ENVIRONMENT_TEST || !strings.HasPrefix(secret, k.GetDisplayPrefix()) ||
		len(k.GetScopes()) != 4 || len(k.GetAllowedIps()) != 1 || k.GetAllowedIps()[0] != "10.0.0.1/32" ||
		k.GetCreatedAt() == nil || k.GetExpiresAt() != nil || k.GetRevokedAt() != nil {
		t.Fatalf("api key = %v", k)
	}

	list, err := c.ListApiKeys(ctx, &accountsv1.ListApiKeysRequest{
		Actor:    &accountsv1.Actor{Type: "console_user", Id: "usr_2", Role: "support"},
		TenantId: tid, Environment: eventsv1.Environment_ENVIRONMENT_TEST,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.GetApiKeys()) != 1 || list.GetApiKeys()[0].GetId() != k.GetId() {
		t.Fatalf("list = %v", list)
	}
	raw, err := proto.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte(secret[len(k.GetDisplayPrefix()):])) {
		t.Fatal("ListApiKeys response contains the secret")
	}
	fields := k.ProtoReflect().Descriptor().Fields()
	for i := range fields.Len() {
		name := string(fields.Get(i).Name())
		if strings.Contains(name, "hash") || strings.Contains(name, "secret") || strings.Contains(name, "lookup") {
			t.Errorf("ApiKey has field %q", name)
		}
	}
}

func TestErrorMapping(t *testing.T) {
	t.Parallel()
	srv, _ := server(t)
	c := authed(srv)
	tid := createTenant(t, c)
	live := eventsv1.Environment_ENVIRONMENT_LIVE

	tests := []struct {
		name   string
		call   func(ctx context.Context) error
		code   connect.Code
		dtCode string
	}{
		{"developer creates live key", func(ctx context.Context) error {
			_, err := c.CreateApiKey(ctx, &accountsv1.CreateApiKeyRequest{Actor: dev, TenantId: tid, Environment: live, Name: "x"})
			return err
		}, connect.CodePermissionDenied, "forbidden_role"},
		{"live not enabled", func(ctx context.Context) error {
			_, err := c.CreateApiKey(ctx, &accountsv1.CreateApiKeyRequest{Actor: owner, TenantId: tid, Environment: live, Name: "x"})
			return err
		}, connect.CodeFailedPrecondition, "live_not_enabled"},
		{"unknown scope", func(ctx context.Context) error {
			_, err := c.CreateApiKey(ctx, &accountsv1.CreateApiKeyRequest{Actor: owner, TenantId: tid,
				Environment: eventsv1.Environment_ENVIRONMENT_TEST, Name: "x", Scopes: []string{"admin:all"}})
			return err
		}, connect.CodeInvalidArgument, "invalid_scope"},
		{"missing environment", func(ctx context.Context) error {
			_, err := c.CreateApiKey(ctx, &accountsv1.CreateApiKeyRequest{Actor: owner, TenantId: tid, Name: "x"})
			return err
		}, connect.CodeInvalidArgument, "invalid_argument"},
		{"unknown key", func(ctx context.Context) error {
			_, err := c.RevokeApiKey(ctx, &accountsv1.RevokeApiKeyRequest{Actor: owner, TenantId: tid, KeyId: "ak_missing"})
			return err
		}, connect.CodeNotFound, "not_found"},
		{"unknown tenant", func(ctx context.Context) error {
			_, err := c.SetLiveEnabled(ctx, &accountsv1.SetLiveEnabledRequest{TenantId: "ten_missing", Enabled: true})
			return err
		}, connect.CodeNotFound, "not_found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, info := connect.NewClientContext(context.Background())
			err := tc.call(ctx)
			if got := connect.CodeOf(err); got != tc.code {
				t.Fatalf("code = %v (%v), want %v", got, err, tc.code)
			}
			if got := dtCode(info); got != tc.dtCode {
				t.Fatalf("dt-code = %q, want %q", got, tc.dtCode)
			}
		})
	}

	// A revoked key cannot be rolled.
	ctx := context.Background()
	k, err := c.CreateApiKey(ctx, &accountsv1.CreateApiKeyRequest{Actor: owner, TenantId: tid,
		Environment: eventsv1.Environment_ENVIRONMENT_TEST, Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	id := k.GetApiKey().GetId()
	if _, err := c.RevokeApiKey(ctx, &accountsv1.RevokeApiKeyRequest{Actor: owner, TenantId: tid, KeyId: id}); err != nil {
		t.Fatal(err)
	}
	ctx, info := connect.NewClientContext(ctx)
	_, err = c.RollApiKey(ctx, &accountsv1.RollApiKeyRequest{Actor: owner, TenantId: tid, KeyId: id})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || dtCode(info) != "key_revoked" {
		t.Fatalf("roll revoked: %v, dt-code %q", err, dtCode(info))
	}
}

// dtCode reads the dt-code error metadata (sent as a trailer).
func dtCode(info *connect.CallInfo) string { return info.ResponseTrailer().Get(rpcauth.CodeKey) }

func TestErrorMapperHidesInternalErrors(t *testing.T) {
	err := rpc.ToConnect(context.Background(), slog.New(slog.DiscardHandler), errors.New("pq: secret detail"))
	var cerr *connect.Error
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInternal {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(cerr.Message(), "secret detail") {
		t.Fatalf("message leaks the cause: %q", cerr.Message())
	}
}

func TestRollReturnsNewSecretAndExpiresOld(t *testing.T) {
	t.Parallel()
	srv, _ := server(t)
	c := authed(srv)
	ctx := context.Background()
	tid := createTenant(t, c)
	if _, err := c.SetLiveEnabled(ctx, &accountsv1.SetLiveEnabledRequest{TenantId: tid, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	created, err := c.CreateApiKey(ctx, &accountsv1.CreateApiKeyRequest{Actor: owner, TenantId: tid,
		Environment: eventsv1.Environment_ENVIRONMENT_LIVE, Name: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	rolled, err := c.RollApiKey(ctx, &accountsv1.RollApiKeyRequest{Actor: owner, TenantId: tid, KeyId: created.GetApiKey().GetId()})
	if err != nil {
		t.Fatal(err)
	}
	if !keyRegex.MatchString(rolled.GetSecret()) || rolled.GetSecret() == created.GetSecret() ||
		!strings.HasPrefix(rolled.GetSecret(), "dt_sk_live_") {
		t.Fatal("roll did not return a new live secret")
	}
	if rolled.GetPrevious().GetId() != created.GetApiKey().GetId() || rolled.GetPrevious().GetExpiresAt() == nil ||
		rolled.GetApiKey().GetId() == created.GetApiKey().GetId() || rolled.GetApiKey().GetName() != "prod" {
		t.Fatalf("roll = %v", rolled)
	}
}

func TestRevokeEndToEndPublishes(t *testing.T) {
	t.Parallel()
	srv, pool := server(t)
	nurl := func() string {
		starting <- struct{}{}
		defer func() { <-starting }()
		return testkit.StartNATS(t)
	}()
	nc, err := nats.Connect(nurl)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := outbox.EnsureStream(ctx, js, outbox.StreamAccounts); err != nil {
		t.Fatal(err)
	}
	relayDone := make(chan error, 1)
	go func() {
		relay := &outbox.Relay{Pool: pool, JS: js, Interval: 50 * time.Millisecond}
		relayDone <- relay.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-relayDone; err != nil {
			t.Errorf("relay: %v", err)
		}
	})

	c := authed(srv)
	tid := createTenant(t, c)
	k, err := c.CreateApiKey(ctx, &accountsv1.CreateApiKeyRequest{Actor: owner, TenantId: tid,
		Environment: eventsv1.Environment_ENVIRONMENT_TEST, Name: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	cons, err := js.OrderedConsumer(ctx, outbox.StreamAccounts.Name, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{outbox.SubjectFor(outbox.StreamAccounts, app.EventKeyRevoked)},
	})
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if _, err := c.RevokeApiKey(ctx, &accountsv1.RevokeApiKeyRequest{Actor: owner, TenantId: tid, KeyId: k.GetApiKey().GetId()}); err != nil {
		t.Fatal(err)
	}
	msg, err := cons.Next(jetstream.FetchMaxWait(time.Second))
	if err != nil {
		t.Fatalf("no apikey.revoked message within 1s: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("revoked event took %v", d)
	}
	if msg.Subject() != "accounts.apikey.revoked" {
		t.Fatalf("subject = %q", msg.Subject())
	}
	var env eventsv1.Envelope
	if err := proto.Unmarshal(msg.Data(), &env); err != nil {
		t.Fatal(err)
	}
	if msg.Headers().Get(jetstream.MsgIDHeader) != env.GetEventId() {
		t.Errorf("Nats-Msg-Id = %q, want %q", msg.Headers().Get(jetstream.MsgIDHeader), env.GetEventId())
	}
	var revoked eventsv1.ApiKeyRevoked
	if err := env.GetPayload().UnmarshalTo(&revoked); err != nil {
		t.Fatal(err)
	}
	if env.GetTenantId() != tid || revoked.GetKeyId() != k.GetApiKey().GetId() || revoked.GetRevokedAt() == nil {
		t.Fatalf("event = %v / %v", &env, &revoked)
	}
	if bytes.Contains(msg.Data(), []byte(k.GetSecret())) {
		t.Fatal("event contains the plaintext key")
	}
}

func TestSetTenantSettingsRPC(t *testing.T) {
	t.Parallel()
	srv, pool := server(t)
	c := authed(srv)
	ctx := context.Background()

	created, err := c.CreateTenant(ctx, &accountsv1.CreateTenantRequest{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	tid := created.GetTenant().GetId()
	def := created.GetTenant().GetSettings()
	if def == nil || strings.Join(def.GetAllowedModels(), ",") != "direct,end_to_end" || def.GetDefaultModel() != "direct" ||
		def.GetPickupConfirmation() != "any" || def.GetUpdatedAt() == nil {
		t.Fatalf("created tenant settings = %v", def)
	}

	bad := []*accountsv1.SetTenantSettingsRequest{
		{Actor: owner, TenantId: tid, AllowedModels: []string{"direct"}, DefaultModel: "direct", PickupConfirmation: "nobody"},
		{Actor: owner, TenantId: tid, AllowedModels: []string{"end_to_end"}, DefaultModel: "direct", PickupConfirmation: "any"},
		{Actor: owner, TenantId: tid, DefaultModel: "direct", PickupConfirmation: "any"},
	}
	for _, req := range bad {
		cctx, info := connect.NewClientContext(ctx)
		_, err := c.SetTenantSettings(cctx, req)
		if connect.CodeOf(err) != connect.CodeInvalidArgument || dtCode(info) != "invalid_argument" {
			t.Fatalf("%v: code %v, dt-code %q", req, err, dtCode(info))
		}
	}

	for _, tc := range []struct {
		name   string
		req    *accountsv1.SetTenantSettingsRequest
		code   connect.Code
		dtCode string
	}{
		{"developer", &accountsv1.SetTenantSettingsRequest{Actor: dev, TenantId: tid, AllowedModels: []string{"direct"}, DefaultModel: "direct", PickupConfirmation: "any"},
			connect.CodePermissionDenied, "forbidden_role"},
		{"unknown tenant", &accountsv1.SetTenantSettingsRequest{Actor: owner, TenantId: "ten_missing", AllowedModels: []string{"direct"}, DefaultModel: "direct", PickupConfirmation: "any"},
			connect.CodeNotFound, "not_found"},
	} {
		cctx, info := connect.NewClientContext(ctx)
		_, err := c.SetTenantSettings(cctx, tc.req)
		if connect.CodeOf(err) != tc.code || dtCode(info) != tc.dtCode {
			t.Fatalf("%s: %v, dt-code %q", tc.name, err, dtCode(info))
		}
	}

	res, err := c.SetTenantSettings(ctx, &accountsv1.SetTenantSettingsRequest{
		Actor: owner, TenantId: tid, AllowedModels: []string{"end_to_end"}, DefaultModel: "end_to_end", PickupConfirmation: "courier",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := res.GetSettings()
	if strings.Join(got.GetAllowedModels(), ",") != "end_to_end" || got.GetDefaultModel() != "end_to_end" ||
		got.GetPickupConfirmation() != "courier" || !got.GetUpdatedAt().AsTime().After(def.GetUpdatedAt().AsTime()) {
		t.Fatalf("settings = %v", got)
	}

	// Creation and the update each enqueued a tenant.settings_set event.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE subject = 'accounts.tenant.settings_set'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("settings events = %d, want 2", n)
	}
}

func TestSetTenantSettingsEmbedAndBrandingRPC(t *testing.T) {
	t.Parallel()
	srv, pool := server(t)
	c := authed(srv)
	ctx := context.Background()
	created, err := c.CreateTenant(ctx, &accountsv1.CreateTenantRequest{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	tid := created.GetTenant().GetId()
	if def := created.GetTenant().GetSettings(); len(def.GetEmbedOrigins()) != 0 || def.GetBranding() != nil {
		t.Fatalf("default settings = %v", def)
	}
	var logo bytes.Buffer
	// ~90 KB of noise: bigger than the old 64 KB request limit, under the 100 KB logo cap.
	noise := image.NewGray(image.Rect(0, 0, 300, 300))
	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // deterministic test fixture
	for i := range noise.Pix {
		noise.Pix[i] = byte(rng.Uint32())
	}
	if err := png.Encode(&logo, noise); err != nil {
		t.Fatal(err)
	}
	req := &accountsv1.SetTenantSettingsRequest{
		Actor: owner, TenantId: tid, AllowedModels: []string{"direct"}, DefaultModel: "direct", PickupConfirmation: "any",
		EmbedOrigins: []string{"https://shop.example.com", "https://*.example.org:8443"},
		// The caller's content type is ignored: it is derived from the bytes.
		Branding: &accountsv1.Branding{DisplayName: "Acme Foods", PrimaryColor: "#0A84FF", Theme: "dark",
			Logo: logo.Bytes(), LogoContentType: "image/webp"},
	}

	badReq := &accountsv1.SetTenantSettingsRequest{Actor: owner, TenantId: tid, AllowedModels: []string{"direct"},
		DefaultModel: "direct", PickupConfirmation: "any", EmbedOrigins: []string{"http://x.com"}}
	if _, err := c.SetTenantSettings(ctx, badReq); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad origin: %v", err)
	}

	res, err := c.SetTenantSettings(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	check := func(what string, s *accountsv1.TenantSettings, b *accountsv1.Branding, origins []string) {
		t.Helper()
		if !reflect.DeepEqual(origins, s.GetEmbedOrigins()) {
			t.Fatalf("%s: embed_origins = %v", what, s.GetEmbedOrigins())
		}
		if got := s.GetBranding(); got.GetDisplayName() != b.GetDisplayName() || got.GetPrimaryColor() != b.GetPrimaryColor() ||
			got.GetTheme() != b.GetTheme() || !bytes.Equal(got.GetLogo(), b.GetLogo()) || got.GetLogoContentType() != b.GetLogoContentType() {
			t.Fatalf("%s: branding = %v", what, got.GetDisplayName())
		}
	}
	want := &accountsv1.Branding{DisplayName: "Acme Foods", PrimaryColor: "#0A84FF", Theme: "dark",
		Logo: logo.Bytes(), LogoContentType: "image/png"}
	check("response", res.GetSettings(), want, req.GetEmbedOrigins())
	// The newest tenant.settings_set event carries both fields, logo included.
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM outbox WHERE subject = 'accounts.tenant.settings_set' ORDER BY created_at DESC LIMIT 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var env eventsv1.Envelope
	if err := proto.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var ev eventsv1.TenantSettingsSet
	if err := env.GetPayload().UnmarshalTo(&ev); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ev.GetEmbedOrigins(), req.GetEmbedOrigins()) || ev.GetBranding().GetDisplayName() != "Acme Foods" ||
		ev.GetBranding().GetPrimaryColor() != "#0A84FF" || ev.GetBranding().GetTheme() != "dark" ||
		!bytes.Equal(ev.GetBranding().GetLogo(), logo.Bytes()) || ev.GetBranding().GetLogoContentType() != "image/png" {
		t.Fatalf("event = %v", &ev)
	}

	// Omitting them replaces the settings whole; an all-empty branding is DT defaults.
	req.EmbedOrigins, req.Branding = nil, &accountsv1.Branding{}
	res, err = c.SetTenantSettings(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.GetSettings().GetEmbedOrigins()) != 0 || res.GetSettings().GetBranding() != nil {
		t.Fatalf("cleared settings = %v", res.GetSettings())
	}
}
