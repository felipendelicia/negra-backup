import { getToken } from './auth'
import type {
  Agent, BackupJob, BackupRun, StorageDestination,
  NotificationSettings, CreateJobRequest, CreateStorageRequest,
  EmailNotificationConfig,
} from './types'

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
    if (res.status === 401) {
      localStorage.removeItem('nat_backup_token')
      window.location.href = '/login'
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

  listStorage: (signal?: AbortSignal) => request<StorageDestination[]>('GET', '/api/storage-destinations', undefined, signal),
  createStorage: (data: CreateStorageRequest) =>
    request<StorageDestination>('POST', '/api/storage-destinations', data),
  updateStorage: (id: string, data: CreateStorageRequest) =>
    request<void>('PUT', `/api/storage-destinations/${id}`, data),
  deleteStorage: (id: string) => request<void>('DELETE', `/api/storage-destinations/${id}`),

  listJobs: (signal?: AbortSignal) => request<BackupJob[]>('GET', '/api/jobs', undefined, signal),
  createJob: (data: CreateJobRequest) => request<BackupJob>('POST', '/api/jobs', data),
  updateJob: (id: string, data: CreateJobRequest) =>
    request<void>('PUT', `/api/jobs/${id}`, data),
  toggleJob: (id: string, enabled: boolean) =>
    request<void>('PATCH', `/api/jobs/${id}/toggle`, { enabled }),
  deleteJob: (id: string) => request<void>('DELETE', `/api/jobs/${id}`),
  triggerJob: (id: string) => request<BackupRun>('POST', `/api/jobs/${id}/run`),

  cancelRun: (id: string) => request<void>('POST', `/api/runs/${id}/cancel`),

  listRuns: (params?: { job_id?: string; status?: string }, signal?: AbortSignal) => {
    const filtered = params ? Object.fromEntries(
      Object.entries(params).filter(([, v]) => v !== undefined)
    ) : {}
    const qs = Object.keys(filtered).length ? new URLSearchParams(filtered as Record<string, string>).toString() : ''
    return request<BackupRun[]>('GET', `/api/runs${qs ? '?' + qs : ''}`, undefined, signal)
  },

  getNotificationSettings: (signal?: AbortSignal) =>
    request<NotificationSettings | { configured: false }>('GET', '/api/settings/notifications', undefined, signal),
  updateNotificationSettings: (data: { type: string; config: EmailNotificationConfig }) =>
    request<void>('PUT', '/api/settings/notifications', data),
}
