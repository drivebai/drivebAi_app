// Thin fetch wrapper used by every page in the admin app.
// - Adds the Bearer JWT from localStorage automatically.
// - Centralizes error normalization (so callers can `try { await api.get(...) }`).
// - On 401, clears the token and redirects to /login (handled by the auth store).
//
// Browsers MUST never talk to Postgres directly — only this client does network IO.

import router from '../router'
import { useToastStore } from '../stores/toast'

const TOKEN_KEY = 'drivebai.admin.token'

// API base resolution.
//   - In `vite dev` the Vite proxy rewrites /api and /uploads onto the backend,
//     so an empty base ("") sends requests to the same origin and the proxy
//     forwards them. Don't change this — it's what makes hot-reload work.
//   - In production builds (vite build), VITE_API_BASE_URL is baked into the
//     bundle and must point at the deployed backend (e.g.
//     https://drivebai-api-team.fly.dev). The admin Fly app does NOT run a
//     backend, so a missing/empty VITE_API_BASE_URL in prod is a deploy bug —
//     every request would 404 against the static-only nginx.
// We surface that bug at boot with a clear console error so the operator sees
// it immediately instead of via a confusing pile of failed XHRs.
const RAW_API_BASE = (import.meta.env.VITE_API_BASE_URL || '').trim()
const API_BASE = RAW_API_BASE.replace(/\/+$/, '') // strip trailing slash

if (import.meta.env.PROD && !API_BASE) {
  // eslint-disable-next-line no-console
  console.error(
    '[admin] VITE_API_BASE_URL is empty in a production build. ' +
    'Set it at build time or in admin/.env.production before running `vite build`.'
  )
}

/** Joins the configured base URL onto a leading-slash path. Same-origin in dev. */
function resolveURL(path: string): string {
  if (/^https?:\/\//i.test(path)) return path
  if (!API_BASE) return path // dev / proxy fallback
  return `${API_BASE}${path.startsWith('/') ? path : '/' + path}`
}

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY)
}
export function setToken(token: string | null) {
  if (token) localStorage.setItem(TOKEN_KEY, token)
  else localStorage.removeItem(TOKEN_KEY)
}

export class ApiError extends Error {
  status: number
  code?: string
  /**
   * Structured payload from the backend's APIError.Details, e.g.
   * `{ missing: ['registration','insurance'] }` on a 422
   * MISSING_REQUIRED_DOCUMENTS approve refusal. Optional — most errors
   * carry only code + message.
   */
  details?: Record<string, unknown>
  constructor(message: string, status: number, code?: string, details?: Record<string, unknown>) {
    super(message)
    this.status = status
    this.code = code
    this.details = details
  }
}

type Method = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'

async function request<T>(method: Method, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  const token = getToken()
  if (token) headers['Authorization'] = `Bearer ${token}`

  const res = await fetch(resolveURL(path), {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })

  if (res.status === 204) return undefined as T

  let data: any = null
  const ct = res.headers.get('content-type') || ''
  if (ct.includes('application/json')) {
    try { data = await res.json() } catch { /* ignore */ }
  }

  if (!res.ok) {
    const apiErr = data?.error
    const msg = apiErr?.message || res.statusText || 'Request failed'
    const code = apiErr?.code
    const details =
      apiErr?.details && typeof apiErr.details === 'object' ? apiErr.details : undefined

    if (res.status === 401) {
      setToken(null)
      // Avoid infinite loops on the login page itself.
      if (router.currentRoute.value.name !== 'login') {
        router.replace({ name: 'login', query: { redirect: router.currentRoute.value.fullPath } })
      }
    } else if (res.status === 403) {
      // Surface forbidden errors so admins know they don't have access.
      useToastStore().error(msg)
    }

    throw new ApiError(msg, res.status, code, details)
  }

  return data as T
}

export const api = {
  get:  <T>(path: string)              => request<T>('GET',    path),
  post: <T>(path: string, body?: any)  => request<T>('POST',   path, body),
  put:  <T>(path: string, body?: any)  => request<T>('PUT',    path, body),
  patch:<T>(path: string, body?: any)  => request<T>('PATCH',  path, body),
  del:  <T>(path: string)              => request<T>('DELETE', path),
}

// Build a query string, omitting empty values.
export function qs(params: Record<string, string | number | undefined | null>): string {
  const u = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '') continue
    u.set(k, String(v))
  }
  const s = u.toString()
  return s ? `?${s}` : ''
}
