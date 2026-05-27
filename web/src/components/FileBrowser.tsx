import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from 'src/lib/api'
import { Button } from 'src/components/ui/button'
import { Folder, File, ChevronRight, Plus, X, ChevronLeft, Loader2 } from 'lucide-react'
import { cn } from 'src/lib/utils'

interface Props {
  agentId: string
  selectedPaths: string[]
  onChange: (paths: string[]) => void
}

export function FileBrowser({ agentId, selectedPaths, onChange }: Props) {
  const [cwd, setCwd] = useState('/')
  const [history, setHistory] = useState<string[]>([])

  const { data: entries = [], isFetching, isError } = useQuery({
    queryKey: ['browse', agentId, cwd],
    queryFn: ({ signal }) => api.browseAgent(agentId, cwd, signal),
    enabled: !!agentId,
    staleTime: 30_000,
    retry: false,
  })

  const dirs  = entries.filter(e => e.is_dir).sort((a, b) => a.name.localeCompare(b.name))
  const files = entries.filter(e => !e.is_dir).sort((a, b) => a.name.localeCompare(b.name))

  function navigate(path: string) {
    setHistory(h => [...h, cwd])
    setCwd(path)
  }

  function goBack() {
    const prev = history[history.length - 1]
    if (prev !== undefined) {
      setHistory(h => h.slice(0, -1))
      setCwd(prev)
    }
  }

  function addPath(path: string) {
    if (!selectedPaths.includes(path)) {
      onChange([...selectedPaths, path])
    }
  }

  function removePath(path: string) {
    onChange(selectedPaths.filter(p => p !== path))
  }

  // Breadcrumb segments from cwd
  const segments = cwd === '/' ? [''] : cwd.split('/').filter(Boolean)

  return (
    <div className="space-y-3">
      {/* Selected paths */}
      {selectedPaths.length > 0 && (
        <div className="space-y-1">
          <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Selected paths</p>
          <div className="space-y-1">
            {selectedPaths.map(p => (
              <div key={p} className="flex items-center gap-2 bg-muted/60 border border-border rounded px-2.5 py-1.5">
                <Folder className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
                <span className="font-mono text-xs flex-1 truncate">{p}</span>
                <button
                  onClick={() => removePath(p)}
                  className="text-muted-foreground hover:text-destructive transition-colors"
                >
                  <X className="h-3.5 w-3.5" />
                </button>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Browser panel */}
      <div className="border border-border rounded overflow-hidden">
        {/* Toolbar */}
        <div className="flex items-center gap-1 px-2 py-1.5 bg-muted/40 border-b border-border">
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="h-6 w-6"
            onClick={goBack}
            disabled={history.length === 0}
          >
            <ChevronLeft className="h-3.5 w-3.5" />
          </Button>

          {/* Breadcrumb */}
          <div className="flex items-center gap-0.5 flex-1 overflow-x-auto font-mono text-xs text-muted-foreground">
            <button
              type="button"
              className="hover:text-foreground px-1 shrink-0"
              onClick={() => { setHistory([]); setCwd('/') }}
            >
              /
            </button>
            {segments.map((seg, i) => {
              if (!seg) return null
              const segPath = '/' + segments.slice(0, i + 1).join('/')
              return (
                <span key={segPath} className="flex items-center gap-0.5 shrink-0">
                  <ChevronRight className="h-3 w-3 opacity-40" />
                  <button
                    type="button"
                    className="hover:text-foreground px-0.5"
                    onClick={() => { setHistory(h => [...h, cwd]); setCwd(segPath) }}
                  >
                    {seg}
                  </button>
                </span>
              )
            })}
          </div>

          {isFetching && <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground shrink-0" />}

          {/* Add current dir button */}
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-6 gap-1 text-xs shrink-0"
            onClick={() => addPath(cwd)}
            disabled={selectedPaths.includes(cwd)}
            title={`Add ${cwd}`}
          >
            <Plus className="h-3 w-3" />
            Add here
          </Button>
        </div>

        {/* Entries */}
        <div className="max-h-52 overflow-y-auto">
          {isError && (
            <p className="text-xs text-destructive px-3 py-3">
              Could not list directory — agent may be offline.
            </p>
          )}
          {!isError && entries.length === 0 && !isFetching && (
            <p className="text-xs text-muted-foreground px-3 py-3">Empty directory.</p>
          )}
          {[...dirs, ...files].map(entry => {
            const isSelected = selectedPaths.includes(entry.path)
            return (
              <div
                key={entry.path}
                className={cn(
                  'flex items-center gap-2 px-3 py-1.5 hover:bg-accent/50 transition-colors group',
                  isSelected && 'bg-accent/30',
                )}
              >
                {entry.is_dir
                  ? <Folder className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
                  : <File    className="h-3.5 w-3.5 text-muted-foreground/50 shrink-0" />
                }
                <button
                  type="button"
                  className={cn(
                    'flex-1 text-left font-mono text-xs truncate',
                    entry.is_dir ? 'hover:text-foreground' : 'text-muted-foreground cursor-default',
                  )}
                  onClick={() => entry.is_dir && navigate(entry.path)}
                >
                  {entry.name}
                  {entry.is_dir && '/'}
                </button>
                {entry.is_dir && (
                  <button
                    type="button"
                    className={cn(
                      'opacity-0 group-hover:opacity-100 transition-opacity',
                      isSelected ? 'text-muted-foreground' : 'text-muted-foreground hover:text-foreground',
                    )}
                    onClick={() => isSelected ? removePath(entry.path) : addPath(entry.path)}
                    title={isSelected ? 'Remove path' : 'Add path'}
                  >
                    {isSelected
                      ? <X    className="h-3.5 w-3.5 text-destructive" />
                      : <Plus className="h-3.5 w-3.5" />
                    }
                  </button>
                )}
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}
