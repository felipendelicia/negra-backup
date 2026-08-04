package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Exclusion patterns
//
// # Syntax
//
// The syntax is a deliberate subset of .gitignore — the one notation a sysadmin
// already knows — with one feature removed and one added:
//
//	*.log                     matches any file or directory named "*.log", at any depth
//	node_modules              matches an entry called node_modules, at any depth
//	.cache/                   trailing "/": matches only if the entry is a directory
//	www/*/cache               contains "/": matches that sequence of segments anywhere
//	**/dist                   "**" matches zero or more path segments
//	/var/www/*/node_modules   leading "/": anchored, matched against the absolute path
//
// Inside a single segment, `*`, `?` and `[a-z]` classes work as in filepath.Match.
//
// Two decisions worth spelling out:
//
//   - Negations ("!important.log") are NOT supported. Re-including something inside an
//     excluded directory is fundamentally incompatible with pruning the walk, and
//     pruning is the entire point: with `node_modules` excluded we must never descend
//     into it, so we cannot know whether something inside it was re-included. Paying a
//     full traversal of every node_modules to support a rare rule is a bad trade for a
//     backup agent.
//   - Anchoring is by absolute path, not by "relative to the repo root" as in git.
//     There is no repository here: a job lists several unrelated roots, so
//     "/var/www/*/node_modules" can only sensibly mean the real filesystem path. A
//     pattern that contains a "/" but does not start with one matches any suffix of the
//     path ("www/*/cache" matches /srv/www/a/cache), which is what an operator writing
//     a partial path expects.
//
// # Implementation
//
// filepath.Match cannot be used on a whole path, because its `*` also matches `/`
// ("/var/*" would match "/var/a/b/c"). So the pattern and the path are split on "/"
// and matched segment by segment, with filepath.Match applied to one segment at a
// time — which is exactly the semantics it was designed for — and "**" handled by the
// segment matcher itself.
//
// Matching is case-insensitive on Windows only, mirroring the filesystem.
type excludeMatcher struct {
	pats []excludePattern
	// cwd is captured once so that an anchored (absolute) pattern can still be matched
	// against a walk that was started from a relative path. Empty if it could not be
	// determined; anchored patterns then only match paths that are already absolute.
	cwd string
}

type excludePattern struct {
	// raw is the pattern as the operator wrote it, kept for error messages.
	raw string
	// segs is the pattern split on "/", already lowercased on Windows. Relative
	// patterns that contain a "/" carry an implicit leading "**".
	segs []string
	// anchored patterns are matched against the absolute path.
	anchored bool
	// dirOnly patterns (trailing "/") only match directories.
	dirOnly bool
	// base patterns (no "/" at all) are matched against the entry's base name.
	base bool
}

// newExcludeMatcher compiles the patterns. It returns a nil matcher when there is
// nothing to exclude, so the walk pays nothing for the common case. Blank entries are
// ignored: a textarea in the UI produces them and failing a nightly backup over a
// stray newline would be absurd.
func newExcludeMatcher(patterns []string) (*excludeMatcher, error) {
	if len(patterns) == 0 {
		return nil, nil
	}

	m := &excludeMatcher{}
	needAbs := false
	for _, raw := range patterns {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		pat, err := compileExcludePattern(trimmed)
		if err != nil {
			return nil, err
		}
		m.pats = append(m.pats, pat)
		needAbs = needAbs || pat.anchored
	}
	if len(m.pats) == 0 {
		return nil, nil
	}
	if needAbs {
		if wd, err := os.Getwd(); err == nil {
			m.cwd = wd
		}
	}
	return m, nil
}

func compileExcludePattern(raw string) (excludePattern, error) {
	pat := excludePattern{raw: raw}

	s := normalizeForMatch(raw)
	if strings.HasSuffix(s, "/") {
		pat.dirOnly = true
		s = strings.TrimRight(s, "/")
	}
	if s == "" || s == "." {
		return pat, fmt.Errorf("exclude pattern %q: matches nothing usable", raw)
	}

	pat.anchored = strings.HasPrefix(s, "/") || filepath.IsAbs(filepath.FromSlash(s))
	pat.base = !strings.Contains(s, "/")

	segs := splitSegments(s)
	if len(segs) == 0 {
		return pat, fmt.Errorf("exclude pattern %q: matches nothing usable", raw)
	}
	// Reject malformed character classes at configuration time instead of silently
	// never matching at 3am. filepath.Match only reports ErrBadPattern for the part it
	// actually scans, so this is best-effort validation, not a full parser.
	for _, seg := range segs {
		if seg == "**" {
			continue
		}
		if _, err := filepath.Match(seg, "probe"); err != nil {
			return pat, fmt.Errorf("exclude pattern %q: %w", raw, err)
		}
	}

	// A relative pattern with a "/" matches any suffix of the path, which is precisely
	// what a leading "**" does.
	if !pat.anchored && !pat.base {
		segs = append([]string{"**"}, segs...)
	}
	pat.segs = segs

	return pat, nil
}

// match reports whether path must be left out of the backup. A nil matcher excludes
// nothing.
func (m *excludeMatcher) match(path string, isDir bool) bool {
	if m == nil || len(m.pats) == 0 {
		return false
	}

	norm := normalizeForMatch(filepath.Clean(path))
	base := norm[strings.LastIndex(norm, "/")+1:]

	// Both splits are done at most once per entry, and only if some pattern needs them.
	var relSegs, absSegs []string

	for _, p := range m.pats {
		if p.dirOnly && !isDir {
			continue
		}

		if p.base {
			if ok, _ := filepath.Match(p.segs[0], base); ok {
				return true
			}
			continue
		}

		segs := relSegs
		if p.anchored {
			if absSegs == nil {
				absSegs = splitSegments(m.absPath(norm))
			}
			segs = absSegs
		} else if relSegs == nil {
			relSegs = splitSegments(norm)
			segs = relSegs
		}

		if matchSegments(p.segs, segs) {
			return true
		}
	}

	return false
}

// matchSegments matches a segmented pattern against a segmented path. "**" consumes
// zero or more segments; every other segment is delegated to filepath.Match, which
// never crosses a separator because there are none left inside a segment.
//
// The backtracking is exponential in the number of "**" in a single pattern. Patterns
// are short, human-written config, so this is not worth a DP table.
func matchSegments(pat, name []string) bool {
	if len(pat) == 0 {
		return len(name) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(name); i++ {
			if matchSegments(pat[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	ok, err := filepath.Match(pat[0], name[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pat[1:], name[1:])
}

// absPath resolves a walk path against the working directory captured at compile time
// so anchored patterns keep working when the job lists a relative root.
func (m *excludeMatcher) absPath(norm string) string {
	if strings.HasPrefix(norm, "/") || filepath.IsAbs(filepath.FromSlash(norm)) {
		return norm
	}
	if m.cwd == "" {
		return norm
	}
	return normalizeForMatch(filepath.Join(m.cwd, filepath.FromSlash(norm)))
}

// normalizeForMatch puts a path or a pattern in the one form the matcher understands:
// forward slashes, and lowercase on Windows, where the filesystem is case-insensitive
// and an operator will not write "AppData" the same way twice.
func normalizeForMatch(s string) string {
	s = filepath.ToSlash(s)
	if runtime.GOOS == "windows" {
		s = strings.ToLower(s)
	}
	return s
}

// splitSegments splits a slash-separated path into its non-empty segments, dropping
// the leading "/" of absolute paths and any "//" duplicates.
func splitSegments(s string) []string {
	parts := strings.Split(s, "/")
	segs := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		segs = append(segs, part)
	}
	return segs
}
