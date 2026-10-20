package testkit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/raduolivas/dt/backend/pkg/testkit"
)

func connect(t *testing.T, pg *testkit.Postgres, role string) *pgx.Conn {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, pg.URL(role))
	if err != nil {
		t.Fatalf("connect as %s: %v", role, err)
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
		conn := connect(t, pg, "tracking")
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

	t.Run("CrossSchemaReadDenied", func(t *testing.T) {
		exec(t, connect(t, pg, "deliveries"), "CREATE TABLE t (id int); INSERT INTO t VALUES (1)")
		_, err := connect(t, pg, "accounts").Exec(ctx, "SELECT * FROM deliveries.t")
		wantDenied(t, err)
	})

	t.Run("CrossSchemaWriteDenied", func(t *testing.T) {
		_, err := connect(t, pg, "accounts").Exec(ctx, "CREATE TABLE deliveries.z (id int)")
		wantDenied(t, err)
	})

	t.Run("PublicSchemaLocked", func(t *testing.T) {
		_, err := connect(t, pg, "gateway").Exec(ctx, "CREATE TABLE public.x (id int)")
		wantDenied(t, err)
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
