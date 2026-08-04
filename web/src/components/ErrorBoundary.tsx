import { Component, Fragment, type ReactNode, type ErrorInfo } from 'react'

interface Props {
  children: ReactNode
  fallback?: ReactNode
  /**
   * When this value changes the boundary clears itself. Pass the current route
   * so navigating away from a broken page recovers the app instead of leaving
   * the fallback pinned until a manual reload.
   */
  resetKey?: unknown
}

interface State {
  hasError: boolean
  error?: Error
  /** Bumped by "Try again" to force the subtree to remount from scratch. */
  resetCount: number
}

export class ErrorBoundary extends Component<Props, State> {
  constructor(props: Props) {
    super(props)
    this.state = { hasError: false, resetCount: 0 }
  }

  static getDerivedStateFromError(error: Error): Partial<State> {
    return { hasError: true, error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // In production you'd send this to an error tracking service
    console.error('ErrorBoundary caught:', error, info)
  }

  componentDidUpdate(prevProps: Props) {
    if (this.state.hasError && prevProps.resetKey !== this.props.resetKey) {
      this.setState(s => ({ hasError: false, error: undefined, resetCount: s.resetCount + 1 }))
    }
  }

  private retry = () => {
    this.setState(s => ({ hasError: false, error: undefined, resetCount: s.resetCount + 1 }))
  }

  private reload = () => {
    window.location.reload()
  }

  render() {
    if (this.state.hasError) {
      return this.props.fallback ?? (
        <div
          role="alert"
          className="flex flex-col items-center justify-center min-h-[200px] text-center p-6"
        >
          <p className="text-lg font-medium text-destructive mb-2">Something went wrong</p>
          <p className="text-sm text-muted-foreground mb-4">
            {this.state.error?.message || 'An unexpected error occurred while rendering this page.'}
          </p>
          <div className="flex items-center gap-4">
            <button type="button" className="text-sm text-primary underline" onClick={this.retry}>
              Try again
            </button>
            <button
              type="button"
              className="text-sm text-muted-foreground underline"
              onClick={this.reload}
            >
              Reload page
            </button>
          </div>
        </div>
      )
    }
    // Keying on resetCount discards the subtree that threw, so "Try again"
    // re-runs it from a clean state instead of replaying the same bad render.
    return <Fragment key={this.state.resetCount}>{this.props.children}</Fragment>
  }
}
