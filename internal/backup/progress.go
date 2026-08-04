package backup

import (
	"io"
	"sync"
	"time"
)

// TarProgress is a raw, unthrottled progress report emitted by TarCompressProgress
// while it walks the paths and writes the archive.
//
// FilesDone counts the files already written in full. CurrentFile is the file being
// written right now and is NOT counted in FilesDone yet; CurrentDone/CurrentSize say
// how far it has been copied. When a file has just been finished, the report carries
// the incremented FilesDone with CurrentDone and CurrentSize both zero, so a consumer
// can always compute "files completed + fraction of the file in flight" without
// double-counting.
type TarProgress struct {
	FilesDone      int
	CurrentFile    string
	CurrentDone    int64
	CurrentSize    int64
	TotalBytesDone int64
}

// TarProgressFunc consumes TarProgress reports. It is called synchronously from the
// goroutine driving the archive, once per file completion and once per write chunk of
// the file in flight, so implementations must be cheap and must do their own
// throttling (see progressReporter).
type TarProgressFunc func(TarProgress)

// progressMinInterval is the minimum wall-clock gap between two OnProgress calls.
const progressMinInterval = 500 * time.Millisecond

// progressReporter turns the raw TarProgress stream into the throttled, monotonic
// (percent, currentFile) pairs that FilesConfig.OnProgress expects.
//
// Throttling policy: emit only when the integer percent actually advances, and never
// more often than once per progressMinInterval; the final 100% always fires. Every
// call becomes a job_progress WebSocket message, so the rate must not scale with the
// size of the backup. The two conditions bound it from both sides:
//
//   - "percent advanced" caps a whole run at 101 messages no matter how many files or
//     bytes it moves — a 200k-file tree emits at most ~100 updates, not 200k.
//   - the 500ms floor caps the burst rate for short runs, where the percent can climb
//     through many points in a few milliseconds.
//
// The alternative "every N files" was rejected because N says nothing about the
// message rate: N=1000 floods on a tree of 4 KB files and goes silent for hours on a
// tree of 50 GB files. Percent-based throttling is invariant to both.
//
// Concurrency: TarCompress runs on its own goroutine when encryption is on (see the
// io.Pipe pipeline in BackupFiles), so report() is driven from that goroutine while
// finish() is called from the goroutine that spawned it. The mutex keeps lastEmit /
// lastPercent race-free; it is uncontended in practice and costs nothing next to the
// compression work between two reports.
type progressReporter struct {
	fn         func(percent int, currentFile string)
	totalFiles int

	mu          sync.Mutex
	started     bool
	lastEmit    time.Time
	lastPercent int
}

// newProgressReporter returns nil when fn is nil so that callers can skip the whole
// progress machinery (TarCompress then takes its original zero-overhead path).
func newProgressReporter(fn func(percent int, currentFile string), totalFiles int) *progressReporter {
	if fn == nil {
		return nil
	}
	return &progressReporter{fn: fn, totalFiles: totalFiles}
}

// report is the TarProgressFunc handed to TarCompressProgress.
func (r *progressReporter) report(p TarProgress) {
	if r == nil {
		return
	}

	now := time.Now()

	r.mu.Lock()
	pct := r.percentLocked(p)
	if r.started && (pct <= r.lastPercent || now.Sub(r.lastEmit) < progressMinInterval) {
		r.mu.Unlock()
		return
	}
	r.started = true
	r.lastEmit = now
	r.lastPercent = pct
	r.mu.Unlock()

	r.fn(pct, p.CurrentFile)
}

// finish emits the terminal 100%, bypassing the throttle: the UI must always land on
// exactly 100 even if the backup was too fast (or too uniform) to produce any
// intermediate update.
func (r *progressReporter) finish() {
	if r == nil {
		return
	}

	r.mu.Lock()
	r.started = true
	r.lastEmit = time.Now()
	r.lastPercent = 100
	r.mu.Unlock()

	r.fn(100, "done")
}

// percentLocked maps a raw report to a percent in [lastPercent, 99]. Intermediate
// values are capped at 99 so that 100 is emitted exactly once, by finish(), and
// clamped to lastPercent so the bar never walks backwards — the file count comes from
// a first walk and the tree can change under it (files added or removed between the
// counting walk and the archiving walk).
func (r *progressReporter) percentLocked(p TarProgress) int {
	pct := 0
	if r.totalFiles > 0 {
		done := float64(p.FilesDone)
		// Fraction of the file currently in flight. This is what makes the database
		// path meaningful: it hands BackupFiles a single big dump file, where a
		// per-file percent would only ever be 0 or 100. It does not cover the dump
		// itself (pg_dump/mysqldump run before BackupFiles is called), so a DB job
		// stays at 0% until the dump finishes.
		if p.CurrentSize > 0 && p.CurrentDone > 0 {
			frac := float64(p.CurrentDone) / float64(p.CurrentSize)
			if frac > 1 {
				frac = 1
			}
			done += frac
		}
		pct = int(done * 100 / float64(r.totalFiles))
	}
	if pct > 99 {
		pct = 99
	}
	if pct < r.lastPercent {
		pct = r.lastPercent
	}
	return pct
}

// progressWriter counts the bytes copied into an underlying writer and reports the
// running total after every successful write, so progress keeps moving inside a single
// large file instead of freezing until it is done.
type progressWriter struct {
	w       io.Writer
	written int64
	onWrite func(written int64)
}

func (pw *progressWriter) Write(b []byte) (int, error) {
	n, err := pw.w.Write(b)
	if n > 0 {
		pw.written += int64(n)
		pw.onWrite(pw.written)
	}
	return n, err
}
