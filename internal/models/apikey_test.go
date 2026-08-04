// internal/models/apikey_test.go
package models_test

import (
	"testing"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/stretchr/testify/assert"
)

// A malformed key must be reported as legacy rather than parsed into an empty
// identifier: an empty keyID would match no row, but an empty secret would be
// compared against a bcrypt hash, and the distinction matters for which code path
// runs.
func TestParseAPIKey(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		wantID     string
		wantSecret string
		wantOK     bool
	}{
		{"current format", "natb_a1b2c3d4_deadbeef", "a1b2c3d4", "deadbeef", true},
		{"secret containing underscores", "natb_id_part1_part2", "id", "part1_part2", true},
		{"legacy opaque key", "0123456789abcdef", "", "", false},
		{"prefix only", "natb_", "", "", false},
		{"prefix and id but no secret", "natb_abc_", "", "", false},
		{"prefix and secret but no id", "natb__secret", "", "", false},
		{"no separator after id", "natb_abcdef", "", "", false},
		{"empty", "", "", "", false},
		{"prefix in the middle is not a prefix", "xnatb_a_b", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, secret, ok := models.ParseAPIKey(tc.key)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantID, id)
			assert.Equal(t, tc.wantSecret, secret)
		})
	}
}
