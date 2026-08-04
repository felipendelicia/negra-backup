// internal/api/runs_pagination_test.go
package api

import (
	"net/url"
	"testing"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/stretchr/testify/assert"
)

func TestParsePagination(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantLimit  int
		wantOffset int
	}{
		{"defaults when absent", "", defaultRunsLimit, 0},
		{"explicit values", "limit=10&offset=20", 10, 20},
		{"limit is clamped to the maximum", "limit=100000", maxRunsLimit, 0},
		{"zero limit falls back to the default", "limit=0", defaultRunsLimit, 0},
		{"negative limit falls back to the default", "limit=-5", defaultRunsLimit, 0},
		{"negative offset is treated as zero", "offset=-5", defaultRunsLimit, 0},
		{"garbage falls back to the defaults", "limit=abc&offset=xyz", defaultRunsLimit, 0},
		{"limit exactly at the maximum", "limit=500", maxRunsLimit, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q, err := url.ParseQuery(tc.query)
			assert.NoError(t, err)
			limit, offset := parsePagination(q)
			assert.Equal(t, tc.wantLimit, limit)
			assert.Equal(t, tc.wantOffset, offset)
		})
	}
}

// The status column has no CHECK constraint, so an unrecognised filter would return
// an empty page that looks like "no runs" rather than a bad request.
func TestIsValidRunStatus(t *testing.T) {
	for _, s := range []string{
		models.RunStatusRunning, models.RunStatusSuccess,
		models.RunStatusFailed, models.RunStatusCancelled,
	} {
		assert.True(t, isValidRunStatus(s), "%q must be accepted", s)
	}
	for _, s := range []string{"", "bogus", "SUCCESS", "success ", "'; DROP TABLE backup_runs; --"} {
		assert.False(t, isValidRunStatus(s), "%q must be rejected", s)
	}
}
