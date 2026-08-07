// internal/api/metrics.go
package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Version is the server build stamped into nat_backup_build_info. cmd/server sets it
// from the ldflags variable; the default keeps tests and `go run` honest.
var Version = "dev"

// The Prometheus text exposition format is written by hand rather than through
// client_golang. Every series here is derived from a SQL query at scrape time — none
// of it is a process-lifetime counter — so a custom Collector would be the only way
// to use the library anyway, and it would still be this SQL plus an adapter. The
// format for gauges is four lines of escaping rules, tested in metrics_test.go.

// metricWriter accumulates one exposition document.
type metricWriter struct {
	buf strings.Builder
	// declared tracks which names already emitted HELP/TYPE, because Prometheus
	// rejects a document that declares the same metric family twice.
	declared map[string]bool
}

func newMetricWriter() *metricWriter {
	return &metricWriter{declared: make(map[string]bool)}
}

// gauge writes one sample, emitting the HELP/TYPE header the first time the family
// is seen. labels must have an even number of elements (key, value, key, value…).
func (m *metricWriter) gauge(name, help string, value float64, labels ...string) {
	if !m.declared[name] {
		m.declared[name] = true
		fmt.Fprintf(&m.buf, "# HELP %s %s\n# TYPE %s gauge\n", name, escapeHelp(help), name)
	}
	m.buf.WriteString(name)
	m.writeLabels(labels)
	m.buf.WriteByte(' ')
	m.buf.WriteString(formatValue(value))
	m.buf.WriteByte('\n')
}

func (m *metricWriter) writeLabels(labels []string) {
	if len(labels) == 0 {
		return
	}
	if len(labels)%2 != 0 {
		// A caller bug, not a runtime condition. Dropping the labels keeps the
		// document parseable instead of emitting a broken series.
		log.Printf("metrics: odd label list %v; labels dropped", labels)
		return
	}
	m.buf.WriteByte('{')
	for i := 0; i < len(labels); i += 2 {
		if i > 0 {
			m.buf.WriteByte(',')
		}
		m.buf.WriteString(labels[i])
		m.buf.WriteString(`="`)
		m.buf.WriteString(escapeLabelValue(labels[i+1]))
		m.buf.WriteByte('"')
	}
	m.buf.WriteByte('}')
}

func (m *metricWriter) String() string { return m.buf.String() }

// escapeLabelValue applies the three escapes the exposition format defines for label
// values. Job and agent names are operator-supplied free text, so a name containing a
// quote or a backslash would otherwise produce a document Prometheus refuses whole —
// one bad name silently taking down every metric on the server.
func escapeLabelValue(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

// escapeHelp escapes the two characters that are special in a HELP line.
func escapeHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

// formatValue renders a float the way the exposition format expects. NaN and the
// infinities have their own spellings.
//
// 'f' rather than 'g': every value here is a byte count, a cardinality or a Unix
// timestamp, and 'g' switches to exponential notation above 1e21 — a form that is
// legal but unreadable in a document an operator diffs by hand. Precision -1 emits
// the shortest string that round-trips, so nothing is lost either way.
func formatValue(v float64) string {
	switch {
	case v != v:
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// jobMetricRow is the per-job slice of the exposition: identity, plus the two runs
// that answer "is this job healthy" — the last successful one and the last finished
// one of any outcome.
type jobMetricRow struct {
	JobID     string  `db:"job_id"`
	JobName   string  `db:"job_name"`
	AgentName *string `db:"agent_name"`
	Enabled   bool    `db:"enabled"`

	LastSuccessAt    *time.Time `db:"last_success_at"`
	LastSuccessBytes *int64     `db:"last_success_bytes"`
	LastSuccessFiles *int       `db:"last_success_files"`

	LastRunAt     *time.Time `db:"last_run_at"`
	LastRunStatus *string    `db:"last_run_status"`
}

// jobMetricsQuery collects both "last" runs in one pass.
//
// attempt >= 1 on the last-run subquery is not incidental. recordSkippedRun writes
// rows with attempt = 0 and status 'failed' to mark an execution that was skipped
// because the previous one was still going. Counting those as the last run would
// report a healthy long-running job as failing, which is exactly the false alarm that
// makes people stop reading alerts.
const jobMetricsQuery = `
SELECT j.id            AS job_id,
       j.name          AS job_name,
       j.enabled       AS enabled,
       a.name          AS agent_name,
       ls.finished_at  AS last_success_at,
       ls.size_bytes   AS last_success_bytes,
       ls.file_count   AS last_success_files,
       lr.finished_at  AS last_run_at,
       lr.status       AS last_run_status
FROM backup_jobs j
LEFT JOIN agents a ON a.id = j.agent_id
LEFT JOIN LATERAL (
    SELECT finished_at, size_bytes, file_count
    FROM backup_runs
    WHERE job_id = j.id AND status = 'success'
    ORDER BY started_at DESC
    LIMIT 1
) ls ON TRUE
LEFT JOIN LATERAL (
    SELECT finished_at, status
    FROM backup_runs
    WHERE job_id = j.id
      AND status IN ('success', 'failed', 'cancelled')
      AND attempt >= 1
    ORDER BY started_at DESC
    LIMIT 1
) lr ON TRUE
ORDER BY j.id`

// handleMetrics serves the Prometheus exposition.
//
// The endpoint exists only when METRICS_TOKEN is set (see config.Config), so there is
// no unauthenticated shape of this route to get wrong.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.metricsTokenValid(r) {
		// No WWW-Authenticate: a scraper either has the token or should not be
		// learning that this path is special.
		respondError(w, http.StatusUnauthorized, "invalid metrics token")
		return
	}

	m := newMetricWriter()
	m.gauge("nat_backup_build_info", "Build information of the running server.", 1, "version", Version)

	s.collectDBMetrics(m)
	s.collectJobMetrics(m)
	s.collectScheduleMetrics(m)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, m.String())
}

// metricsTokenValid compares the presented bearer token in constant time. An unset
// token cannot match, which is what disables the endpoint.
func (s *Server) metricsTokenValid(r *http.Request) bool {
	want := s.cfg.MetricsToken
	if want == "" {
		return false
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// collectDBMetrics emits the fleet-wide counts.
//
// A failing query degrades that family to nothing rather than failing the scrape:
// losing one series is recoverable, a 500 makes the whole target go down and takes
// every other metric with it.
func (s *Server) collectDBMetrics(m *metricWriter) {
	if s.db == nil {
		return
	}

	type statusCount struct {
		Status string `db:"status"`
		N      int64  `db:"n"`
	}

	var agents []statusCount
	if err := s.db.Select(&agents,
		`SELECT status, COUNT(*) AS n FROM agents GROUP BY status`); err != nil {
		log.Printf("metrics: agent counts: %v", err)
	} else {
		// Both statuses are always emitted: a gauge that disappears when it reaches
		// zero is indistinguishable from a scrape failure in any alerting rule.
		byStatus := map[string]int64{"online": 0, "offline": 0}
		for _, a := range agents {
			byStatus[a.Status] = a.N
		}
		for _, status := range sortedKeys(byStatus) {
			m.gauge("nat_backup_agents", "Registered agents by connection status.",
				float64(byStatus[status]), "status", status)
		}
	}

	var runs []statusCount
	if err := s.db.Select(&runs,
		`SELECT status, COUNT(*) AS n FROM backup_runs GROUP BY status`); err != nil {
		log.Printf("metrics: run counts: %v", err)
	} else {
		byStatus := map[string]int64{"running": 0, "success": 0, "failed": 0, "cancelled": 0}
		for _, r := range runs {
			byStatus[r.Status] = r.N
		}
		for _, status := range sortedKeys(byStatus) {
			m.gauge("nat_backup_runs",
				"Run rows currently stored, by status. Retention deletes old rows, so this is a gauge and not a counter.",
				float64(byStatus[status]), "status", status)
		}
	}

	var openAlerts []struct {
		Kind string `db:"kind"`
		N    int64  `db:"n"`
	}
	if err := s.db.Select(&openAlerts,
		`SELECT kind, COUNT(*) AS n FROM alert_state GROUP BY kind`); err != nil {
		log.Printf("metrics: alert counts: %v", err)
	} else {
		for _, a := range openAlerts {
			m.gauge("nat_backup_open_alerts", "Alerts the dead-man's switch currently holds open, by kind.",
				float64(a.N), "kind", a.Kind)
		}
	}
}

// collectJobMetrics emits the per-job series.
//
// nat_backup_job_last_success_timestamp_seconds is the one that matters: a staleness
// alert written against it ("no successful backup in 26 hours") is the same alert the
// dead-man's switch raises internally, expressed in whatever the operator already
// uses. A job that has never succeeded emits no sample at all rather than a zero — a
// zero timestamp reads as 1970 and would satisfy "older than N hours" forever without
// distinguishing "never ran" from "ran and is old".
func (s *Server) collectJobMetrics(m *metricWriter) {
	if s.db == nil {
		return
	}
	var rows []jobMetricRow
	if err := s.db.Select(&rows, jobMetricsQuery); err != nil {
		log.Printf("metrics: job metrics: %v", err)
		return
	}

	enabled := map[string]int64{"true": 0, "false": 0}
	for _, row := range rows {
		enabled[strconv.FormatBool(row.Enabled)]++

		agent := ""
		if row.AgentName != nil {
			agent = *row.AgentName
		}
		labels := []string{"job_id", row.JobID, "job", row.JobName, "agent", agent}

		m.gauge("nat_backup_job_enabled", "Whether a job is enabled.",
			boolValue(row.Enabled), labels...)

		if row.LastSuccessAt != nil {
			m.gauge("nat_backup_job_last_success_timestamp_seconds",
				"Unix time of the last successful run of a job. Absent for jobs that have never succeeded.",
				float64(row.LastSuccessAt.UnixNano())/1e9, labels...)
		}
		if row.LastSuccessBytes != nil {
			m.gauge("nat_backup_job_last_success_size_bytes",
				"Size of the artifact produced by the last successful run.",
				float64(*row.LastSuccessBytes), labels...)
		}
		if row.LastSuccessFiles != nil {
			m.gauge("nat_backup_job_last_success_files",
				"Number of files in the last successful run.",
				float64(*row.LastSuccessFiles), labels...)
		}
		if row.LastRunAt != nil {
			m.gauge("nat_backup_job_last_run_timestamp_seconds",
				"Unix time the last finished run of a job ended, whatever its outcome.",
				float64(row.LastRunAt.UnixNano())/1e9, labels...)
		}
		if row.LastRunStatus != nil {
			m.gauge("nat_backup_job_last_run_success",
				"1 when the last finished run of a job succeeded, 0 otherwise. Runs skipped by the overlap guard are not counted.",
				boolValue(*row.LastRunStatus == "success"), labels...)
		}
	}

	for _, k := range sortedKeys(enabled) {
		m.gauge("nat_backup_jobs", "Configured backup jobs by enabled state.",
			float64(enabled[k]), "enabled", k)
	}
}

// collectScheduleMetrics reports when each job fires next, taken from the scheduler's
// live cron entries rather than re-parsed from schedule_cron — same reasoning as
// NextRuns: a job this process will never run must not report a next execution.
func (s *Server) collectScheduleMetrics(m *metricWriter) {
	if s.sched == nil {
		return
	}
	next := s.sched.NextRuns()
	ids := make([]string, 0, len(next))
	for id := range next {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		m.gauge("nat_backup_job_next_run_timestamp_seconds",
			"Unix time the scheduler will next fire this job.",
			float64(next[id].UnixNano())/1e9, "job_id", id)
	}
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// sortedKeys keeps the exposition byte-stable between scrapes, which makes diffing
// two /metrics dumps by hand a usable debugging step.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// readyzTimeout bounds the database probe. A readiness check that can hang for the
// duration of a TCP timeout is useless to the thing polling it: the load balancer
// gives up first and reports its own error instead of ours.
const readyzTimeout = 3 * time.Second

// handleReadyz reports whether the server can actually serve requests.
//
// /healthz answers "this process is alive" and deliberately still does: a liveness
// probe that fails on a database outage makes the orchestrator kill and restart a
// perfectly healthy server, turning a recoverable dependency failure into a crash
// loop. /readyz is the one that must tell the truth about dependencies, because its
// consequence is being taken out of rotation, not being killed.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		respond(w, http.StatusServiceUnavailable,
			map[string]string{"status": "unavailable", "database": "not configured"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), readyzTimeout)
	defer cancel()

	// PingContext alone can be answered from a pooled connection that the server has
	// since closed. A trivial round trip proves the far end is really there.
	var one int
	if err := s.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
		if err == sql.ErrNoRows {
			err = fmt.Errorf("database returned no rows for SELECT 1")
		}
		log.Printf("readyz: database probe failed: %v", err)
		respond(w, http.StatusServiceUnavailable,
			map[string]string{"status": "unavailable", "database": err.Error()})
		return
	}

	respond(w, http.StatusOK, map[string]string{"status": "ready", "database": "ok"})
}
