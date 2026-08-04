import { useState, useEffect, useRef } from 'react'
import { Copy, Check } from 'lucide-react'
import { cn } from 'src/lib/utils'

interface Props {
  text: string
  /** Describes *what* is copied, for screen readers: "Copy restore command". */
  label?: string
  /** Hide the text label and render the icon only. */
  iconOnly?: boolean
  className?: string
}

/**
 * Copy-to-clipboard button with a transient confirmation.
 *
 * `navigator.clipboard` is unavailable on insecure origins, so a failure is
 * surfaced instead of silently doing nothing.
 */
export function CopyButton({ text, label = 'Copy', iconOnly = false, className }: Props) {
  const [state, setState] = useState<'idle' | 'copied' | 'error'>('idle')
  const timer = useRef<number | null>(null)

  // The timeout outlives the click, so it has to be cancelled on unmount to
  // avoid setting state on a gone component.
  useEffect(() => () => {
    if (timer.current !== null) window.clearTimeout(timer.current)
  }, [])

  function flash(next: 'copied' | 'error') {
    setState(next)
    if (timer.current !== null) window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => setState('idle'), 2000)
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(text)
      flash('copied')
    } catch {
      flash('error')
    }
  }

  const text_ = state === 'copied' ? 'Copied' : state === 'error' ? 'Copy failed' : label

  return (
    <button
      type="button"
      onClick={copy}
      aria-label={label}
      title={label}
      className={cn(
        'inline-flex items-center gap-1 text-xs transition-colors',
        state === 'error' ? 'text-destructive' : 'text-muted-foreground hover:text-foreground',
        className,
      )}
    >
      {state === 'copied'
        ? <Check aria-hidden="true" className="h-3.5 w-3.5 text-emerald-500" />
        : <Copy aria-hidden="true" className="h-3.5 w-3.5" />}
      {iconOnly ? <span className="sr-only">{text_}</span> : text_}
    </button>
  )
}
