// internal/db/db_test.go
package db_test

import (
	"os"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/db"
	"github.com/stretchr/testify/require"
)

func testDSN() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://nat_backup:nat_backup_dev@localhost:5432/nat_backup?sslmode=disable"
}

func TestConnect_Valid(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	pool, err := db.Connect(testDSN())
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, pool.Ping())
}

func TestConnect_Invalid(t *testing.T) {
	_, err := db.Connect("postgres://bad:bad@localhost:19999/none?sslmode=disable&connect_timeout=1")
	require.Error(t, err)
}
