// internal/api/metrics_internal_test.go
package api

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEscapeLabelValue(t *testing.T) {
	cases := map[string]string{
		"plain":                   "plain",
		`quote"inside`:            `quote\"inside`,
		`back\slash`:              `back\\slash`,
		"line\nbreak":             `line\nbreak`,
		`all "\` + "\n" + `mixed`: `all \"\\\nmixed`,
	}
	for in, want := range cases {
		assert.Equal(t, want, escapeLabelValue(in), "input %q", in)
	}
}

// A job named with a quote used to be enough to make the whole document
// unparseable, taking every unrelated metric down with it.
func TestGauge_QuotedJobNameStaysParseable(t *testing.T) {
	m := newMetricWriter()
	m.gauge("nat_backup_job_enabled", "help", 1, "job", `the "nightly" job`)

	line := lastSampleLine(t, m.String())
	assert.Equal(t, `nat_backup_job_enabled{job="the \"nightly\" job"} 1`, line)
}

func TestGauge_DeclaresFamilyOnlyOnce(t *testing.T) {
	m := newMetricWriter()
	m.gauge("nat_backup_agents", "Registered agents.", 3, "status", "online")
	m.gauge("nat_backup_agents", "Registered agents.", 1, "status", "offline")

	out := m.String()
	assert.Equal(t, 1, strings.Count(out, "# HELP nat_backup_agents "))
	assert.Equal(t, 1, strings.Count(out, "# TYPE nat_backup_agents "))
	assert.Contains(t, out, `nat_backup_agents{status="online"} 3`)
	assert.Contains(t, out, `nat_backup_agents{status="offline"} 1`)
}

func TestGauge_NoLabelsOmitsBraces(t *testing.T) {
	m := newMetricWriter()
	m.gauge("nat_backup_up", "help", 1)
	assert.Equal(t, "nat_backup_up 1", lastSampleLine(t, m.String()))
}

// An odd label list is a programming error; the document must still parse.
func TestGauge_OddLabelListDropsLabels(t *testing.T) {
	m := newMetricWriter()
	m.gauge("nat_backup_up", "help", 1, "status")
	assert.Equal(t, "nat_backup_up 1", lastSampleLine(t, m.String()))
}

func TestFormatValue_TimestampKeepsFullPrecision(t *testing.T) {
	// A Unix timestamp in seconds must not come out in a lossy exponential form:
	// staleness alerts subtract it from now() and a rounded value moves the answer
	// by minutes.
	assert.Equal(t, "1770000000.5", formatValue(1770000000.5))
	assert.Equal(t, "1770000000", formatValue(1770000000))
}

func TestFormatValue_NonFinite(t *testing.T) {
	nan := func() float64 { z := 0.0; return z / z }()
	assert.Equal(t, "NaN", formatValue(nan))
}

func TestBoolValue(t *testing.T) {
	assert.Equal(t, float64(1), boolValue(true))
	assert.Equal(t, float64(0), boolValue(false))
}

// lastSampleLine returns the final non-comment, non-empty line of an exposition.
func lastSampleLine(t *testing.T, doc string) string {
	t.Helper()
	var last string
	for _, line := range strings.Split(doc, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		last = line
	}
	if last == "" {
		t.Fatalf("no sample line in exposition:\n%s", doc)
	}
	return last
}
