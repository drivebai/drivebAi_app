// Lightweight formatters used across pages. Kept in one file so date/currency
// formats stay consistent (prototype uses "Dec 8, 2025" style dates).

export function fmtDate(iso?: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })
}

export function fmtDateTime(iso?: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString(undefined, {
    month: 'short', day: 'numeric', year: 'numeric',
    hour: '2-digit', minute: '2-digit',
  })
}

export function fmtMoney(amount?: number | null, currency = 'USD'): string {
  if (amount == null) return '—'
  try {
    return new Intl.NumberFormat(undefined, { style: 'currency', currency }).format(amount)
  } catch {
    return `${currency} ${amount.toFixed(2)}`
  }
}

/** Translate raw photo paths (e.g. "/uploads/cars/...") into URLs that work
 *  through the Vite dev proxy AND against the production backend domain.
 *  Absolute URLs are passed through unchanged. Signed URLs (?sig=&exp=) are
 *  preserved verbatim — the trailing query string lives on `path` and we
 *  just prepend the host. */
export function imgUrl(path?: string | null): string | undefined {
  if (!path) return undefined
  if (/^https?:\/\//i.test(path)) return path
  const apiBase = (import.meta.env.VITE_API_BASE_URL || '').trim().replace(/\/+$/, '')
  const rel = path.startsWith('/') ? path : '/' + path
  return apiBase ? `${apiBase}${rel}` : rel
}
