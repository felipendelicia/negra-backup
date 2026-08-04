// internal/db/migrate.go
package db

import (
	"fmt"

	natmigrations "github.com/felipendelicia/nat-backup/migrations"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// newMigrator builds a migrate instance over the embedded migrations. The caller
// must call the returned close func.
func newMigrator(databaseURL string) (*migrate.Migrate, func(), error) {
	pool, err := Connect(databaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("connect for migration: %w", err)
	}

	driver, err := postgres.WithInstance(pool.DB, &postgres.Config{})
	if err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("postgres driver: %w", err)
	}

	src, err := iofs.New(natmigrations.FS, ".")
	if err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("migration source: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	if err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("migrate init: %w", err)
	}
	return m, func() { pool.Close() }, nil
}

// RunMigrations applies all pending up migrations. Idempotent.
func RunMigrations(databaseURL string) error {
	m, closeFn, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer closeFn()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// RollbackMigration reverts the most recent migration.
//
// Without this the .down.sql files in the repo were dead weight: nothing could run
// them, so they were never exercised and a broken rollback would only be discovered
// during an incident, which is the worst possible moment.
func RollbackMigration(databaseURL string) error {
	m, closeFn, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer closeFn()

	if err := m.Steps(-1); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migrate down: %w", err)
	}
	return nil
}

// MigrationVersion reports the current schema version and whether the last attempt
// left it dirty. A dirty schema means a migration failed halfway and needs manual
// attention: golang-migrate refuses to proceed until it is resolved.
func MigrationVersion(databaseURL string) (version uint, dirty bool, err error) {
	m, closeFn, err := newMigrator(databaseURL)
	if err != nil {
		return 0, false, err
	}
	defer closeFn()

	version, dirty, err = m.Version()
	if err == migrate.ErrNilVersion {
		return 0, false, nil
	}
	return version, dirty, err
}
