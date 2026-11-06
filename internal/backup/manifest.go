package backup

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"time"

	"github.com/klauspost/compress/zstd"
)

// The manifest is the index of what a run actually archived.
//
// Without it there is no way to answer "what is inside this backup" short of
// downloading the artifact and decompressing it, because tar has no internal index:
// listing a 500 GB archive means streaming all 500 GB. That makes two things
// impossible — choosing a file to restore in the UI, and telling an incremental
// backup which files have changed since the last full.
//
// Shape: zstd-compressed JSON Lines, one object per entry, stored as a single blob
// per run rather than a row per file. A row per file would be more queryable and
// would also mean six million rows a month for one nightly job on a modest file
// server, with retention having to delete them all. One blob is a few megabytes for
// a tree that size, is deleted by a single cascade, and is exactly the form the agent
// needs when it loads the previous manifest to diff against.
//
// Field names are one or two characters on purpose. On a 200k-entry tree the
// difference between "path" and "p" is about 1.5 MB before compression, and this
// blob is transferred on every incremental.

// Manifest entry kinds.
const (
	ManifestKindFile    = "f"
	ManifestKindDir     = "d"
	ManifestKindSymlink = "l"
)

// ManifestEntry describes one member of an archive.
type ManifestEntry struct {
	// Path is the name inside the archive, which is what a restore filter matches
	// against — not the absolute path on the source host.
	Path string `json:"p"`
	Kind string `json:"k"`
	// Size is the byte count declared in the tar header. Zero for directories and
	// symlinks.
	Size int64 `json:"s,omitempty"`
	// Mode holds the permission bits only. The kind is already in Kind, and the
	// non-permission bits of fs.FileMode are not portable across the platforms an
	// agent may run on.
	Mode uint32 `json:"m,omitempty"`
	// ModTime is Unix seconds. Second resolution is what the incremental comparison
	// uses, and what most filesystems agree on after a copy.
	ModTime int64 `json:"t,omitempty"`
	// LinkTarget is set for symlinks.
	LinkTarget string `json:"lt,omitempty"`
}

// FileMode renders the permission bits the way a listing shows them.
func (e ManifestEntry) FileMode() fs.FileMode { return fs.FileMode(e.Mode).Perm() }

// ModTimeUTC is the entry's modification time, or the zero time when unrecorded.
func (e ManifestEntry) ModTimeUTC() time.Time {
	if e.ModTime == 0 {
		return time.Time{}
	}
	return time.Unix(e.ModTime, 0).UTC()
}

// maxManifestEntries bounds what one manifest may describe.
//
// A runaway walk — a symlink loop the exclusion patterns did not catch, or a tree
// nobody expected to be that large — must not turn into an unbounded blob that the
// agent buffers, uploads and the server then holds in memory. Past this the manifest
// is marked truncated: the backup itself is unaffected, only its index is partial,
// and saying so is better than silently indexing the first half.
const maxManifestEntries = 2_000_000

// ManifestWriter streams entries out as compressed JSON Lines.
type ManifestWriter struct {
	zw    *zstd.Encoder
	bw    *bufio.Writer
	enc   *json.Encoder
	count int
	bytes int64

	truncated bool
	closed    bool
}

// NewManifestWriter wraps w. Close must be called to flush the compressor, or the
// blob is unreadable.
func NewManifestWriter(w io.Writer) (*ManifestWriter, error) {
	zw, err := zstd.NewWriter(w)
	if err != nil {
		return nil, fmt.Errorf("manifest zstd writer: %w", err)
	}
	// json.Encoder writes small pieces; without buffering each one is a separate
	// call into the compressor.
	bw := bufio.NewWriterSize(zw, 64*1024)
	return &ManifestWriter{zw: zw, bw: bw, enc: json.NewEncoder(bw)}, nil
}

// Add records one entry. Past maxManifestEntries it silently stops recording and
// marks the manifest truncated — the archive is still complete either way.
func (m *ManifestWriter) Add(e ManifestEntry) error {
	if m.count >= maxManifestEntries {
		m.truncated = true
		return nil
	}
	if err := m.enc.Encode(e); err != nil {
		return fmt.Errorf("write manifest entry: %w", err)
	}
	m.count++
	m.bytes += e.Size
	return nil
}

// Close flushes and finishes the compressed stream. It is safe to call twice.
func (m *ManifestWriter) Close() error {
	if m.closed {
		return nil
	}
	m.closed = true
	if err := m.bw.Flush(); err != nil {
		m.zw.Close()
		return fmt.Errorf("flush manifest: %w", err)
	}
	return m.zw.Close()
}

// Count is how many entries were recorded (not counting truncated ones).
func (m *ManifestWriter) Count() int { return m.count }

// TotalBytes is the sum of the recorded entry sizes.
func (m *ManifestWriter) TotalBytes() int64 { return m.bytes }

// Truncated reports whether entries were dropped because of maxManifestEntries.
func (m *ManifestWriter) Truncated() bool { return m.truncated }

// maxManifestLineBytes bounds a single JSON line while reading. A path is capped by
// the OS at a few thousand bytes, so anything past this is a corrupt or hostile blob
// rather than a real entry, and bufio.Scanner's default 64 KiB would reject long but
// legitimate lines on some filesystems.
const maxManifestLineBytes = 1 << 20

// ReadManifest streams a compressed manifest, calling fn for each entry.
//
// Streaming rather than returning a slice: the server pages through manifests to
// answer a UI request, and materialising two million entries to show twenty of them
// would put the memory cost of a scroll on the server.
//
// fn may return errStopManifest to stop early without producing an error.
func ReadManifest(r io.Reader, fn func(ManifestEntry) error) error {
	zr, err := zstd.NewReader(r)
	if err != nil {
		return fmt.Errorf("manifest zstd reader: %w", err)
	}
	defer zr.Close()

	scanner := bufio.NewScanner(zr)
	scanner.Buffer(make([]byte, 0, 64*1024), maxManifestLineBytes)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e ManifestEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return fmt.Errorf("parse manifest entry: %w", err)
		}
		if err := fn(e); err != nil {
			if errors.Is(err, ErrStopManifest) {
				return nil
			}
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	return nil
}

// ErrStopManifest ends a ReadManifest walk early without reporting a failure.
var ErrStopManifest = errors.New("stop reading manifest")
