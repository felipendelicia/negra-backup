package backup

import (
	"bytes"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These are internal on purpose: the matcher and the tolerance policy are not part of
// the package API (the API is FilesConfig.Exclude and BackupResult), but they carry
// the decisions worth pinning down one case at a time.

func TestExcludeMatcher_NilAndEmptyExcludeNothing(t *testing.T) {
	m, err := newExcludeMatcher(nil)
	require.NoError(t, err)
	require.Nil(t, m, "no patterns must produce no matcher, so the walk pays nothing")
	assert.False(t, m.match("/var/www/anything", false))

	// A textarea in the UI produces blank lines; failing a nightly backup over one
	// would be absurd.
	m, err = newExcludeMatcher([]string{"", "   ", "\n"})
	require.NoError(t, err)
	assert.Nil(t, m)
}

func TestExcludeMatcher_ByName(t *testing.T) {
	m, err := newExcludeMatcher([]string{"*.log", "node_modules", ".cache"})
	require.NoError(t, err)

	cases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"/var/www/app/error.log", false, true},
		{"/var/www/app/error.log.1", false, false},
		{"error.log", false, true},
		{"/srv/a/b/c/d/node_modules", true, true},
		{"/home/felipe/.cache", true, true},
		{"/home/felipe/.cacheX", true, false},
		{"/var/www/app/index.php", false, false},
		// A bare name must not match a path that merely contains it as a substring.
		{"/var/lib/node_modules_backup", true, false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, m.match(c.path, c.isDir), "match(%q)", c.path)
	}
}

func TestExcludeMatcher_ByPath(t *testing.T) {
	m, err := newExcludeMatcher([]string{
		"/var/www/*/node_modules", // absolute, one wildcard segment
		"www/*/cache",             // relative with a slash: matches any path suffix
		"**/dist",                 // explicit **
	})
	require.NoError(t, err)

	cases := []struct {
		path string
		want bool
	}{
		{"/var/www/site1/node_modules", true},
		{"/var/www/site2/node_modules", true},
		// One wildcard segment must not swallow separators: this is the filepath.Match
		// trap the segment-by-segment matcher exists to avoid.
		{"/var/www/a/b/node_modules", false},
		{"/srv/www/site1/node_modules", false},
		{"/srv/www/site1/cache", true},
		{"/opt/x/www/y/cache", true},
		{"/opt/www/cache", false},
		{"/home/app/dist", true},
		{"/dist", true},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, m.match(c.path, true), "match(%q)", c.path)
	}
}

func TestExcludeMatcher_DirOnlyPattern(t *testing.T) {
	m, err := newExcludeMatcher([]string{"cache/"})
	require.NoError(t, err)

	assert.True(t, m.match("/srv/app/cache", true))
	assert.False(t, m.match("/srv/app/cache", false), "a file named cache must survive a cache/ pattern")
}

func TestExcludeMatcher_AnchoredNeedsTheFullPath(t *testing.T) {
	m, err := newExcludeMatcher([]string{"/var/log"})
	require.NoError(t, err)

	assert.True(t, m.match("/var/log", true))
	assert.False(t, m.match("/srv/var/log", true),
		"a leading / anchors the pattern to the filesystem root")
}

func TestExcludeMatcher_RejectsMalformedPattern(t *testing.T) {
	_, err := newExcludeMatcher([]string{"[a-"})
	require.Error(t, err, "a broken character class must fail at configuration time, not silently never match")
	assert.ErrorIs(t, err, filepath.ErrBadPattern)

	_, err = newExcludeMatcher([]string{"/"})
	require.Error(t, err)
}

// The tolerance thresholds, driven directly: reaching them through real unreadable
// files would need thousands of them.
func TestSkipAccountant_Policy(t *testing.T) {
	denied := &fs.PathError{Op: "open", Path: "/x", Err: syscall.EACCES}

	t.Run("a few unreadable files in a small tree are forgiven", func(t *testing.T) {
		a := &skipAccountant{total: 20}
		for i := 0; i < skipSuspiciousGrace; i++ {
			require.NoError(t, a.record("/x", denied))
		}
		assert.Equal(t, skipSuspiciousGrace, a.suspicious)
	})

	t.Run("past the grace the ratio decides", func(t *testing.T) {
		a := &skipAccountant{total: 20}
		var err error
		for i := 0; i < skipSuspiciousGrace+1 && err == nil; i++ {
			err = a.record("/x", denied)
		}
		require.ErrorIs(t, err, ErrTooManySkipped, "11 of 20 files is not a backup")
	})

	t.Run("the ratio tolerates the same count in a large tree", func(t *testing.T) {
		a := &skipAccountant{total: 100000}
		for i := 0; i < 100; i++ {
			require.NoError(t, a.record("/x", denied))
		}
	})

	t.Run("the hard limit caps the ratio", func(t *testing.T) {
		a := &skipAccountant{total: 10000000}
		var err error
		for i := 0; i <= skipSuspiciousHardLimit && err == nil; i++ {
			err = a.record("/x", denied)
		}
		require.ErrorIs(t, err, ErrTooManySkipped,
			"5%% of a huge tree is still far too many files to lose silently")
	})

	t.Run("expected skips never count against the budget", func(t *testing.T) {
		a := &skipAccountant{total: 10}
		for i := 0; i < 500; i++ {
			require.NoError(t, a.record("/sock", errIrregularFile))
			require.NoError(t, a.record("/gone", &fs.PathError{Op: "open", Path: "/gone", Err: syscall.ENOENT}))
		}
		assert.Zero(t, a.suspicious)
		assert.Equal(t, 1000, a.count)
	})

	t.Run("an I/O error fails on the first occurrence", func(t *testing.T) {
		a := &skipAccountant{total: 100000}
		err := a.record("/dying", &fs.PathError{Op: "read", Path: "/dying", Err: syscall.EIO})
		require.ErrorIs(t, err, ErrIOFailure)
		assert.Zero(t, a.count, "a dying disk is not a skipped file, it is a failed backup")
	})
}

func TestSkipAccountant_SamplesAreBounded(t *testing.T) {
	denied := &fs.PathError{Op: "open", Path: "/x", Err: syscall.EACCES}

	// total large enough that the ratio never trips, so the sampling itself is tested.
	a := &skipAccountant{total: 10000000}
	for i := 0; i < 500; i++ {
		require.NoError(t, a.record("/denied", denied))
		require.NoError(t, a.record("/sock", errIrregularFile))
	}

	samples := a.samples()
	assert.Len(t, samples, maxSuspiciousSamples+maxExpectedSamples)
	assert.Equal(t, 1000, a.count)
	assert.Equal(t, 500, a.suspicious)
	for i, s := range samples[:maxSuspiciousSamples] {
		assert.True(t, s.Suspicious, "sample %d: suspicious entries must come first", i)
	}
}

// mkSocket creates a unix socket, which is the cheapest portable "entry the archiver
// must not try to read" available from the standard library.
func mkSocket(t *testing.T, path string) bool {
	t.Helper()
	if runtime.GOOS == "windows" {
		return false
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Logf("cannot create unix socket %s: %v", path, err)
		return false
	}
	t.Cleanup(func() { l.Close() })
	return true
}

// Excluding a directory must PRUNE it: the walk may not look at what is inside. The
// proof is a socket buried in the excluded directory — visiting it would necessarily
// produce a skip report, because there is no way to archive it.
func TestTarCompress_ExcludedDirectoryIsPruned(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "keep.txt"), []byte("keep"), 0o644))

	nm := filepath.Join(root, "node_modules", "pkg")
	require.NoError(t, os.MkdirAll(nm, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nm, "index.js"), []byte("x"), 0o644))
	if !mkSocket(t, filepath.Join(nm, "daemon.sock")) {
		t.Skip("unix sockets unavailable")
	}

	exclude, err := newExcludeMatcher([]string{"node_modules"})
	require.NoError(t, err)

	var visited []string
	var buf bytes.Buffer
	st, err := tarCompress([]string{root}, &buf, CompressionZstd, tarOptions{
		exclude: exclude,
		onSkip: func(p string, err error) error {
			visited = append(visited, p)
			return nil
		},
	})
	require.NoError(t, err)

	assert.Empty(t, visited,
		"the walk descended into the pruned directory: %v", visited)
	assert.Equal(t, 1, st.files, "only the file outside node_modules must be archived")

	// Without pruning the same tree is fully visited and the socket shows up.
	visited = nil
	buf.Reset()
	st, err = tarCompress([]string{root}, &buf, CompressionZstd, tarOptions{
		onSkip: func(p string, err error) error {
			visited = append(visited, p)
			return nil
		},
	})
	require.NoError(t, err)
	require.Len(t, visited, 1, "control: without exclusions the socket must be reached")
	assert.Equal(t, filepath.Join(nm, "daemon.sock"), visited[0])
	assert.Equal(t, 2, st.files)
}
