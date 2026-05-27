// internal/db/migrate.go
package db

import (
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	natmigrations "github.com/felipendelicia/nat-backup/migrations"
)

// RunMigrations applies all pending up migrations. Idempotent.
func RunMigrations(databaseURL string) error {
	pool, err := Connect(databaseURL)
	if err != nil {
		return fmt.Errorf("connect for migration: %w", err)
	}
	defer pool.Close()

	driver, err := postgres.WithInstance(pool.DB, &postgres.Config{})
	if err != nil {
		return fmt.Errorf("postgres driver: %w", err)
	}

	src, err := iofs.New(natmigrations.FS, ".")
	if err != nil {
		return fmt.Errorf("migration source: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	if err != nil {
		return fmt.Errorf("migrate init: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migrate up: %w", err)
	}

	return nil
}
