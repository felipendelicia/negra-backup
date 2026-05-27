// internal/notify/retention_test.go
package notify_test

import (
	"testing"

	"github.com/felipendelicia/nat-backup/internal/notify"
	"github.com/stretchr/testify/assert"
)

func TestRetentionCleaner_New(t *testing.T) {
	cleaner := notify.NewRetentionCleaner(nil, nil)
	assert.NotNil(t, cleaner)
}

func TestRetentionCleaner_RunNilDB(t *testing.T) {
	cleaner := notify.NewRetentionCleaner(nil, nil)
	cleaner.Run() // Should not panic
}
