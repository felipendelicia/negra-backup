/**
 * Whether the agent will accept this path as a restore destination.
 *
 * The check on the other side is `filepath.IsAbs` **on the agent's OS**
 * (cmd/agent/remote_restore.go), so it depends on where the files are going to
 * land, not on what this browser runs. With the agent's OS unknown, either form
 * is allowed and the agent keeps the last word.
 */
export function isAbsolutePath(p: string, os: string | undefined): boolean {
  const t = p.trim()
  if (!t) return false
  const windows = /^[A-Za-z]:[\\/]/.test(t) || t.startsWith('\\\\')
  const posix = t.startsWith('/')
  if (os === 'windows') return windows
  if (os) return posix
  return windows || posix
}
