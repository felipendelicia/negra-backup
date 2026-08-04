/**
 * Files a run left out of the archive.
 *
 * A backup that silently dropped files is worse than one that fails, so this is
 * surfaced everywhere a run is shown. There is no column for it: the agent
 * reports the counts on `job_done`, and internal/ws/handler.go writes them into
 * the run log. Parsing those lines is the only way to see it from the API.
 *
 * The three shapes written by the server (internal/ws/handler.go and
 * cmd/agent/main.go `skippedSample`) are:
 *
 *   completed: <path> (sha256:…) — 12 entries skipped, 3 of them suspicious
 *   skipped: /var/run/x.sock — socket
 *   SKIPPED (suspicious): /root/secret — open: permission denied
 *
 * The separator is an em dash with spaces around it, and the sample is bounded
 * by the agent — so `samples.length` is usually smaller than `total`.
 */
export interface SkipEntry {
  path: string
  reason: string
  /** Permission denied, locked file: a symptom of something. Sockets are not. */
  suspicious: boolean
}

export interface SkipSummary {
  total: number
  suspicious: number
  samples: SkipEntry[]
  /** True when the counts came from the run's own summary line rather than
   *  from counting the (bounded) sample. */
  exact: boolean
}

/** The summary the server appends to the `completed:` line. */
const SUMMARY_RE = /(\d+)\s+entries skipped,\s*(\d+)\s+of them suspicious/

const SUSPICIOUS_PREFIX = 'SKIPPED (suspicious): '
const SKIPPED_PREFIX = 'skipped: '

/** Splits "<path> — <reason>" on the first em dash, keeping the whole line as
 *  the path when the reason is missing rather than dropping the entry. */
function splitPathReason(rest: string): { path: string; reason: string } {
  const i = rest.indexOf(' — ')
  if (i === -1) return { path: rest.trim(), reason: '' }
  return { path: rest.slice(0, i).trim(), reason: rest.slice(i + 3).trim() }
}

/** Reads one log line as a skip sample, or null if it is not one. */
export function parseSkipLine(line: string): SkipEntry | null {
  if (line.startsWith(SUSPICIOUS_PREFIX)) {
    return { ...splitPathReason(line.slice(SUSPICIOUS_PREFIX.length)), suspicious: true }
  }
  if (line.startsWith(SKIPPED_PREFIX)) {
    return { ...splitPathReason(line.slice(SKIPPED_PREFIX.length)), suspicious: false }
  }
  return null
}

/**
 * Extracts what a run skipped from its log.
 *
 * Takes anything with a `line` — the stored `RunLogLine[]` and the merged
 * stored+live lines the drawer renders both qualify.
 *
 * Returns null when the run left nothing out, so a caller can render the common
 * case as "nothing to say" without checking counts.
 */
export function summarizeSkips(lines: readonly { line: string }[] | undefined): SkipSummary | null {
  if (!lines || lines.length === 0) return null

  const samples: SkipEntry[] = []
  let total = 0
  let suspicious = 0
  let exact = false

  for (const { line } of lines) {
    if (!exact && line.startsWith('completed:')) {
      const m = SUMMARY_RE.exec(line)
      if (m) {
        total = Number(m[1])
        suspicious = Number(m[2])
        exact = true
      }
      continue
    }
    const entry = parseSkipLine(line)
    if (entry) samples.push(entry)
  }

  if (!exact) {
    total = samples.length
    suspicious = samples.filter(s => s.suspicious).length
  }

  if (total === 0) return null
  return { total, suspicious, samples, exact }
}
