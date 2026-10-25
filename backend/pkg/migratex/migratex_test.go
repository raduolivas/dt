package migratex_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"

	"github.com/raduolivas/dt/backend/pkg/migratex"
	"github.com/raduolivas/dt/backend/pkg/testkit"
)

func TestUpAppliesAndIsIdempotent(t *testing.T) {
	pg := testkit.StartPostgres(t)
	fsys := fstest.MapFS{"0001_t.up.sql": {Data: []byte("CREATE TABLE t (id int)")}}
	ctx := context.Background()
	for i := range 2 { // second run is ErrNoChange, which counts as success
		if err := migratex.Up(ctx, pg.MigratorURL("gateway"), fsys); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	conn, err := pgx.Connect(ctx, pg.URL("gateway"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM t").Scan(&n); err != nil {
		t.Fatal(err)
	}
}

func TestUpReportsBadSQL(t *testing.T) {
	pg := testkit.StartPostgres(t)
	fsys := fstest.MapFS{"0001_bad.up.sql": {Data: []byte("NOT SQL")}}
	if err := migratex.Up(context.Background(), pg.MigratorURL("gateway"), fsys); err == nil {
		t.Fatal("want error")
	}
}

// A database created before the mig_ roles existed rejects the login; the
// error says how to fix it and still never echoes the URL.
func TestUpMissingRoleHint(t *testing.T) {
	pg := testkit.StartPostgres(t)
	fsys := fstest.MapFS{"0001_t.up.sql": {Data: []byte("CREATE TABLE t (id int)")}}
	url := pg.MigratorURL("missing")
	err := migratex.Up(context.Background(), url, fsys)
	want := "migratex: cannot open database for migrations (does the migration role exist? reset the dev database: docker compose down -v)"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if strings.Contains(err.Error(), "mig_missing") {
		t.Fatalf("error echoes the URL: %v", err)
	}
}
