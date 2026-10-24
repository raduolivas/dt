// Package migratex applies a service's embedded SQL migrations with
// golang-migrate, connected as the migration role (mig_<svc> locally) that
// owns the service's schema. Its search_path puts schema_migrations in that
// schema; golang-migrate's advisory lock keeps concurrent replicas from
// migrating at once.
package migratex

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	// Registers the pgx/v5 database driver ("pgx5" scheme).
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgconn"
)

// errMissingRole is returned when a mig_ role cannot log in: usually a dev
// database initialised before the migration roles existed.
var errMissingRole = errors.New("migratex: cannot open database for migrations " +
	"(does the migration role exist? reset the dev database: docker compose down -v)")

// Up applies every pending migration in the root of migrations. Having
// nothing to apply is success. Errors never include the database URL.
func Up(ctx context.Context, databaseURL string, migrations fs.FS) error {
	src, err := iofs.New(migrations, ".")
	if err != nil {
		return fmt.Errorf("migratex: source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, pgx5URL(databaseURL))
	if err != nil {
		if isMigratorAuthFailure(databaseURL, err) {
			return errMissingRole
		}
		return fmt.Errorf("migratex: connect: %w", redact(err))
	}
	// Stop between migrations when ctx ends.
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			m.GracefulStop <- true
		case <-stop:
		}
	}()
	upErr := m.Up()
	close(stop)
	srcErr, dbErr := m.Close()
	if upErr != nil && !errors.Is(upErr, migrate.ErrNoChange) {
		return fmt.Errorf("migratex: up: %w", upErr)
	}
	if err := errors.Join(srcErr, dbErr); err != nil {
		return fmt.Errorf("migratex: close: %w", err)
	}
	return nil
}

func pgx5URL(u string) string {
	for _, p := range []string{"postgresql://", "postgres://"} {
		if rest, ok := strings.CutPrefix(u, p); ok {
			return "pgx5://" + rest
		}
	}
	return u
}

// isMigratorAuthFailure reports whether err is PostgreSQL rejecting the login
// (SQLSTATE class 28) of a mig_ user.
func isMigratorAuthFailure(databaseURL string, err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || !strings.HasPrefix(pgErr.Code, "28") {
		return false
	}
	u, perr := url.Parse(databaseURL)
	return perr == nil && strings.HasPrefix(u.User.Username(), "mig_")
}

// redact drops driver errors' text, which may echo connection details.
func redact(err error) error {
	return errors.New("cannot open database for migrations")
}
