package backup_test

import (
	"bytes"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/backup"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDBDumper_UnknownType(t *testing.T) {
	dumper := backup.NewDBDumper("unknown-db-type", "conn-string")
	var buf bytes.Buffer
	_, err := dumper.Dump(&buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown")
}

func TestDBDumper_SQLite_MissingFile(t *testing.T) {
	dumper := backup.NewDBDumper("sqlite", "/nonexistent/path.db")
	var buf bytes.Buffer
	_, err := dumper.Dump(&buf)
	require.Error(t, err)
}

func TestDBDumper_Types(t *testing.T) {
	types := []string{"postgres", "mysql", "sqlite", "mongodb"}
	for _, dbType := range types {
		dumper := backup.NewDBDumper(dbType, "test-conn")
		assert.NotNil(t, dumper, "dumper for %s should not be nil", dbType)
	}
}
