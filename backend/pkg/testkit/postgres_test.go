package testkit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/raduolivas/dt/backend/pkg/testkit"
)

// connect opens a connection as role's runtime role (svc_<role>, or admin).
func connect(t *testing.T, pg *testkit.Postgres, role string) *pgx.Conn {
	t.Helper()
	return dial(t, pg.URL(role))
}

// connectMigrator opens a connection as mig_<svc>, the schema owner.
func connectMigrator(t *testing.T, pg *testkit.Postgres, svc string) *pgx.Conn {
	t.Helper()
	return dial(t, pg.MigratorURL(svc))
}

func dial(t *testing.T, url string) *pgx.Conn {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	return conn
}

func exec(t *testing.T, conn *pgx.Conn, sql string) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func wantDenied(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("want permission denied (42501), got %v", err)
	}
}

// One container serves every case: schema isolation is a security boundary
// (spec §8.4), so each case runs against the real init script.
func TestSchemaIsolation(t *testing.T) {
	pg := testkit.StartPostgres(t)
	ctx := context.Background()

	t.Run("OwnSchemaIsDefault", func(t *testing.T) {
		conn := connectMigrator(t, pg, "tracking")
		exec(t, conn, "CREATE TABLE y (id int)")
		// regclass::text drops the schema when it is on search_path, so ask the catalog.
		var schema string
		if err := conn.QueryRow(ctx, "SELECT schemaname FROM pg_tables WHERE tablename = 'y'").Scan(&schema); err != nil {
			t.Fatal(err)
		}
		if schema != "tracking" {
			t.Fatalf("table y created in %q, want tracking", schema)
		}
	})

	// Tables are created by mig_<svc>; svc_<svc> gets DML on them through
	// default privileges but owns none and cannot create any.
	exec(t, connectMigrator(t, pg, "deliveries"), "CREATE TABLE t (id int); INSERT INTO t VALUES (1)")

	t.Run("RuntimeRoleUsesMigratorTables", func(t *testing.T) {
		conn := connect(t, pg, "deliveries")
		exec(t, conn, "INSERT INTO t VALUES (2)")
		var n int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM t").Scan(&n); err != nil || n != 2 {
			t.Fatalf("count = %d, %v; want 2", n, err)
		}
	})

	t.Run("RuntimeRoleCannotCreateOrAlter", func(t *testing.T) {
		conn := connect(t, pg, "deliveries")
		_, err := conn.Exec(ctx, "CREATE TABLE u (id int)")
		wantDenied(t, err)
		_, err = conn.Exec(ctx, "ALTER TABLE t ADD COLUMN x int")
		wantDenied(t, err)
	})

	t.Run("CrossSchemaReadDenied", func(t *testing.T) {
		for _, conn := range []*pgx.Conn{connect(t, pg, "accounts"), connectMigrator(t, pg, "accounts")} {
			_, err := conn.Exec(ctx, "SELECT * FROM deliveries.t")
			wantDenied(t, err)
		}
	})

	t.Run("CrossSchemaWriteDenied", func(t *testing.T) {
		for _, conn := range []*pgx.Conn{connect(t, pg, "accounts"), connectMigrator(t, pg, "accounts")} {
			_, err := conn.Exec(ctx, "CREATE TABLE deliveries.z (id int)")
			wantDenied(t, err)
		}
	})

	t.Run("PublicSchemaLocked", func(t *testing.T) {
		for _, conn := range []*pgx.Conn{connect(t, pg, "gateway"), connectMigrator(t, pg, "gateway")} {
			_, err := conn.Exec(ctx, "CREATE TABLE public.x (id int)")
			wantDenied(t, err)
		}
	})

	t.Run("PostGISAvailable", func(t *testing.T) {
		var wkt string
		if err := connect(t, pg, "tracking").QueryRow(ctx, "SELECT ST_AsText(ST_MakePoint(4.9, 52.37))").Scan(&wkt); err != nil {
			t.Fatal(err)
		}
		if wkt != "POINT(4.9 52.37)" {
			t.Fatalf("got %q", wkt)
		}
	})
}
