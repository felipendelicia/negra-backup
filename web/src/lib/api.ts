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

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
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
  })

  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new APIError(res.status, (err as { error?: string }).error || res.statusText)
  }

  if (res.status === 204) return undefined as T
  return res.json() as Promise<T>
}

export const api = {
  login: (username: string, password: string) =>
    request<{ token: string }>('POST', '/api/auth/login', { username, password }),

  listAgents: () => request<Agent[]>('GET', '/api/agents'),
  deleteAgent: (id: string) => request<void>('DELETE', `/api/agents/${id}`),

  listStorage: () => request<StorageDestination[]>('GET', '/api/storage-destinations'),
  createStorage: (data: CreateStorageRequest) =>
    request<StorageDestination>('POST', '/api/storage-destinations', data),
  updateStorage: (id: string, data: CreateStorageRequest) =>
    request<void>('PUT', `/api/storage-destinations/${id}`, data),
  deleteStorage: (id: string) => request<void>('DELETE', `/api/storage-destinations/${id}`),

  listJobs: () => request<BackupJob[]>('GET', '/api/jobs'),
  createJob: (data: CreateJobRequest) => request<BackupJob>('POST', '/api/jobs', data),
  updateJob: (id: string, data: CreateJobRequest) =>
    request<void>('PUT', `/api/jobs/${id}`, data),
  deleteJob: (id: string) => request<void>('DELETE', `/api/jobs/${id}`),
  triggerJob: (id: string) => request<BackupRun>('POST', `/api/jobs/${id}/run`),

  listRuns: (params?: { job_id?: string; status?: string }) => {
    const qs = params ? new URLSearchParams(params as Record<string, string>).toString() : ''
    return request<BackupRun[]>('GET', `/api/runs${qs ? '?' + qs : ''}`)
  },

  getNotificationSettings: () =>
    request<NotificationSettings | { configured: false }>('GET', '/api/settings/notifications'),
  updateNotificationSettings: (data: { type: string; config: EmailNotificationConfig }) =>
    request<void>('PUT', '/api/settings/notifications', data),
}
