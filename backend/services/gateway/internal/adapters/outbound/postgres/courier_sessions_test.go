package postgres_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/courierjwt"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app/apptest"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

const csCourier = "cou_01j9z3k8m2q4r6s8t0v2w4x6y1"

var (
	csT0      = time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	csPartner = domain.Principal{TenantID: "ten_a", Env: domain.EnvLive, CredentialID: "ak_1", Credential: domain.CredentialSecretKey}
)

func courierService(t *testing.T) (*app.CourierSessions, *pgxpool.Pool, *apptest.MemCourierDeny) {
	t.Helper()
	pool, _ := setup(t)
	seed := bytes.Repeat([]byte{7}, 32)
	signer, err := courierjwt.ParseKeys("k1:"+base64.StdEncoding.EncodeToString(seed), "k1")
	if err != nil {
		t.Fatal(err)
	}
	couriers := &apptest.MemCouriers{}
	couriers.Add(csPartner.TenantID, csPartner.Env, csCourier)
	deny := &apptest.MemCourierDeny{}
	return &app.CourierSessions{Repo: postgres.NewCourierSessionStore(pool), Hasher: apptest.Hasher{}, Signer: signer,
		Deny: deny, Couriers: couriers, Audit: &apptest.MemAudit{}, Clock: &fixedNow{t: csT0}}, pool, deny
}

// race runs fn from n goroutines at once and returns their errors.
func race(n int, fn func() error) []error {
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start
			errs[i] = fn()
		})
	}
	close(start)
	wg.Wait()
	return errs
}

func TestExchangeConcurrentOnlyOneWins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, _ := courierService(t)
	sdk, err := svc.Start(ctx, csPartner, csCourier, "sdk", 0)
	if err != nil {
		t.Fatal(err)
	}
	link, err := svc.Start(ctx, csPartner, csCourier, "link", 0)
	if err != nil {
		t.Fatal(err)
	}
	errs := race(10, func() error {
		_, err := svc.Exchange(ctx, link.LinkCode, "")
		return err
	})
	won := 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, app.ErrLinkCodeUsed):
			t.Errorf("loser error = %v, want ErrLinkCodeUsed", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d exchanges won, want exactly 1", won)
	}
	s, err := svc.Get(ctx, csPartner, link.Session.ID)
	if err != nil || s.State != domain.SessionActive {
		t.Fatalf("exchanged session = %+v, %v", s, err)
	}
	if old, _ := svc.Get(ctx, csPartner, sdk.Session.ID); old.State != domain.SessionEnded || old.EndReason != domain.EndReplaced {
		t.Fatalf("replaced session = %+v", old)
	}
}

func TestRefreshConcurrentOnlyOneWins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, _ := courierService(t)
	r, err := svc.Start(ctx, csPartner, csCourier, "sdk", 0)
	if err != nil {
		t.Fatal(err)
	}
	errs := race(10, func() error {
		_, err := svc.Refresh(ctx, r.Tokens.RefreshToken)
		return err
	})
	won, reuse := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, app.ErrRefreshReuse):
			reuse++
		case !errors.Is(err, app.ErrSessionEnded):
			t.Errorf("loser error = %v", err)
		}
	}
	// One rotation wins; the first loser presents a superseded token, which
	// is reuse and ends the session; the rest find it ended.
	if won != 1 || reuse != 1 {
		t.Fatalf("won=%d reuse=%d, want 1 and 1 (errors %v)", won, reuse, errs)
	}
	s, err := svc.Get(ctx, csPartner, r.Session.ID)
	if err != nil || s.State != domain.SessionEnded || s.EndReason != domain.EndRefreshReuse || s.RefreshGeneration != 1 {
		t.Fatalf("session = %+v, %v", s, err)
	}
}

func TestRefreshReuseOfAnyEarlierGeneration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, deny := courierService(t)
	r, err := svc.Start(ctx, csPartner, csCourier, "sdk", 0)
	if err != nil {
		t.Fatal(err)
	}
	tokens := []string{r.Tokens.RefreshToken}
	for range 3 {
		next, err := svc.Refresh(ctx, tokens[len(tokens)-1])
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, next.RefreshToken)
	}
	// Generation 1 of 4: still detected.
	if _, err := svc.Refresh(ctx, tokens[1]); !errors.Is(err, app.ErrRefreshReuse) {
		t.Fatalf("old generation: %v", err)
	}
	if denied, _ := deny.Denied(ctx, r.Session.ID); !denied {
		t.Fatal("reuse did not deny-list the session")
	}
	if _, err := svc.Refresh(ctx, tokens[3]); !errors.Is(err, app.ErrSessionEnded) {
		t.Fatalf("current token after reuse: %v", err)
	}
}

func TestOneActivePerCourierIndex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, _ := setup(t)
	ins := func(id, status string, hash byte) error {
		h := bytes.Repeat([]byte{hash}, 32)
		var started, expires *time.Time
		var refresh []byte
		if status == "active" {
			started, expires, refresh = &csT0, &csT0, h
		}
		_, err := pool.Exec(ctx, `INSERT INTO courier_sessions (id, tenant_id, environment, courier_id, mode, status,
			created_at, started_at, expires_at, expires_in_seconds, link_code_hash, link_expires_at, refresh_hash)
			VALUES ($1, 'ten_a', 'live', $2, 'link', $3, $4, $5, $6, 900, $7, $4, $8)`,
			id, csCourier, status, csT0, started, expires, h, refresh)
		return err
	}
	unique := func(err error) bool {
		var pg *pgconn.PgError
		return errors.As(err, &pg) && pg.Code == "23505"
	}
	if err := ins("cs_1", "active", 1); err != nil {
		t.Fatal(err)
	}
	if err := ins("cs_2", "active", 2); !unique(err) {
		t.Fatalf("second active session: %v", err)
	}
	if err := ins("cs_3", "pending_link", 3); err != nil {
		t.Fatal(err)
	}
	if err := ins("cs_4", "pending_link", 4); !unique(err) {
		t.Fatalf("second pending link: %v", err)
	}
	// Once the active session has ended, the courier may have a new one.
	if _, err := pool.Exec(ctx, `UPDATE courier_sessions SET status = 'ended', ended_at = $1, end_reason = 'partner'
		WHERE id = 'cs_1'`, csT0); err != nil {
		t.Fatal(err)
	}
	if err := ins("cs_5", "active", 5); err != nil {
		t.Fatalf("active after the first ended: %v", err)
	}
}

func TestCourierSessionSecretsAndEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, pool, _ := courierService(t)
	link, err := svc.Start(ctx, csPartner, csCourier, "link", 0)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := svc.Exchange(ctx, link.LinkCode, "")
	if err != nil {
		t.Fatal(err)
	}
	next, err := svc.Refresh(ctx, tok.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.End(ctx, csPartner, link.Session.ID, "partner"); err != nil {
		t.Fatal(err)
	}
	// Only hashes are stored: no secret appears anywhere in either table.
	var rows string
	if err := pool.QueryRow(ctx, `SELECT (SELECT string_agg(s::text, '') FROM courier_sessions s) ||
		(SELECT string_agg(u::text, '') FROM courier_refresh_used u)`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{link.LinkCode, tok.RefreshToken, next.RefreshToken, tok.AccessToken} {
		if bytes.Contains([]byte(rows), []byte(secret)) {
			t.Fatalf("plaintext secret stored: %s", rows)
		}
	}
	var linkHash []byte
	if err := pool.QueryRow(ctx, `SELECT link_code_hash FROM courier_sessions WHERE id = $1`, link.Session.ID).Scan(&linkHash); err != nil ||
		!bytes.Equal(linkHash, apptest.Hasher{}.HashCourierLink(link.LinkCode)) {
		t.Fatalf("link_code_hash = %x, %v", linkHash, err)
	}

	// One started and one ended event, on the gateway stream, ids only.
	r, err := pool.Query(ctx, `SELECT subject, payload FROM outbox ORDER BY created_at, event_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var subjects []string
	var started eventsv1.CourierSessionStarted
	var ended eventsv1.CourierSessionEnded
	for r.Next() {
		var subject string
		var payload []byte
		if err := r.Scan(&subject, &payload); err != nil {
			t.Fatal(err)
		}
		subjects = append(subjects, subject)
		var env eventsv1.Envelope
		if err := proto.Unmarshal(payload, &env); err != nil {
			t.Fatal(err)
		}
		if env.GetTenantId() != "ten_a" || env.GetEnvironment() != eventsv1.Environment_ENVIRONMENT_LIVE ||
			env.GetSource() != "gateway" || env.GetSubject().GetCourierId() != csCourier {
			t.Fatalf("envelope = %v", &env)
		}
		for _, secret := range []string{link.LinkCode, tok.RefreshToken, tok.AccessToken} {
			if bytes.Contains(payload, []byte(secret)) {
				t.Fatal("event carries a secret")
			}
		}
		switch subject {
		case "gateway.courier.session_started":
			if err := env.GetPayload().UnmarshalTo(&started); err != nil {
				t.Fatal(err)
			}
		case "gateway.courier.session_ended":
			if err := env.GetPayload().UnmarshalTo(&ended); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
	if len(subjects) != 2 || subjects[0] != "gateway.courier.session_started" || subjects[1] != "gateway.courier.session_ended" {
		t.Fatalf("subjects = %v", subjects)
	}
	if started.GetSessionId() != link.Session.ID || started.GetCourierId() != csCourier || started.GetMode() != "link" ||
		ended.GetSessionId() != link.Session.ID || ended.GetCourierId() != csCourier || ended.GetReason() != "partner" {
		t.Fatalf("started = %v, ended = %v", &started, &ended)
	}
}

func TestCourierSessionTouch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, _ := courierService(t)
	r, err := svc.Start(ctx, csPartner, csCourier, "sdk", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Duration{0, 30 * time.Second, 61 * time.Second} {
		if err := svc.Touch(ctx, r.Session.ID, csT0.Add(at)); err != nil {
			t.Fatal(err)
		}
	}
	s, err := svc.Get(ctx, csPartner, r.Session.ID)
	if err != nil || s.LastSeenAt == nil || !s.LastSeenAt.Equal(csT0.Add(61*time.Second)) {
		t.Fatalf("last seen = %v, %v", s.LastSeenAt, err)
	}
}
