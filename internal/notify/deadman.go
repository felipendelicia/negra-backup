// internal/notify/deadman.go
//
// A dead-man's switch for the backup system.
//
// Until now the only email that ever left this server came from
// internal/ws/handler.go on a `job_failed` message from an agent. That covers
// exactly one failure mode: a backup that ran and broke. It says nothing about the
// far more dangerous one — a backup that never ran at all. An agent that has been
// unplugged for three days, a job someone disabled by accident, a cron entry that
// was never loaded: all of them produce total silence, and silence is
// indistinguishable from success. This file makes absence observable.
//
// Three questions are asked on every pass:
//
//  1. Did a job's scheduled tick come and go with nothing running? (the cron
//     expression says when it was due; robfig/cron computes it)
//  2. Has an agent stopped sending heartbeats?
//  3. Is a job's last *successful* backup older than its own interval allows?
//
// The classification used for (1) and (3) is the same one the API exposes as the
// per-job `health` field, so the UI and the alerts can never disagree about whether
// a job is fine.
package notify

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/felipendelicia/nat-backup/internal/scheduler"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/robfig/cron/v3"
)

// HealthStatus is the derived per-job verdict. It is a small closed set on purpose:
// it is rendered by the UI and matched on by the alerting code, and both need to
// agree on the vocabulary.
type HealthStatus string

const (
	// HealthOK — the job ran when it was supposed to and has a recent success.
	HealthOK HealthStatus = "ok"
	// HealthRunning — a run is in flight right now; no verdict yet.
	HealthRunning HealthStatus = "running"
	// HealthLate — the job is *silent*: a scheduled tick passed with nothing
	// running, or the newest successful backup is older than the schedule allows.
	// This is the state the dead-man's switch alerts on.
	HealthLate HealthStatus = "late"
	// HealthFailing — the job did run, recently, and the run failed. Already covered
	// by the per-run failure email, so the dead-man's switch stays quiet about it.
	HealthFailing HealthStatus = "failing"
	// HealthNeverRun — an enabled job that has never produced a single run.
	HealthNeverRun HealthStatus = "never_run"
	// HealthDisabled — not scheduled, so nothing to judge. A disabled job is not an
	// alert: somebody turned it off deliberately. It is still reported so "why is
	// this not backed up" has an answer.
	HealthDisabled HealthStatus = "disabled"
	// HealthUnknown — the cron expression cannot be parsed or never fires, so there
	// is no "should have run by now" to compare against.
	HealthUnknown HealthStatus = "unknown"
)

// Alert subject types and kinds. They are the primary key of alert_state, so
// changing a string here orphans the open alerts of a running installation.
const (
	subjectAgent = "agent"
	subjectJob   = "job"

	// AlertAgentOffline fires when an agent stops heartbeating.
	AlertAgentOffline = "agent_offline"
	// AlertJobLate fires when a job is silent — see HealthLate / HealthNeverRun.
	// One kind for both, because a job that missed its tick almost always also has
	// a stale last success, and two alerts for one problem is the same spam by a
	// different route. The reason is carried in the alert detail.
	AlertJobLate = "job_late"
)

// ---------------------------------------------------------------------------
// Per-job health
// ---------------------------------------------------------------------------

// JobHealth is one job joined with the two facts needed to judge it: its most
// recent run and its most recent *successful* run.
//
// The db tags are filled by jobHealthQuery; the fields below the line are derived
// in Go by Classify.
type JobHealth struct {
	JobID         uuid.UUID  `db:"job_id"`
	JobName       string     `db:"job_name"`
	Enabled       bool       `db:"enabled"`
	ScheduleCron  string     `db:"schedule_cron"`
	JobCreatedAt  time.Time  `db:"job_created_at"`
	AgentID       uuid.UUID  `db:"agent_id"`
	AgentName     string     `db:"agent_name"`
	AgentLastSeen *time.Time `db:"agent_last_seen"`

	LastRunID         *uuid.UUID `db:"last_run_id"`
	LastRunStatus     *string    `db:"last_run_status"`
	LastRunStartedAt  *time.Time `db:"last_run_started_at"`
	LastRunFinishedAt *time.Time `db:"last_run_finished_at"`
	LastRunError      *string    `db:"last_run_error"`
	LastSuccessAt     *time.Time `db:"last_success_at"`

	// Derived by Classify.
	Status HealthStatus `db:"-"`
	Detail string       `db:"-"`
	// DueAt is the scheduled tick the job missed, when Status is HealthLate for
	// that reason. Nil otherwise.
	DueAt *time.Time `db:"-"`
	// Interval is the gap between two consecutive fires of the schedule, as
	// measured around the evaluation time. Zero when the cron cannot be parsed.
	Interval time.Duration `db:"-"`
}

// jobHealthQuery answers "when did each job last run, and when did it last work" in
// a single round trip.
//
// Two LATERAL joins rather than a query per job: the naive version is an N+1 that
// costs one round trip per job on a page that lists all of them. Each LATERAL is a
// LIMIT 1 backwards walk of idx_backup_runs_job_id_started_at (migration 003), so
// the cost is one index descent per job regardless of how many runs it has.
//
// `attempt >= 1` excludes overlap skips. Those rows record an execution that was
// *not* attempted (see migration 004); counting one as "the last run" would make a
// job that never actually runs — because the previous run never finishes — look like
// it is running fine, which is precisely the lie this file exists to expose.
//
// last_success uses started_at rather than finished_at: it is the conservative
// choice (older by the duration of the run), and a dead-man's switch should err
// towards alerting sooner.
const jobHealthQuery = `
SELECT j.id             AS job_id,
       j.name           AS job_name,
       j.enabled        AS enabled,
       j.schedule_cron  AS schedule_cron,
       j.created_at     AS job_created_at,
       a.id             AS agent_id,
       a.name           AS agent_name,
       a.last_seen      AS agent_last_seen,
       lr.id            AS last_run_id,
       lr.status        AS last_run_status,
       lr.started_at    AS last_run_started_at,
       lr.finished_at   AS last_run_finished_at,
       lr.error_message AS last_run_error,
       ls.started_at    AS last_success_at
FROM backup_jobs j
JOIN agents a ON a.id = j.agent_id
LEFT JOIN LATERAL (
    SELECT r.id, r.status, r.started_at, r.finished_at, r.error_message
    FROM backup_runs r
    WHERE r.job_id = j.id AND r.attempt >= 1
    ORDER BY r.started_at DESC, r.id DESC
    LIMIT 1
) lr ON true
LEFT JOIN LATERAL (
    SELECT r.started_at
    FROM backup_runs r
    WHERE r.job_id = j.id AND r.status = 'success'
    ORDER BY r.started_at DESC, r.id DESC
    LIMIT 1
) ls ON true
ORDER BY j.created_at DESC`

// LoadJobHealth returns every job with its last run, last success and derived
// health. It is the single source of truth for the `health` field of
// GET /api/jobs and for the job half of the dead-man's switch.
func LoadJobHealth(db *sqlx.DB, now time.Time, grace time.Duration) ([]JobHealth, error) {
	if db == nil {
		return nil, nil
	}
	rows := []JobHealth{}
	if err := db.Select(&rows, jobHealthQuery); err != nil {
		return nil, fmt.Errorf("load job health: %w", err)
	}
	for i := range rows {
		rows[i].Classify(now, grace)
	}
	return rows, nil
}

// Classify derives Status, Detail, DueAt and Interval.
//
// The criterion, in the order it is applied — the order is the whole design, so it
// is spelled out rather than left to be read off the code:
//
//	disabled   the job is not scheduled at all
//	unknown    the cron expression does not parse, or never fires again
//	running    a run is in flight; nothing to judge until it ends
//	never_run  no run has ever been recorded for this job
//	late       (a) the last scheduled tick was more than `grace` ago and no run
//	               started at or after it, or
//	           (b) the newest successful run is older than one interval + grace
//	failing    the last run ended in failure or was cancelled
//	ok         everything else
//
// Why "late" outranks "failing": a job whose last run failed three days ago and has
// not run since is not merely failing, it is *silent*, and silence is what nobody is
// being told about today. Ranking failing first would hide it behind a single email
// sent three days ago.
//
// `grace` absorbs the gap between "the minute the cron says" and "the moment the run
// row appears": the scheduler's own tick, agent hand-off, and the check interval of
// the switch itself. It must be larger than the check interval or a job will be
// reported late every time it is inspected between the tick and the dispatch.
func (j *JobHealth) Classify(now time.Time, grace time.Duration) {
	if !j.Enabled {
		j.Status, j.Detail = HealthDisabled, "job is disabled; it is not scheduled to run"
		return
	}

	sched, err := scheduler.ParseCron(j.ScheduleCron)
	if err != nil {
		j.Status = HealthUnknown
		j.Detail = fmt.Sprintf("schedule %q cannot be parsed: %v", j.ScheduleCron, err)
		return
	}
	interval, ok := scheduleInterval(sched, now)
	if !ok {
		j.Status = HealthUnknown
		j.Detail = fmt.Sprintf("schedule %q never fires again", j.ScheduleCron)
		return
	}
	j.Interval = interval

	if j.LastRunStatus != nil && *j.LastRunStatus == models.RunStatusRunning {
		j.Status = HealthRunning
		j.Detail = "a run is in progress"
		return
	}

	// The search never starts before the job existed: a job created this morning
	// has not missed the ticks of last week.
	due, hasDue := previousFire(sched, j.JobCreatedAt, now)
	tickMissed := hasDue && now.Sub(due) > grace &&
		(j.LastRunStartedAt == nil || j.LastRunStartedAt.Before(due))

	if j.LastRunStartedAt == nil {
		if tickMissed {
			d := due
			j.DueAt = &d
			j.Status = HealthNeverRun
			j.Detail = fmt.Sprintf("has never run; it was first due %s ago (%s)",
				humanDuration(now.Sub(due)), due.Format(time.RFC3339))
			return
		}
		j.Status = HealthNeverRun
		j.Detail = "has never run yet; its first scheduled execution has not come due"
		return
	}

	if tickMissed {
		d := due
		j.DueAt = &d
		j.Status = HealthLate
		j.Detail = fmt.Sprintf("was due %s ago (%s) and did not run; last run started %s ago",
			humanDuration(now.Sub(due)), due.Format(time.RFC3339),
			humanDuration(now.Sub(*j.LastRunStartedAt)))
		return
	}

	// A successful backup older than a whole interval plus the grace window means at
	// least one cycle produced nothing usable, whatever the run rows say.
	staleAfter := interval + grace
	switch {
	case j.LastSuccessAt == nil:
		if now.Sub(j.JobCreatedAt) > staleAfter {
			j.Status = HealthLate
			j.Detail = fmt.Sprintf("no successful backup has ever completed; the job has existed for %s",
				humanDuration(now.Sub(j.JobCreatedAt)))
			return
		}
	case now.Sub(*j.LastSuccessAt) > staleAfter:
		j.Status = HealthLate
		j.Detail = fmt.Sprintf("last successful backup was %s ago; this job is expected to succeed every %s",
			humanDuration(now.Sub(*j.LastSuccessAt)), humanDuration(interval))
		return
	}

	if j.LastRunStatus != nil &&
		(*j.LastRunStatus == models.RunStatusFailed || *j.LastRunStatus == models.RunStatusCancelled) {
		j.Status = HealthFailing
		j.Detail = fmt.Sprintf("last run %s", *j.LastRunStatus)
		if j.LastRunError != nil && *j.LastRunError != "" {
			j.Detail += ": " + firstLine(*j.LastRunError)
		}
		return
	}

	j.Status = HealthOK
	j.Detail = fmt.Sprintf("last successful backup %s ago", humanDuration(now.Sub(*j.LastSuccessAt)))
}

// maxFireSteps bounds the walk in previousFire so a pathological schedule
// (`@every 1s`) cannot spin the checker.
const maxFireSteps = 2000

// scheduleInterval measures the gap between the next two fires of a schedule.
//
// robfig/cron only exposes Next, so the interval is sampled rather than derived. For
// an irregular schedule ("0 9,17 * * *") the sample depends on where `now` falls,
// which is fine for both callers: it is used to size a search window and to set a
// staleness threshold, and both are already padded by the grace window.
func scheduleInterval(sched cron.Schedule, now time.Time) (time.Duration, bool) {
	first := sched.Next(now)
	if first.IsZero() {
		return 0, false
	}
	second := sched.Next(first)
	if second.IsZero() {
		return 0, false
	}
	d := second.Sub(first)
	if d <= 0 {
		return 0, false
	}
	return d, true
}

// previousFire returns the most recent time the schedule fired at or before `now`,
// never earlier than `notBefore`.
//
// robfig/cron has no Prev, so the answer is found by walking Next forward from a
// start point. The start point is three intervals back rather than a fixed window:
// for a yearly cron a fixed 30-day window would find nothing, and for a
// minute-by-minute cron it would mean 43200 useless steps. Three intervals covers
// irregular schedules (a weekday-only job measured over a weekend) while keeping the
// walk to a handful of iterations in every realistic case.
//
// Known limitation, `@every <duration>`: cron models it as a constant delay with no
// absolute phase (Next(t) is always t+d), so walking forward from any start point
// lands on `now` and this function can never report a missed tick for such a job.
// That is why the classifier does not rely on the missed-tick rule alone: the
// stale-success rule below catches `@every` jobs, and it needs no phase.
func previousFire(sched cron.Schedule, notBefore, now time.Time) (time.Time, bool) {
	interval, ok := scheduleInterval(sched, now)
	if !ok {
		return time.Time{}, false
	}
	from := now.Add(-3 * interval)
	if from.Before(notBefore) {
		from = notBefore
	}

	var last time.Time
	found := false
	t := sched.Next(from)
	for steps := 0; !t.IsZero() && !t.After(now) && steps < maxFireSteps; steps++ {
		last, found = t, true
		t = sched.Next(t)
	}
	return last, found
}

// ---------------------------------------------------------------------------
// Per-agent health
// ---------------------------------------------------------------------------

// AgentHealth is one agent with enough context to decide whether its silence
// matters.
type AgentHealth struct {
	AgentID     uuid.UUID  `db:"agent_id"`
	Name        string     `db:"name"`
	LastSeen    *time.Time `db:"last_seen"`
	CreatedAt   time.Time  `db:"created_at"`
	EnabledJobs int        `db:"enabled_jobs"`
	TotalJobs   int        `db:"total_jobs"`

	// Derived.
	Online    bool          `db:"-"`
	SilentFor time.Duration `db:"-"`
}

// agentHealthQuery counts each agent's jobs alongside its heartbeat in one pass.
// agents.status is deliberately not read: it is a cache that cmd/server resets to
// 'offline' at boot and the ws handler sets to 'online' on hello, so it says what
// the hub believes rather than when the agent was last actually heard from.
// last_seen is the fact.
const agentHealthQuery = `
SELECT a.id         AS agent_id,
       a.name       AS name,
       a.last_seen  AS last_seen,
       a.created_at AS created_at,
       COUNT(j.id) FILTER (WHERE j.enabled) AS enabled_jobs,
       COUNT(j.id)                          AS total_jobs
FROM agents a
LEFT JOIN backup_jobs j ON j.agent_id = a.id
GROUP BY a.id, a.name, a.last_seen, a.created_at
ORDER BY a.name`

// LoadAgentHealth returns every agent with its heartbeat age.
//
// An agent that has never connected is measured from created_at, so an agent
// registered in the UI and never installed is eventually reported too — silently
// never installing the agent is one of the ways a backup plan turns out not to
// exist.
func LoadAgentHealth(db *sqlx.DB, now time.Time, offlineAfter time.Duration) ([]AgentHealth, error) {
	if db == nil {
		return nil, nil
	}
	rows := []AgentHealth{}
	if err := db.Select(&rows, agentHealthQuery); err != nil {
		return nil, fmt.Errorf("load agent health: %w", err)
	}
	for i := range rows {
		since := rows[i].CreatedAt
		if rows[i].LastSeen != nil {
			since = *rows[i].LastSeen
		}
		rows[i].SilentFor = now.Sub(since)
		rows[i].Online = rows[i].SilentFor <= offlineAfter
	}
	return rows, nil
}

// ---------------------------------------------------------------------------
// The switch
// ---------------------------------------------------------------------------

// Alerter sends one notification. DBNotifier implements it; tests substitute their
// own. Kept separate from FailureNotifier so the dead-man's switch is not forced
// through a signature built around a run ID it does not have.
type Alerter interface {
	SendAlert(subject, body string) error
}

// Defaults. They are deliberately generous: the cost of a late alert is minutes, the
// cost of a false alert repeated daily is that everybody stops reading them.
const (
	// DefaultCheckInterval is how often the switch looks.
	//
	// Five minutes, because every threshold it compares against is measured in tens
	// of minutes: a faster tick would add nothing but load (the thresholds, not the
	// polling, set the detection latency), and cron itself has a one-minute
	// resolution so sub-minute polling is meaningless. Each pass is two indexed
	// queries plus one small table scan, so the steady-state cost is 24 queries an
	// hour.
	DefaultCheckInterval = 5 * time.Minute

	// DefaultAgentOfflineAfter is how much heartbeat silence is tolerated.
	//
	// Agents heartbeat every 30 s and the server's read deadline is 60 s, so 15
	// minutes is thirty missed beats: far beyond a network blip, a server restart or
	// an agent upgrade, and still fast enough that a machine that died overnight is
	// reported before the morning.
	DefaultAgentOfflineAfter = 15 * time.Minute

	// DefaultJobGrace is how late a scheduled run may be before it counts as missed.
	// It has to exceed DefaultCheckInterval (a job inspected between its tick and its
	// dispatch would otherwise read as late) with room for a slow hand-off to the
	// agent.
	DefaultJobGrace = 30 * time.Minute

	// DefaultRepeatInterval is how often an alert that is still open is repeated.
	//
	// Never repeating means a five-day outage produces one email that scrolls away on
	// day one. Repeating every pass means 288 emails a day. Once a day is a reminder;
	// it is also the natural period of a backup system.
	DefaultRepeatInterval = 24 * time.Hour
)

// DeadManConfig holds the tunables. Zero values are replaced by the defaults above,
// so a caller can set only what it cares about.
type DeadManConfig struct {
	Enabled           bool
	CheckInterval     time.Duration
	AgentOfflineAfter time.Duration
	JobGrace          time.Duration
	RepeatInterval    time.Duration
}

// withDefaults fills in the zero values.
func (c DeadManConfig) withDefaults() DeadManConfig {
	if c.CheckInterval <= 0 {
		c.CheckInterval = DefaultCheckInterval
	}
	if c.AgentOfflineAfter <= 0 {
		c.AgentOfflineAfter = DefaultAgentOfflineAfter
	}
	if c.JobGrace <= 0 {
		c.JobGrace = DefaultJobGrace
	}
	if c.RepeatInterval <= 0 {
		c.RepeatInterval = DefaultRepeatInterval
	}
	return c
}

// DeadManConfigFromEnv reads the thresholds from the environment.
//
// They live here rather than in internal/config because they are this subsystem's
// vocabulary and nothing else reads them; an unparseable value falls back to the
// default with a warning instead of refusing to boot, since a typo in an alerting
// threshold must not take the backup server down.
//
//	DEADMAN_ENABLED               default true
//	DEADMAN_CHECK_INTERVAL        default 5m
//	DEADMAN_AGENT_OFFLINE_AFTER   default 15m
//	DEADMAN_JOB_GRACE             default 30m
//	DEADMAN_REPEAT_INTERVAL       default 24h
func DeadManConfigFromEnv() DeadManConfig {
	cfg := DeadManConfig{Enabled: true}
	if v := os.Getenv("DEADMAN_ENABLED"); v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "0", "false", "no", "off":
			cfg.Enabled = false
		}
	}
	cfg.CheckInterval = envDuration("DEADMAN_CHECK_INTERVAL", DefaultCheckInterval)
	cfg.AgentOfflineAfter = envDuration("DEADMAN_AGENT_OFFLINE_AFTER", DefaultAgentOfflineAfter)
	cfg.JobGrace = envDuration("DEADMAN_JOB_GRACE", DefaultJobGrace)
	cfg.RepeatInterval = envDuration("DEADMAN_REPEAT_INTERVAL", DefaultRepeatInterval)
	return cfg
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		log.Printf("deadman: ignoring invalid %s=%q, using %s", key, v, def)
		return def
	}
	return d
}

// DeadManSwitch periodically compares what should have happened against what did.
type DeadManSwitch struct {
	db      *sqlx.DB
	alerter Alerter
	cfg     DeadManConfig

	// now is injectable so the tests can place the clock relative to a cron tick
	// instead of sleeping.
	now func() time.Time

	done     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func NewDeadManSwitch(db *sqlx.DB, alerter Alerter, cfg DeadManConfig) *DeadManSwitch {
	return &DeadManSwitch{
		db:      db,
		alerter: alerter,
		cfg:     cfg.withDefaults(),
		now:     time.Now,
		done:    make(chan struct{}),
	}
}

// Start runs the check loop until Stop.
//
// The first pass is deliberately delayed by one interval: at boot the hub is empty
// and every agent has just been reset to 'offline', so checking immediately would
// alert on agents that are about to reconnect within seconds.
func (s *DeadManSwitch) Start() {
	if s.db == nil {
		return
	}
	if !s.cfg.Enabled {
		log.Println("deadman: disabled by configuration; nobody will be told when a backup silently stops running")
		return
	}
	s.wg.Add(1)
	go s.loop()
	log.Printf("deadman: watching (check every %s, agent offline after %s, job grace %s, repeat every %s)",
		s.cfg.CheckInterval, s.cfg.AgentOfflineAfter, s.cfg.JobGrace, s.cfg.RepeatInterval)
}

func (s *DeadManSwitch) Stop() {
	s.stopOnce.Do(func() { close(s.done) })
	s.wg.Wait()
}

func (s *DeadManSwitch) loop() {
	defer s.wg.Done()
	// A panic in a watchdog must not take the server with it: the process that
	// notices nothing is running is the last one that should die quietly.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("deadman: check loop panic: %v", r)
		}
	}()

	ticker := time.NewTicker(s.cfg.CheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.Check()
		}
	}
}

// firing is one condition that is true right now.
type firing struct {
	subjectType string
	subjectID   uuid.UUID
	kind        string
	name        string
	detail      string
}

type alertKey struct {
	subjectType string
	subjectID   uuid.UUID
	kind        string
}

func (f firing) key() alertKey {
	return alertKey{f.subjectType, f.subjectID, f.kind}
}

// OpenAlert is a row of alert_state: a condition the switch has reported and that
// has not recovered yet. Exported so the API can show what the switch is currently
// complaining about instead of leaving it buried in an inbox.
type OpenAlert struct {
	SubjectType    string     `db:"subject_type"    json:"subject_type"`
	SubjectID      uuid.UUID  `db:"subject_id"      json:"subject_id"`
	Kind           string     `db:"kind"            json:"kind"`
	FirstSeenAt    time.Time  `db:"first_seen_at"   json:"first_seen_at"`
	LastNotifiedAt *time.Time `db:"last_notified_at" json:"last_notified_at"`
	NotifyCount    int        `db:"notify_count"    json:"notify_count"`
	Detail         string     `db:"detail"          json:"detail"`
}

func (a OpenAlert) key() alertKey {
	return alertKey{a.SubjectType, a.SubjectID, a.Kind}
}

// LoadOpenAlerts returns every alert the dead-man's switch currently holds open.
func LoadOpenAlerts(db *sqlx.DB) ([]OpenAlert, error) {
	if db == nil {
		return nil, nil
	}
	rows := []OpenAlert{}
	if err := db.Select(&rows, `
		SELECT subject_type, subject_id, kind, first_seen_at, last_notified_at, notify_count, detail
		FROM alert_state
		ORDER BY first_seen_at`); err != nil {
		return nil, fmt.Errorf("load alert state: %w", err)
	}
	return rows, nil
}

// Check runs one full pass: evaluate every condition, open the new ones, repeat the
// stale ones, and close the ones that recovered. It is exported so tests (and, if it
// ever matters, an operator endpoint) can drive it a tick at a time.
func (s *DeadManSwitch) Check() {
	if s.db == nil {
		return
	}
	now := s.now()

	current, err := s.evaluate(now)
	if err != nil {
		log.Printf("deadman: %v", err)
		return
	}

	// Alerts whose subject was deleted are dropped before anything else. Otherwise
	// deleting an agent would leave an open alert that no condition can ever match
	// again, and the next pass would announce that a nonexistent agent had recovered.
	s.dropOrphanAlerts()

	open, err := s.loadOpenAlerts()
	if err != nil {
		log.Printf("deadman: %v", err)
		return
	}

	firingByKey := make(map[alertKey]firing, len(current))
	for _, f := range current {
		firingByKey[f.key()] = f
	}

	for _, f := range current {
		s.raise(f, open[f.key()], now)
	}
	for key, a := range open {
		if _, still := firingByKey[key]; !still {
			s.resolve(a, now)
		}
	}
}

// evaluate collects every condition that is true at `now`.
func (s *DeadManSwitch) evaluate(now time.Time) ([]firing, error) {
	agents, err := LoadAgentHealth(s.db, now, s.cfg.AgentOfflineAfter)
	if err != nil {
		return nil, err
	}
	jobs, err := LoadJobHealth(s.db, now, s.cfg.JobGrace)
	if err != nil {
		return nil, err
	}

	var out []firing

	for _, a := range agents {
		// An agent with no enabled job is not part of anyone's backup plan; alerting
		// on it would be noise from a machine nobody is relying on.
		if a.EnabledJobs == 0 || a.Online {
			continue
		}
		detail := fmt.Sprintf("no heartbeat for %s", humanDuration(a.SilentFor))
		if a.LastSeen == nil {
			detail = fmt.Sprintf("has never connected since it was registered %s ago",
				humanDuration(a.SilentFor))
		}
		out = append(out, firing{
			subjectType: subjectAgent,
			subjectID:   a.AgentID,
			kind:        AlertAgentOffline,
			name:        a.Name,
			detail: fmt.Sprintf("%s (threshold %s). %d enabled backup job(s) depend on it.",
				detail, s.cfg.AgentOfflineAfter, a.EnabledJobs),
		})
	}

	for _, j := range jobs {
		// HealthFailing is deliberately absent: a failed run already sends its own
		// email from internal/ws/handler.go and internal/scheduler. This switch
		// reports absence, not failure, and duplicating the failure mail is the
		// fastest way to get all of them ignored.
		if j.Status != HealthLate && j.Status != HealthNeverRun {
			continue
		}
		// A never_run job whose first tick has not come due yet is not late.
		if j.Status == HealthNeverRun && j.DueAt == nil {
			continue
		}
		out = append(out, firing{
			subjectType: subjectJob,
			subjectID:   j.JobID,
			kind:        AlertJobLate,
			name:        j.JobName,
			detail: fmt.Sprintf("%s. Schedule: %s. Agent: %s.",
				j.Detail, j.ScheduleCron, j.AgentName),
		})
	}

	sort.Slice(out, func(i, k int) bool {
		if out[i].kind != out[k].kind {
			return out[i].kind < out[k].kind
		}
		return out[i].name < out[k].name
	})
	return out, nil
}

func (s *DeadManSwitch) loadOpenAlerts() (map[alertKey]OpenAlert, error) {
	rows, err := LoadOpenAlerts(s.db)
	if err != nil {
		return nil, err
	}
	out := make(map[alertKey]OpenAlert, len(rows))
	for _, r := range rows {
		out[r.key()] = r
	}
	return out, nil
}

func (s *DeadManSwitch) dropOrphanAlerts() {
	if _, err := s.db.Exec(`
		DELETE FROM alert_state
		WHERE (subject_type = 'agent' AND NOT EXISTS (SELECT 1 FROM agents a WHERE a.id = subject_id))
		   OR (subject_type = 'job'   AND NOT EXISTS (SELECT 1 FROM backup_jobs j WHERE j.id = subject_id))
		   OR subject_type NOT IN ('agent', 'job')`); err != nil {
		log.Printf("deadman: prune orphan alerts: %v", err)
	}
}

// raise records a firing condition and notifies if it is new or due for a reminder.
//
// The row is written before the notification and marked notified only after the send
// succeeds. That ordering matters: if SMTP is down, the alert stays open with
// notify_count = 0 and is retried on the next pass instead of being recorded as
// delivered and never mentioned again.
func (s *DeadManSwitch) raise(f firing, existing OpenAlert, now time.Time) {
	isNew := existing.Kind == ""
	if _, err := s.db.Exec(`
		INSERT INTO alert_state (subject_type, subject_id, kind, first_seen_at, detail)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (subject_type, subject_id, kind)
		DO UPDATE SET detail = EXCLUDED.detail`,
		f.subjectType, f.subjectID, f.kind, now, f.detail); err != nil {
		log.Printf("deadman: record alert %s/%s: %v", f.kind, f.subjectID, err)
		return
	}
	if isNew {
		log.Printf("deadman: ALERT %s %s (%s): %s", f.subjectType, f.name, f.kind, f.detail)
	}

	// Anti-spam, the whole point of the alert_state table: notify once when the
	// condition opens, then at most once per RepeatInterval for as long as it holds.
	// An agent down for five days sends 5 emails, not 1440.
	if existing.NotifyCount > 0 && existing.LastNotifiedAt != nil &&
		now.Sub(*existing.LastNotifiedAt) < s.cfg.RepeatInterval {
		return
	}

	firstSeen := now
	if !isNew {
		firstSeen = existing.FirstSeenAt
	}
	subject, body := alertMessage(f, firstSeen, now, existing.NotifyCount)
	if !s.send(subject, body) {
		return
	}
	if _, err := s.db.Exec(`
		UPDATE alert_state SET last_notified_at = $4, notify_count = notify_count + 1
		WHERE subject_type = $1 AND subject_id = $2 AND kind = $3`,
		f.subjectType, f.subjectID, f.kind, now); err != nil {
		log.Printf("deadman: mark alert notified %s/%s: %v", f.kind, f.subjectID, err)
	}
}

// resolve closes an alert whose condition no longer holds and announces the
// recovery — the other half of the information, and the half that tells the operator
// they can stop worrying without having to go and look.
//
// The row is deleted only once the recovery notice is out, so a failing SMTP server
// means the notice is retried on the next pass rather than lost. If the condition
// fires again in the meantime the row is simply still there and the alert continues,
// which is the correct outcome.
func (s *DeadManSwitch) resolve(a OpenAlert, now time.Time) {
	// Never notified means nobody was ever told this was broken, so telling them it
	// is fixed is pure noise. Drop it quietly.
	if a.NotifyCount > 0 {
		name := s.subjectName(a)
		subject, body := recoveryMessage(a, name, now)
		if !s.send(subject, body) {
			return
		}
		log.Printf("deadman: RECOVERED %s %s (%s)", a.SubjectType, name, a.Kind)
	}
	if _, err := s.db.Exec(`
		DELETE FROM alert_state WHERE subject_type=$1 AND subject_id=$2 AND kind=$3`,
		a.SubjectType, a.SubjectID, a.Kind); err != nil {
		log.Printf("deadman: clear alert %s/%s: %v", a.Kind, a.SubjectID, err)
	}
}

// send returns true when the alert was delivered (or when there is nothing to
// deliver it with, in which case nothing is recorded as notified either).
func (s *DeadManSwitch) send(subject, body string) bool {
	if s.alerter == nil {
		log.Printf("deadman: no notifier configured, not sending: %s", subject)
		return false
	}
	if err := s.alerter.SendAlert(subject, body); err != nil {
		log.Printf("deadman: send %q: %v", subject, err)
		return false
	}
	return true
}

// subjectName resolves the human name of an alert's subject, falling back to the id
// when the row is gone (which dropOrphanAlerts normally prevents).
func (s *DeadManSwitch) subjectName(a OpenAlert) string {
	table := "agents"
	if a.SubjectType == subjectJob {
		table = "backup_jobs"
	}
	var name string
	// #nosec G201 -- table is chosen from a two-value constant set above, never input.
	if err := s.db.Get(&name, `SELECT name FROM `+table+` WHERE id=$1`, a.SubjectID); err != nil {
		return a.SubjectID.String()
	}
	return name
}

func alertMessage(f firing, firstSeen, now time.Time, previousNotifications int) (string, string) {
	what := "Agent OFFLINE"
	if f.subjectType == subjectJob {
		what = "Backup OVERDUE"
	}
	subject := fmt.Sprintf("[nat-backup] %s: %s", what, f.name)
	if previousNotifications > 0 {
		subject = fmt.Sprintf("[nat-backup] STILL %s: %s", strings.ToUpper(what), f.name)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", f.detail)
	fmt.Fprintf(&b, "Subject: %s %q (%s)\n", f.subjectType, f.name, f.subjectID)
	fmt.Fprintf(&b, "Detected: %s\n", firstSeen.Format(time.RFC3339))
	if previousNotifications > 0 {
		fmt.Fprintf(&b, "Unresolved for: %s (reminder #%d)\n",
			humanDuration(now.Sub(firstSeen)), previousNotifications+1)
	}
	b.WriteString("\nThis alert comes from the nat-backup dead-man's switch: it reports backups that " +
		"did NOT happen, which no run-level notification can ever report.\n")
	return subject, b.String()
}

func recoveryMessage(a OpenAlert, name string, now time.Time) (string, string) {
	what := "Agent recovered"
	if a.SubjectType == subjectJob {
		what = "Backup recovered"
	}
	subject := fmt.Sprintf("[nat-backup] %s: %s", what, name)

	var b strings.Builder
	fmt.Fprintf(&b, "%s.\n\n", what)
	fmt.Fprintf(&b, "Subject: %s %q (%s)\n", a.SubjectType, name, a.SubjectID)
	fmt.Fprintf(&b, "Was: %s\n", a.Detail)
	fmt.Fprintf(&b, "Broken for: %s (%s → %s)\n",
		humanDuration(now.Sub(a.FirstSeenAt)),
		a.FirstSeenAt.Format(time.RFC3339), now.Format(time.RFC3339))
	return subject, b.String()
}

// humanDuration renders a duration the way an operator reads it in an alert.
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%.1f hours", d.Hours())
	default:
		return fmt.Sprintf("%.1f days", d.Hours()/24)
	}
}

// firstLine keeps an alert subject or detail from swallowing a multi-line stack
// trace out of error_message.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}
