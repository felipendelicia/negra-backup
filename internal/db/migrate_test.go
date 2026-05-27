// internal/db/migrate_test.go
package db_test

import (
	"testing"

	"github.com/felipendelicia/nat-backup/internal/db"
	"github.com/stretchr/testify/require"
)

func TestRunMigrations(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	dsn := testDSN()

	err := db.RunMigrations(dsn)
	require.NoError(t, err)

	pool, err := db.Connect(dsn)
	require.NoError(t, err)
	defer pool.Close()

	tables := []string{"agents", "storage_destinations", "backup_jobs", "backup_runs", "notification_settings", "admin_users"}
	for _, tbl := range tables {
		var exists bool
		err = pool.QueryRow(
			`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = $1)`, tbl,
		).Scan(&exists)
		require.NoError(t, err)
		require.True(t, exists, "table %s must exist", tbl)
	}

	// Idempotent: running again must not error
	require.NoError(t, db.RunMigrations(dsn))
}
