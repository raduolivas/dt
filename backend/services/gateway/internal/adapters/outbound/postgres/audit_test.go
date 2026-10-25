package postgres_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/raduolivas/dt/backend/pkg/ids"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
)

func auditSetup(t *testing.T) (*pgxpool.Pool, *postgres.AuditStore) {
	t.Helper()
	pool, _ := setup(t)
	return pool, postgres.NewAuditStore(pool)
}

func auditEntry(at time.Time) app.AuditEntry {
	return app.AuditEntry{
		TenantID: "ten_a", Env: "test", ActorType: "secret_key", ActorID: "ak_1",
		Action: "things.create", Target: "thg_1", Status: 201,
		IP: netip.MustParseAddr("198.51.100.9"), UserAgent: "curl/8", RequestID: "req_1", At: at,
	}
}

func auditCount(t *testing.T, pool *pgxpool.Pool, where string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_log WHERE "+where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAuditRecordRoundTrip(t *testing.T) {
	t.Parallel()
	pool, s := auditSetup(t)
	ctx := context.Background()

	if err := s.Record(ctx, auditEntry(t0)); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// Optional fields left empty are stored as NULL.
	bare := app.AuditEntry{
		TenantID: "ten_a", Env: "live", ActorType: "secret_key", ActorID: "ak_2",
		Action: "things.delete", Status: 500, RequestID: "req_2", At: t0,
	}
	if err := s.Record(ctx, bare); err != nil {
		t.Fatalf("Record bare: %v", err)
	}

	var (
		id, tenant, env, actorType, actorID, action, reqID string
		target, ua                                         *string
		status                                             int
		ip                                                 *netip.Addr
		at                                                 time.Time
	)
	err := pool.QueryRow(ctx, `SELECT id, tenant_id, environment, actor_type, actor_id, action, target,
		status, ip, user_agent, request_id, occurred_at FROM audit_log WHERE actor_id = 'ak_1'`).
		Scan(&id, &tenant, &env, &actorType, &actorID, &action, &target, &status, &ip, &ua, &reqID, &at)
	if err != nil {
		t.Fatal(err)
	}
	if !ids.Valid(ids.Audit, id) {
		t.Fatalf("id = %q, want an aud_ id", id)
	}
	if tenant != "ten_a" || env != "test" || actorType != "secret_key" || action != "things.create" ||
		target == nil || *target != "thg_1" || status != 201 || ip == nil || ip.String() != "198.51.100.9" ||
		ua == nil || *ua != "curl/8" || reqID != "req_1" || !at.Equal(t0) {
		t.Fatalf("row = %s %s %s %s %v %d %v %v %s %v", tenant, env, actorType, action, target, status, ip, ua, reqID, at)
	}

	if err := pool.QueryRow(ctx, `SELECT target, ip, user_agent FROM audit_log WHERE actor_id = 'ak_2'`).
		Scan(&target, &ip, &ua); err != nil {
		t.Fatal(err)
	}
	if target != nil || ip != nil || ua != nil {
		t.Fatalf("bare row: target %v ip %v ua %v, want NULLs", target, ip, ua)
	}
}

func TestAuditLogIsAppendOnly(t *testing.T) {
	t.Parallel()
	pool, s := auditSetup(t) // the pool connects as svc_gateway, not the table owner
	ctx := context.Background()
	if err := s.Record(ctx, auditEntry(t0)); err != nil {
		t.Fatal(err)
	}
	// Not the owner, the role cannot grant itself UPDATE back (PostgreSQL only
	// warns), so the UPDATE below is still refused.
	if _, err := pool.Exec(ctx, "GRANT UPDATE, TRUNCATE ON audit_log TO CURRENT_USER"); err != nil {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("GRANT = %v", err)
		}
	}
	for _, stmt := range []string{"UPDATE audit_log SET status = 200", "TRUNCATE audit_log"} {
		_, err := pool.Exec(ctx, stmt)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("%s = %v, want permission denied (42501)", stmt, err)
		}
	}
	if got := auditCount(t, pool, "true"); got != 1 {
		t.Fatalf("rows = %d after the refused statements, want 1", got)
	}
	// INSERT, SELECT and DELETE stay with the role (DELETE for retention).
	if _, err := pool.Exec(ctx, "DELETE FROM audit_log WHERE actor_id = 'nobody'"); err != nil {
		t.Fatalf("DELETE: %v", err)
	}
}

func TestAuditRetentionDeletesOlderThan365Days(t *testing.T) {
	t.Parallel()
	pool, s := auditSetup(t)
	ctx := context.Background()
	year := 365 * 24 * time.Hour

	// 2500 rows past retention (several batches), and rows at and inside it.
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_log (id, tenant_id, environment, actor_type, actor_id, action, status, request_id, occurred_at)
		SELECT 'aud_old' || g, 'ten_a', 'test', 'secret_key', 'ak_old', 'x', 200, 'r', $1
		FROM generate_series(1, 2500) g`, t0.Add(-year-time.Hour)); err != nil {
		t.Fatal(err)
	}
	for i, at := range []time.Time{t0.Add(-year), t0.Add(-year + time.Hour), t0} {
		e := auditEntry(at)
		e.ActorID = "ak_keep"
		e.RequestID = "req_keep_" + string(rune('a'+i))
		if err := s.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	n, err := s.DeleteAuditBefore(ctx, t0.Add(-year), 1000)
	if err != nil || n != 1000 {
		t.Fatalf("DeleteAuditBefore = %d, %v; want one batch of 1000", n, err)
	}

	r := &app.AuditRetention{Store: s, Clock: fixedClock{t0}, Interval: time.Hour}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- r.Run(runCtx) }()
	deadline := time.Now().Add(10 * time.Second)
	for auditCount(t, pool, "actor_id = 'ak_old'") > 0 {
		if time.Now().After(deadline) {
			t.Fatal("retention left rows older than 365 days")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil on cancel", err)
	}
	if got := auditCount(t, pool, "actor_id = 'ak_keep'"); got != 3 {
		t.Fatalf("rows within retention left = %d, want 3", got)
	}
}
