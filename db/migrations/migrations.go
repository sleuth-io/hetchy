// Package migrations bundles the SQL migrations into the binary so the
// hetchy executable can apply them itself (`hetchy --migrate`) without
// requiring the golang-migrate CLI on the host.
package migrations

import (
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	// Postgres driver is registered as a side effect.
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed *.sql
var sqlFS embed.FS

// Up applies every pending migration to the database at databaseURL. It
// returns nil if the database is already at the latest version.
func Up(databaseURL string) error {
	if databaseURL == "" {
		return errors.New("migrations: DATABASE_URL is empty")
	}
	m, closer, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer closer()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// Down rolls back n migrations. Use 0 to roll back everything.
func Down(databaseURL string, n int) error {
	if databaseURL == "" {
		return errors.New("migrations: DATABASE_URL is empty")
	}
	m, closer, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer closer()
	if n <= 0 {
		if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("migrate down: %w", err)
		}
		return nil
	}
	if err := m.Steps(-n); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate down %d: %w", n, err)
	}
	return nil
}

// Version reports the current schema version. dirty indicates that a prior
// migration failed mid-run and needs manual repair.
func Version(databaseURL string) (version uint, dirty bool, err error) {
	if databaseURL == "" {
		return 0, false, errors.New("migrations: DATABASE_URL is empty")
	}
	m, closer, err := newMigrator(databaseURL)
	if err != nil {
		return 0, false, err
	}
	defer closer()
	v, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("migrate version: %w", err)
	}
	return v, dirty, nil
}

func newMigrator(databaseURL string) (*migrate.Migrate, func(), error) {
	src, err := iofs.New(sqlFS, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("iofs: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, databaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("migrate init: %w", err)
	}
	closer := func() { _, _ = m.Close() }
	return m, closer, nil
}
