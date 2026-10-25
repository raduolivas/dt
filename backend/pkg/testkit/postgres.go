// Package testkit starts real dependencies for integration tests.
package testkit

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// ServiceNames are the day-one services, each with its own schema and role.
var ServiceNames = []string{"gateway", "deliveries", "tracking", "webhooks", "accounts"}

// PostgresImage matches the local stack (PostgreSQL 18 + PostGIS 3.6).
const PostgresImage = "postgis/postgis:18-3.6"

const (
	adminUser = "dt_admin"
	adminPass = "dt_admin"
	database  = "dt"
)

// Postgres is a running database initialised with the dev/test init script.
type Postgres struct {
	host string
	port string
}

// StartPostgres starts a container and terminates it when t finishes.
// It skips under -short, so unit runs need no Docker.
func StartPostgres(t testing.TB) *Postgres {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test: needs Docker (run without -short)")
	}
	ctx := context.Background()
	initDir := filepath.Join(RepoRoot(t), "infra/compose/postgres/init")
	c, err := postgres.Run(ctx, PostgresImage,
		// 10_postgis.sh overrides the image's own hook of the same name (see the file).
		postgres.WithInitScripts(
			filepath.Join(initDir, "01-schemas.sql"),
			filepath.Join(initDir, "10_postgis.sh"),
		),
		postgres.WithDatabase(database),
		postgres.WithUsername(adminUser),
		postgres.WithPassword(adminPass),
		postgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("postgres host: %v", err)
	}
	port, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("postgres port: %v", err)
	}
	return &Postgres{host: host, port: port.Port()}
}

// URL returns a DSN for database dt as role: "admin" or a service name
// (its runtime role svc_<svc>).
func (p *Postgres) URL(role string) string {
	if role == "admin" {
		return p.dsn(adminUser, adminPass)
	}
	return p.dsn("svc_"+role, "svc_"+role)
}

// MigratorURL returns a DSN for database dt as mig_<svc>, the role that owns
// the service's schema and runs its migrations.
func (p *Postgres) MigratorURL(svc string) string {
	return p.dsn("mig_"+svc, "mig_"+svc)
}

func (p *Postgres) dsn(user, pass string) string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, pass),
		Host:     net.JoinHostPort(p.host, p.port),
		Path:     "/" + database,
		RawQuery: "sslmode=disable",
	}
	return u.String()
}

// RepoRoot walks up from the working directory to the one containing .git.
func RepoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal(fmt.Errorf("testkit: no .git above working directory"))
		}
		dir = parent
	}
}
