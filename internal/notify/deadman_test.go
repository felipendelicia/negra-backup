// internal/notify/deadman_test.go
//
// Internal test package (package notify) so Check can be driven one pass at a time
// instead of waiting on the five-minute ticker, and so the classification helpers
// can be exercised without a database.
//
// The database-backed tests share a Postgres instance with the other packages'
// tests, which run concurrently. Every assertion therefore filters on the unique
// name of the row it seeded rather than counting everything the switch found —
// counting totals would make these tests fail whenever another package happened to
// leave a job behind.
package notify

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felipendelicia/nat-backup/internal/db"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/felipendelicia/nat-backup/internal/scheduler"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- classification (no database) ------------------------------------------

func ptrTime(t time.Time) *time.Time { return &t }
func ptrStr(s string) *string        { return &s }

// baseTime is a fixed instant well away from any cron boundary, so "the last 03:00"
// is unambiguous.
var baseTime = time.Date(2026, 3, 10, 14, 37, 0, 0, time.UTC)

func TestClassify_DisabledJobIsNotAnAlert(t *testing.T) {
	j := JobHealth{Enabled: false, ScheduleCron: "0 3 * * *"}
	j.Classify(baseTime, time.Minute)
	assert.Equal(t, HealthDisabled, j.Status)
}

func TestClassify_UnparseableCron(t *testing.T) {
	j := JobHealth{Enabled: true, ScheduleCron: "not-a-cron"}
	j.Classify(baseTime, time.Minute)
	assert.Equal(t, HealthUnknown, j.Status)
	assert.Contains(t, j.Detail, "cannot be parsed")
}

// A job created minutes ago has not missed anything yet. Alerting here would train
// everyone to ignore the alerts on day one.
func TestClassify_NeverRunButNotDueYet(t *testing.T) {
	j := JobHealth{
		Enabled:      true,
		ScheduleCron: "0 3 * * *",
		JobCreatedAt: baseTime.Add(-10 * time.Minute),
	}
	j.Classify(baseTime, 30*time.Minute)
	assert.Equal(t, HealthNeverRun, j.Status)
	assert.Nil(t, j.DueAt, "a job whose first tick has not come due is not an alert")
}

func TestClassify_NeverRunAndOverdue(t *testing.T) {
	j := JobHealth{
		Enabled:      true,
		ScheduleCron: "0 3 * * *",
		JobCreatedAt: baseTime.Add(-72 * time.Hour),
	}
	j.Classify(baseTime, 30*time.Minute)
	assert.Equal(t, HealthNeverRun, j.Status)
	require.NotNil(t, j.DueAt, "the tick at 03:00 was missed and must be reported")
	assert.Equal(t, time.Date(2026, 3, 10, 3, 0, 0, 0, time.UTC), j.DueAt.UTC())
}

// The core of the dead-man's switch: the cron said 03:00, it is 14:37, and the last
// run was yesterday.
func TestClassify_MissedScheduledTick(t *testing.T) {
	j := JobHealth{
		Enabled:          true,
		ScheduleCron:     "0 3 * * *",
		JobCreatedAt:     baseTime.Add(-30 * 24 * time.Hour),
		LastRunStatus:    ptrStr(models.RunStatusSuccess),
		LastRunStartedAt: ptrTime(baseTime.Add(-24 * time.Hour)),
		LastSuccessAt:    ptrTime(baseTime.Add(-24 * time.Hour)),
	}
	j.Classify(baseTime, 30*time.Minute)
	assert.Equal(t, HealthLate, j.Status)
	require.NotNil(t, j.DueAt)
	assert.Equal(t, time.Date(2026, 3, 10, 3, 0, 0, 0, time.UTC), j.DueAt.UTC())
	assert.Contains(t, j.Detail, "did not run")
}

// The window between the scheduled minute and the run row appearing must not read
// as a miss, or every inspection during dispatch would raise an alert.
func TestClassify_WithinGraceIsNotLate(t *testing.T) {
	now := time.Date(2026, 3, 10, 3, 10, 0, 0, time.UTC) // 10 minutes after the tick
	j := JobHealth{
		Enabled:          true,
		ScheduleCron:     "0 3 * * *",
		JobCreatedAt:     now.Add(-30 * 24 * time.Hour),
		LastRunStatus:    ptrStr(models.RunStatusSuccess),
		LastRunStartedAt: ptrTime(now.Add(-24 * time.Hour)),
		LastSuccessAt:    ptrTime(now.Add(-24 * time.Hour)),
	}
	j.Classify(now, 30*time.Minute)
	assert.NotEqual(t, HealthLate, j.Status, "inside the grace window the run may still be starting")
}

func TestClassify_StaleSuccessEvenWithoutAMissedTick(t *testing.T) {
	// The tick was honoured (a run started after 03:00) but every run fails, so no
	// usable backup exists. This is criterion 3 and it is what catches schedules
	// whose previous fire cannot be located, such as @every.
	j := JobHealth{
		Enabled:          true,
		ScheduleCron:     "0 3 * * *",
		JobCreatedAt:     baseTime.Add(-30 * 24 * time.Hour),
		LastRunStatus:    ptrStr(models.RunStatusFailed),
		LastRunStartedAt: ptrTime(baseTime.Add(-11 * time.Hour)),
		LastSuccessAt:    ptrTime(baseTime.Add(-5 * 24 * time.Hour)),
	}
	j.Classify(baseTime, 30*time.Minute)
	assert.Equal(t, HealthLate, j.Status,
		"five days without a successful backup outranks 'the last run failed'")
	assert.Contains(t, j.Detail, "last successful backup")
}

// A failure that is still inside the schedule's own window is `failing`, not `late`:
// the per-run email has already gone out and there is nothing silent about it.
func TestClassify_RecentFailureIsFailingNotLate(t *testing.T) {
	j := JobHealth{
		Enabled:          true,
		ScheduleCron:     "0 3 * * *",
		JobCreatedAt:     baseTime.Add(-30 * 24 * time.Hour),
		LastRunStatus:    ptrStr(models.RunStatusFailed),
		LastRunError:     ptrStr("disk full\nstack trace here"),
		LastRunStartedAt: ptrTime(baseTime.Add(-11 * time.Hour)), // today's 03:00 tick
		LastSuccessAt:    ptrTime(baseTime.Add(-20 * time.Hour)), // inside 24h + grace
	}
	j.Classify(baseTime, 30*time.Minute)
	assert.Equal(t, HealthFailing, j.Status)
	assert.Contains(t, j.Detail, "disk full")
	assert.NotContains(t, j.Detail, "stack trace", "the detail keeps to the first line")
}

func TestClassify_Healthy(t *testing.T) {
	j := JobHealth{
		Enabled:          true,
		ScheduleCron:     "0 3 * * *",
		JobCreatedAt:     baseTime.Add(-30 * 24 * time.Hour),
		LastRunStatus:    ptrStr(models.RunStatusSuccess),
		LastRunStartedAt: ptrTime(baseTime.Add(-11 * time.Hour)),
		LastSuccessAt:    ptrTime(baseTime.Add(-11 * time.Hour)),
	}
	j.Classify(baseTime, 30*time.Minute)
	assert.Equal(t, HealthOK, j.Status)
}

func TestClassify_RunningIsNotJudged(t *testing.T) {
	j := JobHealth{
		Enabled:          true,
		ScheduleCron:     "0 3 * * *",
		JobCreatedAt:     baseTime.Add(-30 * 24 * time.Hour),
		LastRunStatus:    ptrStr(models.RunStatusRunning),
		LastRunStartedAt: ptrTime(baseTime.Add(-5 * time.Minute)),
		LastSuccessAt:    ptrTime(baseTime.Add(-24 * time.Hour)),
	}
	j.Classify(baseTime, 30*time.Minute)
	assert.Equal(t, HealthRunning, j.Status)
}

func TestPreviousFire(t *testing.T) {
	sched, err := scheduler.ParseCron("0 3 * * *")
	require.NoError(t, err)

	prev, ok := previousFire(sched, baseTime.Add(-30*24*time.Hour), baseTime)
	require.True(t, ok)
	assert.Equal(t, time.Date(2026, 3, 10, 3, 0, 0, 0, time.UTC), prev.UTC())

	// Clamped: a job created after the last tick has not missed it.
	_, ok = previousFire(sched, baseTime.Add(-time.Hour), baseTime)
	assert.False(t, ok, "the search must not report a tick from before the job existed")

	// A minute-by-minute schedule resolves to the current minute, not to a walk of
	// the whole lookback window.
	perMinute, err := scheduler.ParseCron("* * * * *")
	require.NoError(t, err)
	prev, ok = previousFire(perMinute, baseTime.Add(-30*24*time.Hour), baseTime)
	require.True(t, ok)
	assert.Equal(t, baseTime, prev.UTC())
}

// --- database-backed tests -------------------------------------------------

func testDSN() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://nat_backup:nat_backup_dev@localhost:5432/nat_backup?sslmode=disable"
}

func testDB(t *testing.T) *sqlx.DB {
	t.Helper()
	if testing.Short() {
		t.Skip("requires postgres")
	}
	pool, err := db.Connect(testDSN())
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

type sentAlert struct{ subject, body string }

type mockAlerter struct {
	mu   sync.Mutex
	sent []sentAlert
	err  error
}

func (m *mockAlerter) SendAlert(subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, sentAlert{subject, body})
	return nil
}

// matching returns the alerts whose subject mentions `needle`. Assertions filter by
// the seeded row's unique name so unrelated rows left by concurrently running test
// packages cannot influence the result.
func (m *mockAlerter) matching(needle string) []sentAlert {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []sentAlert
	for _, s := range m.sent {
		if strings.Contains(s.subject, needle) {
			out = append(out, s)
		}
	}
	return out
}

func (m *mockAlerter) setErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// seed creates an agent and a job wired to a storage destination, and registers the
// cleanup (including alert_state, which has no foreign key to cascade through).
type seeded struct {
	agentID   uuid.UUID
	jobID     uuid.UUID
	agentName string
	jobName   string
}

func seed(t *testing.T, pool *sqlx.DB, scheduleCron string) seeded {
	t.Helper()
	suffix := uuid.NewString()[:8]
	s := seeded{
		agentName: "dm-agent-" + suffix,
		jobName:   "dm-job-" + suffix,
	}

	require.NoError(t, pool.QueryRow(
		`INSERT INTO agents (name, api_key, last_seen) VALUES ($1, $2, NOW()) RETURNING id`,
		s.agentName, "dm-key-"+suffix,
	).Scan(&s.agentID))

	var destID uuid.UUID
	require.NoError(t, pool.QueryRow(
		`INSERT INTO storage_destinations (name, type, config) VALUES ($1, 'local', 'x') RETURNING id`,
		"dm-dest-"+suffix,
	).Scan(&destID))

	require.NoError(t, pool.QueryRow(`
		INSERT INTO backup_jobs
		  (agent_id, name, enabled, type, source, storage_destination_id, schedule_cron, compression, encrypt)
		VALUES ($1, $2, true, 'files', '{"paths":["/tmp"]}', $3, $4, 'zstd', false)
		RETURNING id`,
		s.agentID, s.jobName, destID, scheduleCron,
	).Scan(&s.jobID))

	t.Cleanup(func() {
		pool.Exec(`DELETE FROM alert_state WHERE subject_id IN ($1, $2)`, s.agentID, s.jobID) //nolint:errcheck
		pool.Exec(`DELETE FROM agents WHERE id=$1`, s.agentID)                                //nolint:errcheck
		pool.Exec(`DELETE FROM storage_destinations WHERE id=$1`, destID)                     //nolint:errcheck
	})
	return s
}

// addRun inserts a terminal run started `ago` in the past.
func addRun(t *testing.T, pool *sqlx.DB, jobID uuid.UUID, status string, ago time.Duration) {
	t.Helper()
	_, err := pool.Exec(`
		INSERT INTO backup_runs (job_id, status, attempt, started_at, finished_at)
		VALUES ($1, $2, 1, NOW() - make_interval(secs => $3), NOW() - make_interval(secs => $3))`,
		jobID, status, ago.Seconds())
	require.NoError(t, err)
}

func newSwitch(pool *sqlx.DB, alerter Alerter) *DeadManSwitch {
	return NewDeadManSwitch(pool, alerter, DeadManConfig{
		Enabled:           true,
		CheckInterval:     time.Minute,
		AgentOfflineAfter: 15 * time.Minute,
		JobGrace:          time.Minute,
		RepeatInterval:    24 * time.Hour,
	})
}

func alertRow(t *testing.T, pool *sqlx.DB, subjectID uuid.UUID, kind string) (OpenAlert, bool) {
	t.Helper()
	var a OpenAlert
	err := pool.Get(&a, `
		SELECT subject_type, subject_id, kind, first_seen_at, last_notified_at, notify_count, detail
		FROM alert_state WHERE subject_id=$1 AND kind=$2`, subjectID, kind)
	if err != nil {
		return OpenAlert{}, false
	}
	return a, true
}

// TestDeadMan_AgentOfflineNotifiesOnceThenRepeats is the anti-spam requirement:
// an agent down for days must produce a handful of emails, not one per check.
func TestDeadMan_AgentOfflineNotifiesOnceThenRepeats(t *testing.T) {
	pool := testDB(t)
	s := seed(t, pool, "0 3 * * *")

	_, err := pool.Exec(`UPDATE agents SET last_seen = NOW() - INTERVAL '3 days' WHERE id=$1`, s.agentID)
	require.NoError(t, err)

	alerter := &mockAlerter{}
	dm := newSwitch(pool, alerter)

	dm.Check()
	require.Len(t, alerter.matching(s.agentName), 1, "the outage must be reported once")
	assert.Contains(t, alerter.matching(s.agentName)[0].subject, "Agent OFFLINE")

	// Twelve more passes — two hours of a five-minute ticker — must add nothing.
	for i := 0; i < 12; i++ {
		dm.Check()
	}
	assert.Len(t, alerter.matching(s.agentName), 1,
		"an open alert must not be re-sent on every pass")

	row, ok := alertRow(t, pool, s.agentID, AlertAgentOffline)
	require.True(t, ok)
	assert.Equal(t, 1, row.NotifyCount)

	// Once the repeat interval has elapsed the reminder goes out — exactly one.
	_, err = pool.Exec(
		`UPDATE alert_state SET last_notified_at = NOW() - INTERVAL '25 hours' WHERE subject_id=$1`,
		s.agentID)
	require.NoError(t, err)
	dm.Check()
	dm.Check()
	sent := alerter.matching(s.agentName)
	require.Len(t, sent, 2, "one reminder per repeat interval, not one per pass")
	assert.Contains(t, sent[1].subject, "STILL")
	assert.Contains(t, sent[1].body, "reminder #2")
}

// TestDeadMan_AgentRecoveryIsReported covers the other half of the information.
func TestDeadMan_AgentRecoveryIsReported(t *testing.T) {
	pool := testDB(t)
	s := seed(t, pool, "0 3 * * *")

	_, err := pool.Exec(`UPDATE agents SET last_seen = NOW() - INTERVAL '2 days' WHERE id=$1`, s.agentID)
	require.NoError(t, err)

	alerter := &mockAlerter{}
	dm := newSwitch(pool, alerter)
	dm.Check()
	require.Len(t, alerter.matching(s.agentName), 1)

	// The agent reconnects.
	_, err = pool.Exec(`UPDATE agents SET last_seen = NOW() WHERE id=$1`, s.agentID)
	require.NoError(t, err)

	dm.Check()
	sent := alerter.matching(s.agentName)
	require.Len(t, sent, 2)
	assert.Contains(t, sent[1].subject, "Agent recovered")
	assert.Contains(t, sent[1].body, "Broken for")

	_, ok := alertRow(t, pool, s.agentID, AlertAgentOffline)
	assert.False(t, ok, "a recovered alert must be closed, not left to fire again")

	// And the recovery is announced once, not on every subsequent pass.
	dm.Check()
	dm.Check()
	assert.Len(t, alerter.matching(s.agentName), 2)
}

// TestDeadMan_StateSurvivesRestart is the reason the state is a table and not a map.
// A fresh DeadManSwitch is exactly what a redeployed server has.
func TestDeadMan_StateSurvivesRestart(t *testing.T) {
	pool := testDB(t)
	s := seed(t, pool, "0 3 * * *")

	_, err := pool.Exec(`UPDATE agents SET last_seen = NOW() - INTERVAL '4 days' WHERE id=$1`, s.agentID)
	require.NoError(t, err)

	first := &mockAlerter{}
	newSwitch(pool, first).Check()
	require.Len(t, first.matching(s.agentName), 1)

	// Restart: brand new switch, brand new process, same database.
	second := &mockAlerter{}
	restarted := newSwitch(pool, second)
	restarted.Check()
	restarted.Check()
	assert.Empty(t, second.matching(s.agentName),
		"a restart must not re-announce alerts that were already sent")

	// And it still remembers enough to announce the recovery.
	_, err = pool.Exec(`UPDATE agents SET last_seen = NOW() WHERE id=$1`, s.agentID)
	require.NoError(t, err)
	restarted.Check()
	sent := second.matching(s.agentName)
	require.Len(t, sent, 1)
	assert.Contains(t, sent[0].subject, "recovered")
}

// TestDeadMan_UndeliveredAlertIsRetried: if SMTP is down the alert must stay open
// and be retried, not be recorded as delivered and silently dropped.
func TestDeadMan_UndeliveredAlertIsRetried(t *testing.T) {
	pool := testDB(t)
	s := seed(t, pool, "0 3 * * *")

	_, err := pool.Exec(`UPDATE agents SET last_seen = NOW() - INTERVAL '2 days' WHERE id=$1`, s.agentID)
	require.NoError(t, err)

	alerter := &mockAlerter{}
	alerter.setErr(fmt.Errorf("smtp: connection refused"))
	dm := newSwitch(pool, alerter)

	dm.Check()
	row, ok := alertRow(t, pool, s.agentID, AlertAgentOffline)
	require.True(t, ok, "the condition must be recorded even when it cannot be sent")
	assert.Equal(t, 0, row.NotifyCount, "nothing was delivered, so nothing is marked notified")

	// Mail comes back.
	alerter.setErr(nil)
	dm.Check()
	require.Len(t, alerter.matching(s.agentName), 1)
}

// An alert nobody ever received must not produce a "recovered" email for a problem
// nobody was told about.
func TestDeadMan_NoRecoveryForUndeliveredAlert(t *testing.T) {
	pool := testDB(t)
	s := seed(t, pool, "0 3 * * *")

	_, err := pool.Exec(`UPDATE agents SET last_seen = NOW() - INTERVAL '2 days' WHERE id=$1`, s.agentID)
	require.NoError(t, err)

	alerter := &mockAlerter{}
	alerter.setErr(fmt.Errorf("smtp down"))
	dm := newSwitch(pool, alerter)
	dm.Check()

	alerter.setErr(nil)
	_, err = pool.Exec(`UPDATE agents SET last_seen = NOW() WHERE id=$1`, s.agentID)
	require.NoError(t, err)
	dm.Check()

	assert.Empty(t, alerter.matching(s.agentName))
	_, ok := alertRow(t, pool, s.agentID, AlertAgentOffline)
	assert.False(t, ok, "the stale row must still be cleaned up")
}

// An agent nobody backs up with is not worth waking anyone for.
func TestDeadMan_IgnoresAgentWithoutEnabledJobs(t *testing.T) {
	pool := testDB(t)
	s := seed(t, pool, "0 3 * * *")

	_, err := pool.Exec(`UPDATE agents SET last_seen = NOW() - INTERVAL '9 days' WHERE id=$1`, s.agentID)
	require.NoError(t, err)
	_, err = pool.Exec(`UPDATE backup_jobs SET enabled=false WHERE id=$1`, s.jobID)
	require.NoError(t, err)

	alerter := &mockAlerter{}
	newSwitch(pool, alerter).Check()
	assert.Empty(t, alerter.matching(s.agentName))
	assert.Empty(t, alerter.matching(s.jobName), "a disabled job is not overdue")
}

// TestDeadMan_JobWithNoRecentSuccess is criterion 3 end to end: the job exists, the
// agent is online, runs happen — and none of them has produced a usable backup in
// days. Nothing in the run-level notification path can report this.
func TestDeadMan_JobWithNoRecentSuccess(t *testing.T) {
	pool := testDB(t)
	s := seed(t, pool, "0 3 * * *") // daily

	addRun(t, pool, s.jobID, models.RunStatusSuccess, 10*24*time.Hour)

	alerter := &mockAlerter{}
	dm := newSwitch(pool, alerter)
	dm.Check()

	sent := alerter.matching(s.jobName)
	require.Len(t, sent, 1)
	assert.Contains(t, sent[0].subject, "Backup OVERDUE")

	row, ok := alertRow(t, pool, s.jobID, AlertJobLate)
	require.True(t, ok)
	assert.Equal(t, subjectJob, row.SubjectType)

	// A fresh successful backup closes it and says so.
	addRun(t, pool, s.jobID, models.RunStatusSuccess, time.Minute)
	dm.Check()
	sent = alerter.matching(s.jobName)
	require.Len(t, sent, 2)
	assert.Contains(t, sent[1].subject, "Backup recovered")

	_, ok = alertRow(t, pool, s.jobID, AlertJobLate)
	assert.False(t, ok)
}

// A job that has never run and was due days ago is the "somebody disabled the cron"
// case, and it must be reported even though there is not a single run row to key off.
func TestDeadMan_JobThatNeverRan(t *testing.T) {
	pool := testDB(t)
	s := seed(t, pool, "0 3 * * *")

	_, err := pool.Exec(
		`UPDATE backup_jobs SET created_at = NOW() - INTERVAL '5 days' WHERE id=$1`, s.jobID)
	require.NoError(t, err)

	alerter := &mockAlerter{}
	newSwitch(pool, alerter).Check()

	sent := alerter.matching(s.jobName)
	require.Len(t, sent, 1)
	assert.Contains(t, sent[0].body, "never run")
}

// A job created moments ago has not missed anything, and must not alert.
func TestDeadMan_FreshJobIsQuiet(t *testing.T) {
	pool := testDB(t)
	s := seed(t, pool, "0 3 * * *")

	alerter := &mockAlerter{}
	newSwitch(pool, alerter).Check()
	assert.Empty(t, alerter.matching(s.jobName))
	assert.Empty(t, alerter.matching(s.agentName))
}

// Deleting the subject must not leave an alert that later "recovers" out of nowhere.
func TestDeadMan_OrphanAlertsAreDroppedSilently(t *testing.T) {
	pool := testDB(t)

	ghost := uuid.New()
	_, err := pool.Exec(`
		INSERT INTO alert_state (subject_type, subject_id, kind, first_seen_at, last_notified_at, notify_count, detail)
		VALUES ('agent', $1, $2, NOW() - INTERVAL '1 hour', NOW() - INTERVAL '1 hour', 1, 'gone')`,
		ghost, AlertAgentOffline)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Exec(`DELETE FROM alert_state WHERE subject_id=$1`, ghost) }) //nolint:errcheck

	alerter := &mockAlerter{}
	newSwitch(pool, alerter).Check()

	assert.Empty(t, alerter.matching(ghost.String()),
		"a deleted agent must not be announced as recovered")
	var n int
	require.NoError(t, pool.Get(&n, `SELECT COUNT(*) FROM alert_state WHERE subject_id=$1`, ghost))
	assert.Zero(t, n)
}

func TestDeadMan_LoadJobHealthIsClassified(t *testing.T) {
	pool := testDB(t)
	s := seed(t, pool, "0 3 * * *")
	addRun(t, pool, s.jobID, models.RunStatusSuccess, time.Hour)

	rows, err := LoadJobHealth(pool, time.Now(), 30*time.Minute)
	require.NoError(t, err)

	var found bool
	for _, r := range rows {
		if r.JobID != s.jobID {
			continue
		}
		found = true
		assert.Equal(t, HealthOK, r.Status)
		require.NotNil(t, r.LastRunID)
		require.NotNil(t, r.LastSuccessAt)
		assert.Equal(t, s.agentName, r.AgentName)
	}
	assert.True(t, found, "the seeded job must appear in the health snapshot")
}

// Overlap skips (attempt = 0) record an execution that never happened, so they must
// not be mistaken for "the job ran".
func TestDeadMan_SkippedRunsDoNotCountAsRuns(t *testing.T) {
	pool := testDB(t)
	s := seed(t, pool, "0 3 * * *")

	addRun(t, pool, s.jobID, models.RunStatusSuccess, 10*24*time.Hour)
	_, err := pool.Exec(`
		INSERT INTO backup_runs (job_id, status, attempt, started_at, finished_at, error_message)
		VALUES ($1, 'failed', 0, NOW(), NOW(), 'Skipped: a previous run of this job is still in progress')`,
		s.jobID)
	require.NoError(t, err)

	rows, err := LoadJobHealth(pool, time.Now(), time.Minute)
	require.NoError(t, err)
	for _, r := range rows {
		if r.JobID != s.jobID {
			continue
		}
		require.NotNil(t, r.LastRunStartedAt)
		assert.WithinDuration(t, time.Now().Add(-10*24*time.Hour), *r.LastRunStartedAt, time.Minute,
			"the attempt=0 skip must not be reported as the last run")
		assert.Equal(t, HealthLate, r.Status)
	}
}

func TestDeadManConfigFromEnv(t *testing.T) {
	t.Setenv("DEADMAN_CHECK_INTERVAL", "90s")
	t.Setenv("DEADMAN_AGENT_OFFLINE_AFTER", "wrong")
	t.Setenv("DEADMAN_ENABLED", "false")

	cfg := DeadManConfigFromEnv()
	assert.Equal(t, 90*time.Second, cfg.CheckInterval)
	assert.Equal(t, DefaultAgentOfflineAfter, cfg.AgentOfflineAfter,
		"a typo in a threshold falls back to the default rather than breaking the server")
	assert.False(t, cfg.Enabled)
	assert.Equal(t, DefaultJobGrace, cfg.JobGrace)
}

func TestDeadManSwitch_StartStop(t *testing.T) {
	dm := NewDeadManSwitch(nil, &mockAlerter{}, DeadManConfig{Enabled: true})
	dm.Start()
	dm.Stop()
	dm.Stop() // idempotent
}
