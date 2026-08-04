// internal/backup/progress_internal_test.go
package backup

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These exercise the throttle directly. The behavioural tests in progress_test.go
// can only reach it through a deliberately slowed backup, which makes them slow and
// timing-dependent; here the state is driven straight.

func TestProgressReporter_NilWhenNoCallback(t *testing.T) {
	require.Nil(t, newProgressReporter(nil, 10),
		"a nil callback must produce a nil reporter so TarCompress keeps its zero-overhead path")

	var r *progressReporter
	assert.NotPanics(t, func() {
		r.report(TarProgress{})
		r.finish()
	}, "the nil reporter must be safe to call")
}

func TestProgressReporter_ThrottlesWithinTheInterval(t *testing.T) {
	var calls []int
	r := newProgressReporter(func(pct int, _ string) { calls = append(calls, pct) }, 100)

	// First report always goes out: the UI needs an initial value.
	r.report(TarProgress{FilesDone: 1})
	require.Len(t, calls, 1)

	// Same instant, higher percent: suppressed by the interval floor.
	for i := 2; i <= 50; i++ {
		r.report(TarProgress{FilesDone: i})
	}
	assert.Len(t, calls, 1, "updates inside the interval must be dropped")

	// Once the window has passed, the next advance is emitted.
	r.mu.Lock()
	r.lastEmit = time.Now().Add(-2 * progressMinInterval)
	r.mu.Unlock()

	r.report(TarProgress{FilesDone: 60})
	assert.Len(t, calls, 2)
}

func TestProgressReporter_NeverGoesBackwardsOrPast99(t *testing.T) {
	var calls []int
	r := newProgressReporter(func(pct int, _ string) { calls = append(calls, pct) }, 10)

	// More files archived than the counting walk found: the tree changed underneath.
	// The percent must saturate rather than exceed 100.
	for i := 1; i <= 40; i++ {
		r.mu.Lock()
		r.lastEmit = time.Now().Add(-2 * progressMinInterval)
		r.mu.Unlock()
		r.report(TarProgress{FilesDone: i})
	}

	require.NotEmpty(t, calls)
	last := -1
	for i, pct := range calls {
		assert.GreaterOrEqual(t, pct, last, "call %d went backwards", i)
		assert.LessOrEqual(t, pct, 99, "intermediate updates must stay below 100")
		last = pct
	}

	r.finish()
	assert.Equal(t, 100, calls[len(calls)-1], "finish must land exactly on 100")
}

// finish bypasses the throttle: a backup that completes inside the interval must
// still report 100, otherwise the UI is stuck at whatever it saw last.
func TestProgressReporter_FinishBypassesThrottle(t *testing.T) {
	var calls []int
	r := newProgressReporter(func(pct int, _ string) { calls = append(calls, pct) }, 10)

	r.report(TarProgress{FilesDone: 1})
	r.finish()

	require.Len(t, calls, 2)
	assert.Equal(t, 100, calls[1])
}

// report runs on the compression goroutine while finish runs on the caller's.
// Run with -race.
func TestProgressReporter_ConcurrentReportAndFinish(t *testing.T) {
	var mu sync.Mutex
	r := newProgressReporter(func(int, string) {
		mu.Lock()
		defer mu.Unlock()
	}, 1000)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			r.report(TarProgress{FilesDone: i})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			r.finish()
		}
	}()
	wg.Wait()
}
