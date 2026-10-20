package migratex_test

import (
	"context"
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
		if err := migratex.Up(ctx, pg.URL("gateway"), fsys); err != nil {
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
	if err := migratex.Up(context.Background(), pg.URL("gateway"), fsys); err == nil {
		t.Fatal("want error")
	}
}
