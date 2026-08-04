// internal/scheduler/scheduler_test.go
//
// This is an internal test package (package scheduler, not scheduler_test) so the
// retry scan can be driven one tick at a time. Waiting on retryScanInterval would
// make every retry test take 30 seconds.
package scheduler

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/felipendelicia/nat-backup/internal/crypto"
	"github.com/felipendelicia/nat-backup/internal/db"
	"github.com/felipendelicia/nat-backup/internal/models"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockDispatcher struct {
	mu    sync.Mutex
	err   error
	calls []string // run IDs, in dispatch order
}

func (m *mockDispatcher) DispatchJob(agentID, runID string, job models.BackupJob, storageType string, storageConfig json.RawMessage, passphrase string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, runID)
	return m.err
}

func (m *mockDispatcher) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func TestScheduler_StartStop(t *testing.T) {
	s := New(nil, &mockDispatcher{}, "")
	s.Start()
	s.Stop()
}

func TestScheduler_ParseCron(t *testing.T) {
	valid := []string{"0 2 * * *", "*/5 * * * *", "@daily"}
	for _, expr := range valid {
		err := ValidateCron(expr)
		require.NoError(t, err, "expected valid: %s", expr)
	}
	invalid := []string{"not-a-cron", "abc"}
	for _, expr := range invalid {
		err := ValidateCron(expr)
		assert.Error(t, err, "expected invalid: %s", expr)
	}
}

// --- database-backed tests -------------------------------------------------
//
// Same convention as internal/db: DATABASE_URL if set, the docker-compose DSN
// otherwise, skipped under -short.

const testEncKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

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

// seedJob creates an agent, an encrypted storage destination and a job wired to
// both, and registers cleanup. Deleting the agent cascades to the job and its runs.
func seedJob(t *testing.T, pool *sqlx.DB, retryMax, retryBackoffSeconds int) models.BackupJob {
	t.Helper()

	cfg, err := crypto.Encrypt(testEncKey, `{"path":"/tmp/nat-backup-test"}`)
	require.NoError(t, err)

	suffix := uuid.NewString()

	var agentID, destID uuid.UUID
	require.NoError(t, pool.QueryRow(
		`INSERT INTO agents (name, api_key) VALUES ($1, $2) RETURNING id`,
		"sched-test-"+suffix, "sched-test-key-"+suffix,
	).Scan(&agentID))
	require.NoError(t, pool.QueryRow(
		`INSERT INTO storage_destinations (name, type, config) VALUES ($1, 'local', $2) RETURNING id`,
		"sched-test-"+suffix, cfg,
	).Scan(&destID))

	t.Cleanup(func() {
		pool.Exec(`DELETE FROM agents WHERE id=$1`, agentID)              //nolint:errcheck
		pool.Exec(`DELETE FROM storage_destinations WHERE id=$1`, destID) //nolint:errcheck
	})

	var job models.BackupJob
	require.NoError(t, pool.QueryRowx(`
		INSERT INTO backup_jobs
		  (agent_id, name, enabled, type, source, storage_destination_id, schedule_cron,
		   compression, encrypt, retry_max, retry_backoff_seconds)
		VALUES ($1, $2, true, 'files', '{"paths":["/tmp"]}', $3, '@daily', 'zstd', false, $4, $5)
		RETURNING *`,
		agentID, "sched-test-"+suffix, destID, retryMax, retryBackoffSeconds,
	).StructScan(&job))
	return job
}

func runStatus(t *testing.T, pool *sqlx.DB, runID uuid.UUID) (status string, attempt int, errMsg *string) {
	t.Helper()
	require.NoError(t, pool.QueryRow(
		`SELECT status, attempt, error_message FROM backup_runs WHERE id=$1`, runID,
	).Scan(&status, &attempt, &errMsg))
	return
}

// markFailed closes a run and backdates finished_at so its backoff has elapsed.
func markFailed(t *testing.T, pool *sqlx.DB, runID uuid.UUID, ago time.Duration) {
	t.Helper()
	_, err := pool.Exec(
		`UPDATE backup_runs SET status='failed', finished_at=NOW() - make_interval(secs => $2),
		        error_message='boom' WHERE id=$1`,
		runID, ago.Seconds(),
	)
	require.NoError(t, err)
}

func TestBeginRun_RejectsOverlap(t *testing.T) {
	pool := testDB(t)
	job := seedJob(t, pool, 0, 300)

	first, err := BeginRun(pool, job.ID, AttemptFirst)
	require.NoError(t, err)
	require.Equal(t, AttemptFirst, first.Attempt)
	require.Equal(t, models.RunStatusRunning, first.Status)

	_, err = BeginRun(pool, job.ID, AttemptFirst)
	require.ErrorIs(t, err, ErrRunOverlap, "a second run must not start while the first is running")

	// Closing the first run frees the slot again.
	markFailed(t, pool, first.ID, 0)
	second, err := BeginRun(pool, job.ID, 2)
	require.NoError(t, err)
	require.Equal(t, 2, second.Attempt)
}

// TestBeginRun_ConcurrentSingleWinner is the reason the guard is a partial unique
// index and not a plain SELECT-then-INSERT.
//
// The rounds and the racer count are not decoration. With the index dropped and only
// `WHERE NOT EXISTS` in play, this shape lets 2 to 5 runs through in ~99% of rounds:
// under READ COMMITTED each statement reads the snapshot taken when it began, so
// every racer that starts before the winner commits sees "nothing running". A single
// round of a handful of goroutines passes even without the index, which would make
// this test worthless.
func TestBeginRun_ConcurrentSingleWinner(t *testing.T) {
	pool := testDB(t)
	job := seedJob(t, pool, 0, 300)

	const rounds, racers = 10, 16
	for round := 0; round < rounds; round++ {
		var (
			wg       sync.WaitGroup
			mu       sync.Mutex
			accepted int
			rejected int
		)
		start := make(chan struct{})
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := BeginRun(pool, job.ID, AttemptFirst)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					accepted++
				case errors.Is(err, ErrRunOverlap):
					rejected++
				default:
					t.Errorf("unexpected error: %v", err)
				}
			}()
		}
		close(start)
		wg.Wait()

		require.Equal(t, 1, accepted, "round %d: exactly one concurrent caller may start a run", round)
		require.Equal(t, racers-1, rejected, "round %d", round)

		var running int
		require.NoError(t, pool.Get(&running,
			`SELECT COUNT(*) FROM backup_runs WHERE job_id=$1 AND status='running'`, job.ID))
		require.Equal(t, 1, running, "round %d", round)

		// Free the slot for the next round.
		_, err := pool.Exec(`DELETE FROM backup_runs WHERE job_id=$1`, job.ID)
		require.NoError(t, err)
	}
}

func TestTriggerJob_SkipsWhileRunning(t *testing.T) {
	pool := testDB(t)
	job := seedJob(t, pool, 0, 300)

	inFlight, err := BeginRun(pool, job.ID, AttemptFirst)
	require.NoError(t, err)

	disp := &mockDispatcher{}
	s := New(pool, disp, testEncKey)
	s.triggerJob(job, AttemptFirst)

	assert.Zero(t, disp.count(), "the cron must not dispatch while a run is in flight")

	// The skip has to leave a trace, and it must not be retryable.
	var skipped struct {
		ID      uuid.UUID `db:"id"`
		Status  string    `db:"status"`
		Attempt int       `db:"attempt"`
		Msg     *string   `db:"error_message"`
	}
	require.NoError(t, pool.Get(&skipped, `
		SELECT id, status, attempt, error_message FROM backup_runs
		WHERE job_id=$1 AND id <> $2`, job.ID, inFlight.ID))
	assert.Equal(t, models.RunStatusFailed, skipped.Status)
	assert.Equal(t, attemptSkipped, skipped.Attempt)
	require.NotNil(t, skipped.Msg)
	assert.Contains(t, *skipped.Msg, "still in progress")
}

func TestScanRetries_RespectsRetryMax(t *testing.T) {
	pool := testDB(t)
	job := seedJob(t, pool, 2, minTestBackoff) // 2 retries => at most 3 attempts

	disp := &mockDispatcher{}
	s := New(pool, disp, testEncKey)

	// Attempt 1 fails long enough ago that the backoff has elapsed.
	first, err := BeginRun(pool, job.ID, AttemptFirst)
	require.NoError(t, err)
	markFailed(t, pool, first.ID, time.Hour/2)

	s.scanRetries()
	require.Equal(t, 1, disp.count(), "the first failure must be retried")
	attempt2 := latestRun(t, pool, job.ID)
	_, at2, _ := runStatus(t, pool, attempt2)
	assert.Equal(t, 2, at2)

	// Attempt 2 fails: one retry left.
	markFailed(t, pool, attempt2, time.Hour/2)
	s.scanRetries()
	require.Equal(t, 2, disp.count())
	attempt3 := latestRun(t, pool, job.ID)
	_, at3, _ := runStatus(t, pool, attempt3)
	assert.Equal(t, 3, at3)

	// Attempt 3 fails: retry_max is exhausted, nothing more may be dispatched.
	markFailed(t, pool, attempt3, time.Hour/2)
	s.scanRetries()
	assert.Equal(t, 2, disp.count(), "retry_max=2 must allow exactly 2 retries")

	// And repeated scans stay idempotent.
	s.scanRetries()
	assert.Equal(t, 2, disp.count())

	var total int
	require.NoError(t, pool.Get(&total, `SELECT COUNT(*) FROM backup_runs WHERE job_id=$1`, job.ID))
	assert.Equal(t, 3, total, "3 attempts total, no extra rows")
}

func TestScanRetries_HonoursBackoffAndDefaults(t *testing.T) {
	pool := testDB(t)

	// A job with the default policy (retry_max = 0) must never be retried.
	noRetry := seedJob(t, pool, 0, defaultTestBackoff)
	disp := &mockDispatcher{}
	s := New(pool, disp, testEncKey)
	run, err := BeginRun(pool, noRetry.ID, AttemptFirst)
	require.NoError(t, err)
	markFailed(t, pool, run.ID, time.Hour/2)
	s.scanRetries()
	assert.Zero(t, disp.count(), "retry_max=0 keeps the pre-migration behaviour")

	// A job that failed just now is still inside its backoff window.
	withRetry := seedJob(t, pool, 3, defaultTestBackoff)
	fresh, err := BeginRun(pool, withRetry.ID, AttemptFirst)
	require.NoError(t, err)
	markFailed(t, pool, fresh.ID, 0)
	s.scanRetries()
	assert.Zero(t, disp.count(), "a retry must wait for its backoff")

	// Past the grace window the failure stands: no stampede after a long outage.
	stale := seedJob(t, pool, 3, defaultTestBackoff)
	old, err := BeginRun(pool, stale.ID, AttemptFirst)
	require.NoError(t, err)
	markFailed(t, pool, old.ID, 72*time.Hour)
	s.scanRetries()
	assert.Zero(t, disp.count(), "a retry that came due days ago must not fire on restart")
}

// TestScanRetries_YieldsToRunningJob covers the interaction between the two features:
// a due retry must not start a second run of a job that is already going.
func TestScanRetries_YieldsToRunningJob(t *testing.T) {
	pool := testDB(t)
	job := seedJob(t, pool, 3, minTestBackoff)

	failed, err := BeginRun(pool, job.ID, AttemptFirst)
	require.NoError(t, err)
	markFailed(t, pool, failed.ID, time.Hour/2)

	// Someone triggered the job manually in the meantime.
	manual, err := BeginRun(pool, job.ID, AttemptFirst)
	require.NoError(t, err)

	disp := &mockDispatcher{}
	s := New(pool, disp, testEncKey)
	s.scanRetries()

	assert.Zero(t, disp.count(), "the retry must yield to the run already in progress")

	var running int
	require.NoError(t, pool.Get(&running,
		`SELECT COUNT(*) FROM backup_runs WHERE job_id=$1 AND status='running'`, job.ID))
	assert.Equal(t, 1, running)
	assert.Equal(t, manual.ID, latestRun(t, pool, job.ID))
}

func latestRun(t *testing.T, pool *sqlx.DB, jobID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, pool.Get(&id,
		`SELECT id FROM backup_runs WHERE job_id=$1 ORDER BY started_at DESC, id DESC LIMIT 1`, jobID))
	return id
}

const (
	// minTestBackoff is the lowest value the CHECK constraint allows, so the tests
	// can backdate finished_at by a small amount and still be past the backoff.
	minTestBackoff = 10
	// defaultTestBackoff matches the API default.
	defaultTestBackoff = 300
)
