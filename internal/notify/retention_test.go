// internal/notify/retention_test.go
package notify

import (
	"testing"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetentionCleaner_New(t *testing.T) {
	cleaner := NewRetentionCleaner(nil, nil, "")
	assert.NotNil(t, cleaner)
}

func TestRetentionCleaner_RunNilDB(t *testing.T) {
	cleaner := NewRetentionCleaner(nil, nil, "")
	cleaner.Run() // Should not panic
}

func strptr(s string) *string { return &s }

// deleteArtifact decides whether a run's stored file can be removed. Getting this
// wrong is what turned backups into orphans: the row was deleted unconditionally
// while the file was only ever logged as "would delete".
func TestRetentionCleaner_DeleteArtifact(t *testing.T) {
	rc := NewRetentionCleaner(nil, nil, "")

	t.Run("run that never uploaded anything has nothing to delete", func(t *testing.T) {
		require.NoError(t, rc.deleteArtifact(expiredRun{ID: "r1", StoragePath: nil}))
		require.NoError(t, rc.deleteArtifact(expiredRun{ID: "r1", StoragePath: strptr("")}))
	})

	t.Run("destination already removed leaves only the row to clean", func(t *testing.T) {
		require.NoError(t, rc.deleteArtifact(expiredRun{
			ID: "r2", StoragePath: strptr("backup.tar.zst"),
		}))
	})

	t.Run("local destination is unreachable from the server, so the row is kept", func(t *testing.T) {
		err := rc.deleteArtifact(expiredRun{
			ID:          "r3",
			StoragePath: strptr("backup.tar.zst"),
			DestType:    strptr(models.StorageTypeLocal),
			DestConfig:  strptr("ciphertext"),
		})
		assert.ErrorIs(t, err, errLocalDestinationUnreachable)
	})

	t.Run("undecryptable destination config is an error, not a silent row delete", func(t *testing.T) {
		err := rc.deleteArtifact(expiredRun{
			ID:          "r4",
			StoragePath: strptr("backup.tar.zst"),
			DestType:    strptr(models.StorageTypeS3),
			DestConfig:  strptr("not-valid-ciphertext"),
		})
		assert.Error(t, err)
	})
}
