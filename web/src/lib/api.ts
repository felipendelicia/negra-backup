import { getToken, clearToken } from './auth'
import type {
  Agent, BackupJob, BackupRun, StorageDestination,
  NotificationSettings, CreateJobRequest, CreateStorageRequest,
  EmailNotificationConfig, RunsPage, RunsQuery, RunLogs, RunStats,
  JobListItem, HealthSummary, ArchiveListing, RestoreRequest, RestoreAccepted,
} from './types'

const LOGIN_PATH = '/login'

export class APIError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

async function request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const token = getToken()
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
  }
  if (token) {
    headers['Authorization'] = `Bearer ${token}`
  }

  const res = await fetch(path, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
    signal,
  })

  if (!res.ok) {
    // A 401 from /api/auth/* means "those credentials are wrong", not "your
    // session died" — swallowing it into a redirect would wipe the login form
    // (and its error message) before the user ever sees why it failed.
    if (res.status === 401 && !path.startsWith('/api/auth/')) {
      clearToken()
      if (window.location.pathname !== LOGIN_PATH) {
        // replace(), not href: the expired page must not stay in history.
        window.location.replace(LOGIN_PATH)
      }
    }
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new APIError(res.status, (err as { error?: string }).error || res.statusText)
  }

  if (res.status === 204) return undefined as T
  return res.json() as Promise<T>
}

export const api = {
  login: (username: string, password: string) =>
    request<{ token: string }>('POST', '/api/auth/login', { username, password }),

  listAgents: (signal?: AbortSignal) => request<Agent[]>('GET', '/api/agents', undefined, signal),
  createAgent: (name: string) => request<{ agent: Agent; api_key: string }>('POST', '/api/agents', { name }),
  deleteAgent: (id: string) => request<void>('DELETE', `/api/agents/${id}`),
  updateAgent: (id: string) => request<void>('POST', `/api/agents/${id}/update`),
  browseAgent: (id: string, path: string, signal?: AbortSignal) =>
    request<{ name: string; path: string; is_dir: boolean }[]>('GET', `/api/agents/${id}/browse?path=${encodeURIComponent(path)}`, undefined, signal),

  listStorage: (signal?: AbortSignal) => request<StorageDestination[]>('GET', '/api/storage-destinations', undefined, signal),
  createStorage: (data: CreateStorageRequest) =>
    request<StorageDestination>('POST', '/api/storage-destinations', data),
  updateStorage: (id: string, data: CreateStorageRequest) =>
    request<void>('PUT', `/api/storage-destinations/${id}`, data),
  deleteStorage: (id: string) => request<void>('DELETE', `/api/storage-destinations/${id}`),

  // The jobs list carries the derived health of every job (last run, next run,
  // verdict, agent liveness). Create/update still take a plain CreateJobRequest:
  // those fields are computed, never written.
  listJobs: (signal?: AbortSignal) => request<JobListItem[]>('GET', '/api/jobs', undefined, signal),
  createJob: (data: CreateJobRequest) => request<BackupJob>('POST', '/api/jobs', data),
  updateJob: (id: string, data: CreateJobRequest) =>
    request<void>('PUT', `/api/jobs/${id}`, data),
  toggleJob: (id: string, enabled: boolean) =>
    request<void>('PATCH', `/api/jobs/${id}/toggle`, { enabled }),
  deleteJob: (id: string) => request<void>('DELETE', `/api/jobs/${id}`),
  triggerJob: (id: string) => request<BackupRun>('POST', `/api/jobs/${id}/run`),

  cancelRun: (id: string) => request<void>('POST', `/api/runs/${id}/cancel`),

  // The server rejects an unknown `status` or a malformed `job_id` with a 400,
  // so empty-string filters are dropped rather than forwarded.
  listRuns: (params?: RunsQuery, signal?: AbortSignal) => {
    const qs = new URLSearchParams()
    for (const [k, v] of Object.entries(params ?? {})) {
      if (v === undefined || v === null || v === '') continue
      qs.set(k, String(v))
    }
    const suffix = qs.toString()
    return request<RunsPage>('GET', `/api/runs${suffix ? '?' + suffix : ''}`, undefined, signal)
  },

  getRunLogs: (id: string, signal?: AbortSignal) =>
    request<RunLogs>('GET', `/api/runs/${id}/logs`, undefined, signal),

  getRunStats: (days: number, signal?: AbortSignal) =>
    request<RunStats>('GET', `/api/runs/stats?days=${days}`, undefined, signal),

  getHealth: (signal?: AbortSignal) =>
    request<HealthSummary>('GET', '/api/health', undefined, signal),

  /**
   * Lists a run's archive without restoring it.
   *
   * The agent downloads and decrypts the whole file to answer, so this is a
   * minutes-scale request on a large backup over a slow link — the server waits
   * up to 5 minutes for the agent before giving up. It needs the agent online
   * (409) and can fail in the agent (502).
   */
  listRunArchive: (id: string, signal?: AbortSignal) =>
    request<ArchiveListing>('GET', `/api/runs/${id}/archive`, undefined, signal),

  /**
   * Dispatches a restore to the agent and returns as soon as it is handed off
   * (202). The work itself reports on the run's own log stream.
   */
  startRestore: (id: string, body: RestoreRequest) =>
    request<RestoreAccepted>('POST', `/api/runs/${id}/restore`, body),

  getNotificationSettings: (signal?: AbortSignal) =>
    request<NotificationSettings | { configured: false }>('GET', '/api/settings/notifications', undefined, signal),
  updateNotificationSettings: (data: { type: string; config: EmailNotificationConfig }) =>
    request<void>('PUT', '/api/settings/notifications', data),
}
