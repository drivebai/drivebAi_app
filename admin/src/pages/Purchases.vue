<script setup lang="ts">
/**
 * Buy the Car — admin panel.
 *
 * Lists every purchase_request the platform has ever seen and lets an
 * admin drill into a single record to review the Bill of Sale, the
 * captured/authorized payment, and any inspection rejection filed by the
 * buyer (with the evidence gallery).
 *
 * Backend endpoints (per the Buy the Car spec §4.4):
 *   GET  /api/v1/admin/purchase-requests         — paginated list + filters
 *   GET  /api/v1/admin/purchase-requests/{id}    — full record + BoS + rejection
 *   POST /api/v1/admin/purchase-rejections/{id}/resolve
 *   POST /api/v1/admin/purchase-requests/{id}/retry-refund
 *
 * The page degrades gracefully when the backend hasn't shipped those
 * endpoints yet: the list call returns 404/500, we catch it, and show an
 * empty state. The types intentionally treat every joined field as
 * optional so partial backend rollouts don't blow up the UI.
 */

import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import PageHeader from '../components/PageHeader.vue'
import DataTable from '../components/DataTable.vue'
import StatusBadge from '../components/StatusBadge.vue'
import Drawer from '../components/Drawer.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import { adminApi } from '../api/admin'
import type {
  PurchaseRequest,
  PurchaseRequestDetail,
  PurchaseRequestStatus,
  PurchaseRejectionEvidence,
  PurchaseInspectionChecklist,
  PurchaseAdminCarDocument,
} from '../api/types'
import { useToastStore } from '../stores/toast'
import { fmtDate, fmtDateTime, fmtMoney, imgUrl } from '../utils/format'

const toast = useToastStore()
const router = useRouter()

// ── State ─────────────────────────────────────────────────────────────────────

const rows = ref<PurchaseRequest[]>([])
const total = ref(0)
const page = ref(1)
const limit = ref(50)
const loading = ref(false)
const query = ref('')
const statusFilter = ref<'' | 'active' | 'terminal' | 'rejection' | 'refund_issue'>('')

let searchTimer: number | undefined
watch(query, () => {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => { page.value = 1; load() }, 250)
})
watch(statusFilter, () => { page.value = 1; load() })

async function load() {
  loading.value = true
  try {
    // Map the local pill filter onto a backend-friendly status hint. The
    // backend can freely ignore it; we do a client-side pass below as a
    // safety net.
    const res = await adminApi.listPurchaseRequests({
      query: query.value,
      status: statusFilter.value || undefined,
      page: page.value,
      limit: limit.value,
    })
    rows.value = maybeClientFilter(res.items || [])
    total.value = res.total ?? rows.value.length
  } catch (e: any) {
    // Backend may not have shipped the endpoint yet — treat as empty rather
    // than shouting at the admin. Only surface real 4xx/5xx as toasts once
    // the endpoint exists.
    rows.value = []
    total.value = 0
    if (e?.status && e.status !== 404) {
      toast.error(e?.message || 'Failed to load purchase requests')
    }
  } finally {
    loading.value = false
  }
}
load()

// Client-side backstop for the filter pills. The backend accepts the same
// hint but if it decides to ignore it we still get a sensible view.
const ACTIVE_STATUSES: PurchaseRequestStatus[] = [
  'requested', 'accepted',
  'bos_pending_seller', 'bos_pending_buyer', 'bos_signed',
  'payment_authorized', 'handover_scheduled', 'awaiting_inspection',
  'inspection_accepted', 'inspection_rejected',
]
const TERMINAL_STATUSES: PurchaseRequestStatus[] = [
  'completed', 'declined', 'cancelled', 'expired', 'expired_auth',
  'rejected_refunded', 'rejected_upheld',
]
function maybeClientFilter(list: PurchaseRequest[]): PurchaseRequest[] {
  if (!statusFilter.value) return list
  if (statusFilter.value === 'active') {
    return list.filter(r => ACTIVE_STATUSES.includes(r.status as PurchaseRequestStatus))
  }
  if (statusFilter.value === 'terminal') {
    return list.filter(r => TERMINAL_STATUSES.includes(r.status as PurchaseRequestStatus))
  }
  if (statusFilter.value === 'rejection') {
    return list.filter(r => r.status === 'inspection_rejected')
  }
  if (statusFilter.value === 'refund_issue') {
    return list.filter(r => refundNeedsAttention(r))
  }
  return list
}

// ── Detail drawer ─────────────────────────────────────────────────────────────

const detail = ref<PurchaseRequestDetail | null>(null)
const detailLoading = ref(false)
async function openDetails(row: PurchaseRequest) {
  detailLoading.value = true
  detail.value = null
  try {
    detail.value = await adminApi.getPurchaseRequest(row.id)
  } catch (e: any) {
    // If the detail endpoint isn't up yet, fall back to whatever we have
    // in the row so the admin can still see the summary + IDs.
    if (e?.status === 404) {
      detail.value = row as PurchaseRequestDetail
    } else {
      toast.error(e?.message || 'Failed to load purchase request')
    }
  } finally {
    detailLoading.value = false
  }
}
function closeDetail() { detail.value = null }

// ── Rejection resolution ──────────────────────────────────────────────────────

const resolving = ref(false)
const resolveNote = ref('')
const pendingResolution = ref<null | 'accept' | 'uphold'>(null)

async function resolveRejection(resolution: 'accept' | 'uphold') {
  if (!detail.value?.rejection || resolving.value) return
  const rejId = detail.value.rejection.id
  resolving.value = true
  try {
    await adminApi.resolvePurchaseRejection(rejId, {
      resolution,
      note: resolveNote.value.trim() || undefined,
    })
    toast.success(
      resolution === 'accept'
        ? 'Rejection accepted — hold released'
        : 'Sale upheld — payment will be captured'
    )
    pendingResolution.value = null
    resolveNote.value = ''
    // Refresh the detail + list row so the new state is reflected.
    if (detail.value) {
      try {
        detail.value = await adminApi.getPurchaseRequest(detail.value.id)
      } catch { /* keep stale */ }
    }
    load()
  } catch (e: any) {
    toast.error(e?.message || 'Failed to resolve rejection')
  } finally {
    resolving.value = false
  }
}

async function retryRefund() {
  if (!detail.value) return
  try {
    await adminApi.retryPurchaseRefund(detail.value.id)
    toast.success('Refund retry queued')
    detail.value = await adminApi.getPurchaseRequest(detail.value.id)
    load()
  } catch (e: any) {
    toast.error(e?.message || 'Failed to retry refund')
  }
}

// ── Bill of Sale finalize (admin retry) ────────────────────────────────────────

const finalizing = ref(false)

/**
 * The finalized PDF is generated once both parties have signed. The retry
 * endpoint (Design A §3d) only regenerates when the BoS is signed and the
 * column is still NULL, so we only surface the "Generate" affordance in that
 * window — reusing the same signed roll-up the BoS badge uses.
 */
function canGenerateBos(r: PurchaseRequestDetail): boolean {
  return bosStatus(r).label === 'Signed'
}

async function retryFinalizeBos() {
  if (!detail.value || finalizing.value) return
  finalizing.value = true
  try {
    await adminApi.finalizeBillOfSale(detail.value.id)
    toast.success('Bill of Sale PDF generated')
    // Re-fetch so the freshly-signed finalized_pdf_url replaces the empty state.
    detail.value = await adminApi.getPurchaseRequest(detail.value.id)
  } catch (e: any) {
    toast.error(e?.message || 'Failed to generate Bill of Sale PDF')
  } finally {
    finalizing.value = false
  }
}

// ── Status / tone helpers ─────────────────────────────────────────────────────

/**
 * The purchase state machine has ~16 statuses. We collapse them down to five
 * tones so the table stays readable.
 */
function statusTone(s: string): 'success' | 'warning' | 'danger' | 'info' | 'neutral' {
  if (s === 'completed' || s === 'inspection_accepted' || s === 'rejected_upheld') return 'success'
  if (s === 'declined' || s === 'cancelled' || s === 'expired' || s === 'expired_auth') return 'neutral'
  if (s === 'inspection_rejected') return 'danger'
  if (s === 'rejected_refunded') return 'info'
  return 'warning' // any in-flight state
}

function statusLabel(s: string): string {
  switch (s) {
    case 'requested': return 'Requested'
    case 'accepted': return 'Accepted'
    case 'declined': return 'Declined'
    case 'cancelled': return 'Cancelled'
    case 'bos_pending_seller': return 'BoS — seller to sign'
    case 'bos_pending_buyer': return 'BoS — buyer to sign'
    case 'bos_signed': return 'BoS signed'
    case 'payment_authorized': return 'Payment authorized'
    case 'handover_scheduled': return 'Handover scheduled'
    case 'awaiting_inspection': return 'Awaiting inspection'
    case 'inspection_accepted': return 'Inspection accepted'
    case 'completed': return 'Completed'
    case 'inspection_rejected': return 'Rejected — under review'
    case 'rejected_refunded': return 'Rejected — hold released'
    case 'rejected_upheld': return 'Sale upheld'
    case 'expired': return 'Expired'
    case 'expired_auth': return 'Auth expired'
    default: return s
  }
}

function paymentTone(row: PurchaseRequest): 'success' | 'warning' | 'danger' | 'info' | 'neutral' {
  const p = row.payment_status
  if (!p) return 'neutral'
  if (p === 'succeeded' || p === 'captured') return 'success'
  if (p === 'authorized' || p === 'requires_capture') return 'info'
  if (p === 'processing' || p === 'requires_action' || p === 'requires_confirmation' || p === 'requires_payment_method') return 'warning'
  if (p === 'failed' || p === 'canceled') return 'danger'
  return 'neutral'
}

function paymentLabel(row: PurchaseRequest): string {
  return row.payment_status || '—'
}

/** BoS status roll-up derived from the BoS signature timestamps (attached to
 *  both list + detail responses) with the purchase status as a fallback. */
function bosStatus(r: PurchaseRequest | PurchaseRequestDetail): { label: string, tone: 'success' | 'warning' | 'neutral' } {
  const bos = r.bill_of_sale
  if (bos?.seller_signed_at && bos?.buyer_signed_at) return { label: 'Signed', tone: 'success' }
  if (bos?.seller_signed_at) return { label: 'Buyer to sign', tone: 'warning' }
  if (bos?.buyer_signed_at) return { label: 'Seller to sign', tone: 'warning' }
  const s = r.status
  if (s === 'accepted' || s === 'bos_pending_seller' || s === 'bos_pending_buyer') {
    return { label: 'Pending', tone: 'warning' }
  }
  if (
    s === 'bos_signed' || s === 'payment_authorized' || s === 'handover_scheduled' ||
    s === 'awaiting_inspection' || s === 'inspection_accepted' || s === 'completed' ||
    s === 'inspection_rejected' || s === 'rejected_refunded' || s === 'rejected_upheld'
  ) {
    return { label: 'Signed', tone: 'success' }
  }
  return { label: 'Not started', tone: 'neutral' }
}

function rejectionTone(row: PurchaseRequest | PurchaseRequestDetail): 'danger' | 'warning' | 'success' | 'neutral' {
  if (row.status === 'inspection_rejected') return 'danger'
  if (row.status === 'rejected_refunded' || row.status === 'rejected_upheld') return 'warning'
  return 'neutral'
}

function rejectionLabel(row: PurchaseRequest | PurchaseRequestDetail): string {
  if (row.status === 'inspection_rejected') return 'Under review'
  if (row.status === 'rejected_refunded') return 'Hold released'
  if (row.status === 'rejected_upheld') return 'Upheld'
  return '—'
}

function hasRejection(row: PurchaseRequest | PurchaseRequestDetail): boolean {
  return (
    row.status === 'inspection_rejected' ||
    row.status === 'rejected_refunded' ||
    row.status === 'rejected_upheld' ||
    !!(row as PurchaseRequestDetail).rejection
  )
}

/**
 * A purchase is "refund-attention" when Stripe is stuck: refund failed,
 * pending_manual, or (in the detail view) a refund failure reason is on record.
 * The list projection only carries refund_status; refund_failure_reason lives
 * on the admin_detail block returned by the detail endpoint.
 */
function refundNeedsAttention(row: PurchaseRequest | PurchaseRequestDetail): boolean {
  const rf = row.refund_status
  if (rf === 'failed' || rf === 'pending_manual') return true
  if ((row as PurchaseRequestDetail).admin_detail?.refund_failure_reason) return true
  return false
}

function refundBadge(row: PurchaseRequest): { label: string, tone: 'danger' | 'warning' | 'success' | 'neutral' } | null {
  const rf = row.refund_status
  if (rf === 'failed') return { label: 'Refund failed', tone: 'danger' }
  if (rf === 'pending_manual') return { label: 'Manual refund', tone: 'danger' }
  if (rf === 'pending') return { label: 'Refund pending', tone: 'warning' }
  if (rf === 'succeeded') return { label: 'Refunded', tone: 'success' }
  if (rf === 'not_applicable' || !rf) return null
  return { label: String(rf), tone: 'neutral' }
}

// ── Money helpers ─────────────────────────────────────────────────────────────

function fmtCents(cents?: number | null, currency = 'USD'): string {
  if (cents == null) return '—'
  return fmtMoney(cents / 100, currency)
}

function fmtOffer(row: PurchaseRequest): string {
  return fmtCents(row.offer_amount_cents, row.currency)
}

function buyerLabel(row: PurchaseRequest): string {
  return row.buyer_name || row.buyer_id
}
function sellerLabel(row: PurchaseRequest): string {
  return row.seller_name || row.seller_id
}
function carLabel(row: PurchaseRequest): string {
  const t = row.car_title || [row.vehicle_make, row.vehicle_model].filter(Boolean).join(' ')
  if (!t) return row.car_id
  return row.vehicle_year ? `${t} · ${row.vehicle_year}` : t
}

// ── Inspection checklist ────────────────────────────────────────────────────
function checklistItems(c: PurchaseInspectionChecklist): { label: string; ok: boolean }[] {
  return [
    { label: 'VIN matches', ok: c.vin_matches },
    { label: 'Odometer reviewed', ok: c.odometer_reviewed },
    { label: 'Exterior OK', ok: c.exterior_ok },
    { label: 'Interior OK', ok: c.interior_ok },
    { label: 'Mechanical / test drive OK', ok: c.mechanical_test_drive_ok },
    { label: 'Title reviewed', ok: c.title_reviewed },
    { label: 'Keys handed over', ok: c.keys_handed_over },
    { label: 'Buyer understands acceptance completes payment', ok: c.buyer_understands_acceptance_completes_payment },
  ]
}

// ── Car documents (signed) ──────────────────────────────────────────────────
const CAR_DOC_TYPES = ['title', 'registration', 'inspection', 'insurance']
const CAR_DOC_LABELS: Record<string, string> = {
  title: 'Title', registration: 'Registration', inspection: 'Inspection', insurance: 'Insurance',
}
function carDocLabel(t: string): string { return CAR_DOC_LABELS[t] || t }
function carDocGroups(docs: PurchaseAdminCarDocument[]): { type: string; label: string; docs: PurchaseAdminCarDocument[] }[] {
  const present = docs ?? []
  const extra = [...new Set(present.map((d) => d.document_type))].filter((t) => !CAR_DOC_TYPES.includes(t))
  return [...CAR_DOC_TYPES, ...extra].map((type) => ({
    type,
    label: carDocLabel(type),
    docs: present.filter((d) => d.document_type === type),
  }))
}
/** Detect image documents by extension (signed URLs carry a ?sig=&exp= tail). */
function isImageUrl(url?: string | null, name?: string | null): boolean {
  const s = (name || url || '').split('?')[0].toLowerCase()
  return /\.(png|jpe?g|gif|webp|heic|heif|bmp)$/.test(s)
}

// ── Status timeline ─────────────────────────────────────────────────────────
// Collapse the request's timestamp columns into a chronological event list.
function timeline(r: PurchaseRequestDetail): { label: string; at: string }[] {
  const bos = r.bill_of_sale
  const pairs: { label: string; at?: string | null }[] = [
    { label: 'Requested', at: r.created_at },
    { label: 'Seller signed Bill of Sale', at: bos?.seller_signed_at },
    { label: 'Buyer signed Bill of Sale', at: bos?.buyer_signed_at },
    { label: 'Bill of Sale finalized', at: bos?.finalized_at },
    { label: 'Handover scheduled', at: r.handover_scheduled_at },
    { label: 'Keys handed over', at: r.keys_handed_over_at },
    { label: 'Inspection accepted', at: r.inspection_accepted_at },
    { label: 'Completed', at: r.completed_at },
    { label: 'Refunded', at: r.refunded_at },
  ]
  return pairs
    .filter((p): p is { label: string; at: string } => !!p.at)
    .sort((a, b) => new Date(a.at).getTime() - new Date(b.at).getTime())
}

// ── Chat link ───────────────────────────────────────────────────────────────
// Jump to the Chats console pre-seeded with the chat id in the search box.
function openChat(chatId?: string | null) {
  if (!chatId) return
  router.push({ name: 'chats', query: { q: chatId } })
}

// ── Rejection reason mapping ──────────────────────────────────────────────────

const REJECTION_REASON_LABELS: Record<string, string> = {
  undisclosed_damage: 'Undisclosed damage',
  mechanical_issues: 'Mechanical issues',
  title_or_paperwork: 'Title or paperwork',
  vin_mismatch: 'VIN mismatch',
  not_as_described: 'Not as described',
  no_show: 'Seller no-show',
  other: 'Other',
}
function reasonLabel(r?: string | null): string {
  if (!r) return '—'
  return REJECTION_REASON_LABELS[r] || r
}

const TITLE_CONDITION_LABELS: Record<string, string> = {
  clean: 'Clean',
  lien_recorded: 'Lien recorded',
  salvage: 'Salvage',
  rebuilt: 'Rebuilt',
  lemon_buyback: 'Lemon buyback',
  flood: 'Flood',
  manufacturer_buyback: 'Manufacturer buyback',
  other: 'Other',
}
function titleConditionLabel(bos: { title_condition?: string | null; title_condition_other?: string | null }): string {
  const tc = bos.title_condition
  if (!tc) return '—'
  if (tc === 'other') {
    return bos.title_condition_other ? `Other — ${bos.title_condition_other}` : 'Other'
  }
  return TITLE_CONDITION_LABELS[tc] || tc
}

function isImage(mime: string) { return mime.startsWith('image/') }
function isVideo(mime: string) { return mime.startsWith('video/') }

// Evidence viewer modal — click a thumbnail to open the full-res version.
const evidencePreview = ref<PurchaseRejectionEvidence | null>(null)

function pageCount(t: number, l: number) {
  return Math.max(1, Math.ceil(t / l))
}

// Detail helper — the drawer needs a stable object even mid-load.
const detailRow = computed<PurchaseRequestDetail | null>(() => detail.value)
</script>

<template>
  <PageHeader title="Purchases" />

  <div class="filters">
    <input
      v-model="query"
      placeholder="Search by car, buyer email, seller email, or Stripe intent…"
      class="search"
    />
    <div class="pills">
      <button :class="{ active: statusFilter === '' }"             @click="statusFilter = ''">All</button>
      <button :class="{ active: statusFilter === 'active' }"       @click="statusFilter = 'active'">Active</button>
      <button :class="{ active: statusFilter === 'terminal' }"     @click="statusFilter = 'terminal'">Terminal</button>
      <button :class="{ active: statusFilter === 'rejection' }"    @click="statusFilter = 'rejection'">Rejections</button>
      <button :class="{ active: statusFilter === 'refund_issue' }" @click="statusFilter = 'refund_issue'">Refund issue</button>
    </div>
  </div>

  <!-- Desktop / tablet: full data table. Hidden on phones in favour of cards. -->
  <div class="desktop-only">
    <DataTable
      :rows :loading :total :page :limit
      :on-row-click="openDetails"
      @page="(p: number) => { page = p; load() }"
    >
      <template #header>
        <th>Created</th>
        <th>Car</th>
        <th>Buyer</th>
        <th>Seller</th>
        <th>Offer</th>
        <th>Status</th>
        <th>Payment</th>
        <th>BoS</th>
        <th>Rejection</th>
        <th>Refund</th>
      </template>
      <template #row="{ row }">
        <td>{{ fmtDate(row.created_at) }}</td>
        <td class="car-cell">
          <div class="thumb thumb-placeholder" />
          <span>{{ carLabel(row) }}</span>
        </td>
        <td>{{ buyerLabel(row) }}</td>
        <td>{{ sellerLabel(row) }}</td>
        <td>{{ fmtOffer(row) }}</td>
        <td><StatusBadge :label="statusLabel(row.status)" :tone="statusTone(row.status)" /></td>
        <td><StatusBadge :label="paymentLabel(row)" :tone="paymentTone(row)" /></td>
        <td><StatusBadge :label="bosStatus(row).label" :tone="bosStatus(row).tone" /></td>
        <td>
          <StatusBadge
            v-if="hasRejection(row)"
            :label="rejectionLabel(row)"
            :tone="rejectionTone(row)"
          />
          <span v-else class="muted">—</span>
        </td>
        <td>
          <StatusBadge
            v-if="refundBadge(row)"
            :label="refundBadge(row)!.label"
            :tone="refundBadge(row)!.tone"
          />
          <span v-else class="muted">—</span>
        </td>
      </template>
      <template #empty>No purchase requests yet.</template>
    </DataTable>
  </div>

  <!-- Mobile: card list mirroring the Vehicles page pattern. -->
  <div class="mobile-only card-list">
    <div v-if="loading" class="card-state">Loading…</div>
    <div v-else-if="!rows.length" class="card-state">No purchase requests yet.</div>
    <article v-for="row in rows" :key="row.id" class="purchase-card" @click="openDetails(row)">
      <div class="card-top">
        <div class="card-thumb card-thumb-placeholder" />
        <div class="card-meta">
          <div class="card-title">{{ carLabel(row) }}</div>
          <div class="card-sub">{{ fmtDate(row.created_at) }} · {{ fmtOffer(row) }}</div>
          <div class="card-parties">
            <div><span class="muted">Buyer</span> {{ buyerLabel(row) }}</div>
            <div><span class="muted">Seller</span> {{ sellerLabel(row) }}</div>
          </div>
          <div class="card-badges">
            <StatusBadge :label="statusLabel(row.status)" :tone="statusTone(row.status)" />
            <StatusBadge :label="paymentLabel(row)" :tone="paymentTone(row)" />
            <StatusBadge :label="`BoS: ${bosStatus(row).label}`" :tone="bosStatus(row).tone" />
            <StatusBadge
              v-if="hasRejection(row)"
              :label="`Rejection: ${rejectionLabel(row)}`"
              :tone="rejectionTone(row)"
            />
            <StatusBadge
              v-if="refundBadge(row)"
              :label="refundBadge(row)!.label"
              :tone="refundBadge(row)!.tone"
            />
          </div>
        </div>
      </div>
    </article>

    <footer v-if="total > 0" class="mobile-pager">
      <span class="muted">
        {{ ((page - 1) * limit) + 1 }}–{{ Math.min(page * limit, total) }} of {{ total }}
      </span>
      <div class="pager">
        <button :disabled="page <= 1" @click="page = page - 1; load()">‹ Prev</button>
        <span class="page-num">{{ page }} / {{ pageCount(total, limit) }}</span>
        <button :disabled="page >= pageCount(total, limit)" @click="page = page + 1; load()">Next ›</button>
      </div>
    </footer>
  </div>

  <!-- ── Detail drawer ── -->
  <Drawer
    v-if="detailRow || detailLoading"
    :title="detailRow ? `Purchase — ${carLabel(detailRow)}` : 'Purchase'"
    @close="closeDetail"
  >
    <div v-if="detailLoading" class="loading">Loading…</div>
    <template v-else-if="detailRow">
      <!-- Summary -->
      <section class="detail-section">
        <h4 class="section-title">Summary</h4>
        <dl class="kv">
          <dt>Status</dt>
          <dd><StatusBadge :label="statusLabel(detailRow.status)" :tone="statusTone(detailRow.status)" /></dd>
          <dt>Car</dt><dd>{{ carLabel(detailRow) }}</dd>
          <dt>Offer</dt><dd>{{ fmtOffer(detailRow) }}</dd>
          <dt v-if="detailRow.buyer_message">Buyer message</dt>
          <dd v-if="detailRow.buyer_message" class="wrap">{{ detailRow.buyer_message }}</dd>
          <dt>Created</dt><dd>{{ fmtDateTime(detailRow.created_at) }}</dd>
          <dt>Updated</dt><dd>{{ fmtDateTime(detailRow.updated_at) }}</dd>
          <dt v-if="detailRow.expires_at">Offer expires</dt>
          <dd v-if="detailRow.expires_at">{{ fmtDateTime(detailRow.expires_at) }}</dd>
          <dt v-if="detailRow.auth_expires_at">Auth expires</dt>
          <dd v-if="detailRow.auth_expires_at">{{ fmtDateTime(detailRow.auth_expires_at) }}</dd>
          <dt v-if="detailRow.handover_scheduled_at">Handover</dt>
          <dd v-if="detailRow.handover_scheduled_at">
            {{ fmtDateTime(detailRow.handover_scheduled_at) }}
            <span v-if="detailRow.handover_location">— {{ detailRow.handover_location }}</span>
          </dd>
          <dt v-if="detailRow.inspection_deadline_at">Inspection deadline</dt>
          <dd v-if="detailRow.inspection_deadline_at">{{ fmtDateTime(detailRow.inspection_deadline_at) }}</dd>
          <dt v-if="detailRow.admin_detail?.cancellation_reason">Cancellation reason</dt>
          <dd v-if="detailRow.admin_detail?.cancellation_reason" class="wrap">{{ detailRow.admin_detail.cancellation_reason }}</dd>
        </dl>
        <div v-if="detailRow.chat_id" class="summary-actions">
          <button class="btn-secondary" @click="openChat(detailRow.chat_id)">Open chat</button>
        </div>
      </section>

      <!-- Status timeline -->
      <section v-if="timeline(detailRow).length" class="detail-section">
        <h4 class="section-title">Timeline</h4>
        <ol class="timeline">
          <li v-for="(ev, i) in timeline(detailRow)" :key="i" class="timeline-item">
            <span class="timeline-dot" />
            <div class="timeline-body">
              <div class="timeline-label">{{ ev.label }}</div>
              <div class="timeline-at">{{ fmtDateTime(ev.at) }}</div>
            </div>
          </li>
        </ol>
      </section>

      <!-- Car (photos + signed documents) -->
      <section v-if="detailRow.admin_detail" class="detail-section">
        <h4 class="section-title">Car</h4>
        <dl class="kv">
          <dt>Vehicle</dt>
          <dd>
            {{ detailRow.admin_detail.car_year || '—' }}
            {{ detailRow.admin_detail.car_make }} {{ detailRow.admin_detail.car_model }}
          </dd>
          <dt>VIN</dt><dd class="mono">{{ detailRow.admin_detail.car_vin || '—' }}</dd>
        </dl>

        <div v-if="detailRow.admin_detail.car_photos?.length" class="photo-grid">
          <a
            v-for="(url, i) in detailRow.admin_detail.car_photos"
            :key="i"
            :href="imgUrl(url)"
            target="_blank"
            rel="noopener"
            class="photo-link"
          >
            <img :src="imgUrl(url)" alt="Car photo" />
          </a>
        </div>
        <p v-else class="muted">No photos on file.</p>

        <div class="doc-subhead">Documents</div>
        <div class="doc-grid">
          <div v-for="g in carDocGroups(detailRow.admin_detail.car_documents)" :key="g.type" class="doc-card">
            <div class="doc-card-head">{{ g.label }}</div>
            <template v-if="g.docs.length">
              <div v-for="(doc, di) in g.docs" :key="di" class="doc-entry">
                <a
                  v-if="isImageUrl(doc.file_url, doc.file_name)"
                  :href="imgUrl(doc.file_url)"
                  target="_blank"
                  rel="noopener"
                  class="doc-thumb-link"
                  :title="doc.file_name || g.label"
                >
                  <img :src="imgUrl(doc.file_url)" :alt="`${g.label} document`" class="doc-thumb" />
                </a>
                <a :href="imgUrl(doc.file_url)" target="_blank" rel="noopener" class="doc-open">
                  <span v-if="!isImageUrl(doc.file_url, doc.file_name)" class="doc-icon">📄</span>
                  <span class="doc-open-label">{{ doc.file_name || 'Open / download' }}</span>
                </a>
              </div>
            </template>
            <div v-else class="doc-missing">Not uploaded</div>
          </div>
        </div>
      </section>

      <!-- Buyer & seller profiles -->
      <section v-if="detailRow.admin_detail" class="detail-section">
        <h4 class="section-title">Buyer &amp; Seller</h4>
        <div class="party-grid">
          <div class="party-card">
            <div class="party-role">Buyer</div>
            <dl class="kv party-kv">
              <dt>Name</dt><dd>{{ detailRow.buyer_name || '—' }}</dd>
              <dt>Email</dt><dd class="wrap">{{ detailRow.admin_detail.buyer_email || '—' }}</dd>
              <dt>Phone</dt><dd>{{ detailRow.admin_detail.buyer_phone || '—' }}</dd>
              <dt>Address</dt>
              <dd class="wrap">
                {{ detailRow.admin_detail.buyer_address || '—' }}
                <span v-if="detailRow.admin_detail.buyer_address" class="source-tag">from Bill of Sale</span>
              </dd>
            </dl>
            <div class="id-doc">
              <div class="id-doc-label">ID document</div>
              <a
                v-if="detailRow.admin_detail.buyer_id_document_url"
                :href="imgUrl(detailRow.admin_detail.buyer_id_document_url)"
                target="_blank"
                rel="noopener"
                class="id-doc-preview"
              >
                <img
                  v-if="isImageUrl(detailRow.admin_detail.buyer_id_document_url)"
                  :src="imgUrl(detailRow.admin_detail.buyer_id_document_url)"
                  alt="Buyer ID document"
                />
                <span v-else class="doc-open"><span class="doc-icon">📄</span><span class="doc-open-label">Open ID document</span></span>
              </a>
              <div v-else class="doc-missing">Not on file</div>
            </div>
          </div>

          <div class="party-card">
            <div class="party-role">Seller</div>
            <dl class="kv party-kv">
              <dt>Name</dt><dd>{{ detailRow.seller_name || '—' }}</dd>
              <dt>Email</dt><dd class="wrap">{{ detailRow.admin_detail.seller_email || '—' }}</dd>
              <dt>Phone</dt><dd>{{ detailRow.admin_detail.seller_phone || '—' }}</dd>
              <dt>Address</dt>
              <dd class="wrap">
                {{ detailRow.admin_detail.seller_address || '—' }}
                <span v-if="detailRow.admin_detail.seller_address" class="source-tag">from Bill of Sale</span>
              </dd>
            </dl>
            <div class="id-doc">
              <div class="id-doc-label">ID document</div>
              <a
                v-if="detailRow.admin_detail.seller_id_document_url"
                :href="imgUrl(detailRow.admin_detail.seller_id_document_url)"
                target="_blank"
                rel="noopener"
                class="id-doc-preview"
              >
                <img
                  v-if="isImageUrl(detailRow.admin_detail.seller_id_document_url)"
                  :src="imgUrl(detailRow.admin_detail.seller_id_document_url)"
                  alt="Seller ID document"
                />
                <span v-else class="doc-open"><span class="doc-icon">📄</span><span class="doc-open-label">Open ID document</span></span>
              </a>
              <div v-else class="doc-missing">Not on file</div>
            </div>
          </div>
        </div>
      </section>

      <!-- Bill of Sale -->
      <section class="detail-section">
        <h4 class="section-title">Bill of Sale</h4>
        <template v-if="detailRow.bill_of_sale">
          <dl class="kv">
            <dt>Vehicle</dt>
            <dd>
              {{ detailRow.bill_of_sale.vehicle_year || '—' }}
              {{ detailRow.bill_of_sale.vehicle_make || '' }}
              {{ detailRow.bill_of_sale.vehicle_model || '' }}
            </dd>
            <dt>VIN</dt><dd>{{ detailRow.bill_of_sale.vin || '—' }}</dd>
            <dt>Sale amount</dt>
            <dd>{{ fmtCents(detailRow.bill_of_sale.sale_amount_cents, detailRow.bill_of_sale.currency || detailRow.currency) }}</dd>
            <dt>Seller name</dt><dd>{{ detailRow.bill_of_sale.seller_name || '—' }}</dd>
            <dt>Seller address</dt><dd class="wrap">{{ detailRow.bill_of_sale.seller_address || '—' }}</dd>
            <dt>Buyer name</dt><dd>{{ detailRow.bill_of_sale.buyer_name || '—' }}</dd>
            <dt>Buyer address</dt><dd class="wrap">{{ detailRow.bill_of_sale.buyer_address || '—' }}</dd>
            <dt>Title condition</dt><dd>{{ titleConditionLabel(detailRow.bill_of_sale) }}</dd>
            <dt>Title document</dt>
            <dd>
              <a
                v-if="detailRow.bill_of_sale.title_document_url"
                :href="imgUrl(detailRow.bill_of_sale.title_document_url)"
                target="_blank"
                rel="noopener"
                class="inline-link"
              >Open title document</a>
              <span v-else class="muted">Not uploaded</span>
            </dd>
            <dt>Terms</dt><dd class="wrap">{{ detailRow.bill_of_sale.terms_conditions || '—' }}</dd>
          </dl>

          <div class="sig-row">
            <div class="sig-box">
              <div class="sig-label">Seller signature</div>
              <img
                v-if="detailRow.bill_of_sale.seller_signature_url"
                :src="imgUrl(detailRow.bill_of_sale.seller_signature_url)"
                alt="Seller signature"
                class="sig-img"
              />
              <div v-else class="sig-empty">Not signed yet</div>
              <div v-if="detailRow.bill_of_sale.seller_signed_at" class="sig-date">
                Signed {{ fmtDateTime(detailRow.bill_of_sale.seller_signed_at) }}
              </div>
            </div>
            <div class="sig-box">
              <div class="sig-label">Buyer signature</div>
              <img
                v-if="detailRow.bill_of_sale.buyer_signature_url"
                :src="imgUrl(detailRow.bill_of_sale.buyer_signature_url)"
                alt="Buyer signature"
                class="sig-img"
              />
              <div v-else class="sig-empty">Not signed yet</div>
              <div v-if="detailRow.bill_of_sale.buyer_signed_at" class="sig-date">
                Signed {{ fmtDateTime(detailRow.bill_of_sale.buyer_signed_at) }}
              </div>
            </div>
          </div>

          <!-- Finalized PDF: signed private URL (same imgUrl signing path as the
               signature images). When the column is still NULL we say so plainly
               and, once both parties have signed, offer an admin-callable
               regenerate (Design A §3d). -->
          <div class="pdf-block">
            <a
              v-if="detailRow.bill_of_sale.finalized_pdf_url"
              class="pdf-btn"
              :href="imgUrl(detailRow.bill_of_sale.finalized_pdf_url)"
              target="_blank"
              rel="noopener"
            >View finalized Bill of Sale (PDF)</a>
            <template v-else>
              <p class="muted pdf-empty">Bill of Sale PDF not generated yet.</p>
              <button
                v-if="canGenerateBos(detailRow)"
                class="btn-primary"
                :disabled="finalizing"
                @click="retryFinalizeBos"
              >{{ finalizing ? 'Generating…' : 'Generate PDF' }}</button>
            </template>
          </div>
        </template>
        <p v-else class="muted">Bill of Sale has not been opened yet.</p>
      </section>

      <!-- Payment -->
      <section class="detail-section">
        <h4 class="section-title">Payment</h4>
        <dl class="kv">
          <dt>Stripe intent</dt><dd>{{ detailRow.payment_intent_id || '—' }}</dd>
          <dt>Payment status</dt>
          <dd><StatusBadge :label="paymentLabel(detailRow)" :tone="paymentTone(detailRow)" /></dd>
          <dt>Refund status</dt>
          <dd>
            <StatusBadge
              v-if="refundBadge(detailRow)"
              :label="refundBadge(detailRow)!.label"
              :tone="refundBadge(detailRow)!.tone"
            />
            <span v-else class="muted">—</span>
          </dd>
          <template v-if="detailRow.refund_id">
            <dt>Refund ID</dt><dd>{{ detailRow.refund_id }}</dd>
          </template>
          <template v-if="detailRow.refunded_at">
            <dt>Refunded at</dt><dd>{{ fmtDateTime(detailRow.refunded_at) }}</dd>
          </template>
          <template v-if="detailRow.admin_detail?.refund_failure_reason">
            <dt>Refund error</dt>
            <dd class="danger-text wrap">{{ detailRow.admin_detail.refund_failure_reason }}</dd>
          </template>
        </dl>
        <div v-if="refundNeedsAttention(detailRow)" class="admin-actions">
          <button class="btn-danger" @click="retryRefund">Retry refund</button>
        </div>
      </section>

      <!-- Inspection checklist -->
      <section
        v-if="detailRow.admin_detail?.inspection_checklist || detailRow.inspection_checklist"
        class="detail-section"
      >
        <h4 class="section-title">Buyer Inspection Checklist</h4>
        <ul class="checklist">
          <li
            v-for="(item, i) in checklistItems((detailRow.admin_detail?.inspection_checklist || detailRow.inspection_checklist)!)"
            :key="i"
            class="checklist-item"
          >
            <span class="check-icon" :class="{ ok: item.ok }">{{ item.ok ? '✓' : '—' }}</span>
            <span class="check-label">{{ item.label }}</span>
          </li>
        </ul>
        <p class="muted checklist-at">
          Accepted {{ fmtDateTime((detailRow.admin_detail?.inspection_checklist || detailRow.inspection_checklist)!.created_at) }}
        </p>
      </section>

      <!-- Rejection -->
      <section v-if="detailRow.rejection || hasRejection(detailRow)" class="detail-section">
        <h4 class="section-title">Inspection Rejection</h4>
        <template v-if="detailRow.rejection">
          <dl class="kv">
            <dt>Reason</dt><dd>{{ reasonLabel(detailRow.rejection.reason_category) }}</dd>
            <dt>Status</dt>
            <dd><StatusBadge :label="rejectionLabel(detailRow)" :tone="rejectionTone(detailRow)" /></dd>
            <dt>Filed</dt><dd>{{ fmtDateTime(detailRow.rejection.created_at) }}</dd>
            <dt v-if="detailRow.rejection.resolved_at">Resolved</dt>
            <dd v-if="detailRow.rejection.resolved_at">{{ fmtDateTime(detailRow.rejection.resolved_at) }}</dd>
            <dt>Explanation</dt><dd class="wrap">{{ detailRow.rejection.explanation }}</dd>
            <dt v-if="detailRow.rejection.admin_note">Admin note</dt>
            <dd v-if="detailRow.rejection.admin_note" class="wrap">{{ detailRow.rejection.admin_note }}</dd>
          </dl>

          <div v-if="detailRow.rejection.evidence?.length" class="evidence-gallery">
            <div v-for="ev in detailRow.rejection.evidence" :key="ev.id" class="evidence-item">
              <button
                v-if="isImage(ev.mime_type)"
                type="button"
                class="evidence-btn"
                @click="evidencePreview = ev"
                :title="ev.filename || ''"
              >
                <img :src="imgUrl(ev.file_url)" :alt="ev.filename || 'evidence'" />
              </button>
              <button
                v-else-if="isVideo(ev.mime_type)"
                type="button"
                class="evidence-btn"
                @click="evidencePreview = ev"
                :title="ev.filename || ''"
              >
                <video :src="imgUrl(ev.file_url)" muted playsinline />
                <span class="play">▶</span>
              </button>
              <a
                v-else
                :href="imgUrl(ev.file_url)"
                target="_blank"
                rel="noopener"
                class="evidence-doc"
                :title="ev.filename || ''"
              >
                <span class="doc-icon">📄</span>
                <span class="doc-name">{{ ev.filename || 'Document' }}</span>
              </a>
            </div>
          </div>
          <p v-else class="muted">No evidence attached.</p>

          <!-- Admin adjudication actions -->
          <template v-if="detailRow.status === 'inspection_rejected'">
            <label class="admin-label" for="admin-note">Admin note (optional)</label>
            <textarea
              id="admin-note"
              v-model="resolveNote"
              rows="3"
              placeholder="Notes visible to internal staff…"
            />
            <div class="admin-actions">
              <button
                class="btn-danger"
                :disabled="resolving"
                @click="pendingResolution = 'accept'"
              >Accept rejection (release hold)</button>
              <button
                class="btn-primary"
                :disabled="resolving"
                @click="pendingResolution = 'uphold'"
              >Uphold sale (capture)</button>
            </div>
          </template>
        </template>
        <p v-else class="muted">
          Rejection is on record but full detail is not yet loaded.
        </p>
      </section>
    </template>
  </Drawer>

  <!-- ── Evidence preview modal ── -->
  <div v-if="evidencePreview" class="modal-overlay" @click.self="evidencePreview = null">
    <div class="modal" role="dialog" aria-label="Evidence preview">
      <header>
        <h2>{{ evidencePreview.filename || 'Evidence' }}</h2>
        <button class="ghost close" @click="evidencePreview = null" aria-label="Close">×</button>
      </header>
      <div class="modal-body">
        <img
          v-if="isImage(evidencePreview.mime_type)"
          :src="imgUrl(evidencePreview.file_url)"
          alt=""
          class="preview-img"
        />
        <video
          v-else-if="isVideo(evidencePreview.mime_type)"
          :src="imgUrl(evidencePreview.file_url)"
          controls
          class="preview-video"
        />
        <iframe
          v-else
          :src="imgUrl(evidencePreview.file_url)"
          class="preview-iframe"
          title="Evidence"
        />
      </div>
    </div>
  </div>

  <!-- Confirm dialogs for rejection resolution -->
  <ConfirmDialog
    :open="pendingResolution === 'accept'"
    title="Accept rejection?"
    message="The Stripe hold will be released and the buyer will not be charged. The sale will be marked as refunded."
    confirm-label="Release hold"
    destructive
    @confirm="resolveRejection('accept')"
    @cancel="pendingResolution = null"
  />
  <ConfirmDialog
    :open="pendingResolution === 'uphold'"
    title="Uphold sale?"
    message="The buyer's card will be captured for the sale amount and the vehicle will be marked as sold. This cannot be undone."
    confirm-label="Capture payment"
    @confirm="resolveRejection('uphold')"
    @cancel="pendingResolution = null"
  />
</template>

<style scoped>
.filters {
  display: flex; gap: 16px; align-items: center;
  margin-bottom: 16px;
  flex-wrap: wrap;
}
.search { flex: 1; max-width: 420px; min-width: 220px; }
.pills {
  display: flex; gap: 4px; padding: 4px;
  background: var(--surface); border: 1px solid var(--border);
  border-radius: 999px;
  flex-wrap: wrap;
}
.pills button {
  border: none; background: transparent;
  padding: 6px 14px; border-radius: 999px;
  color: var(--text-muted);
}
.pills button.active { background: var(--accent-soft); color: var(--accent-strong); }

.car-cell { display: flex; align-items: center; gap: 12px; }
.thumb {
  width: 40px; height: 40px; border-radius: 6px; object-fit: cover;
  border: 1px solid var(--border); flex-shrink: 0;
}
.thumb-placeholder { background: var(--bg); }

.muted { color: var(--text-muted); }
.wrap { white-space: pre-wrap; }
.danger-text { color: var(--danger); }
.loading { color: var(--text-muted); padding: 32px; text-align: center; }

/* ---- Detail sections ---- */
.detail-section {
  padding-bottom: 16px;
  margin-bottom: 16px;
  border-bottom: 1px solid var(--border);
}
.detail-section:last-child {
  border-bottom: none;
  margin-bottom: 0;
}
.section-title {
  margin: 0 0 12px;
  font-size: 13px;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-muted);
  font-weight: 700;
}
.kv { display: grid; grid-template-columns: 140px 1fr; gap: 10px 16px; margin: 0; }
.kv dt { color: var(--text-muted); }
.kv dd { margin: 0; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12.5px; word-break: break-all; }
.inline-link { color: var(--accent-strong); font-size: 13px; }
.source-tag {
  display: inline-block;
  margin-left: 6px;
  font-size: 11px;
  color: var(--text-subtle);
  font-style: italic;
}

/* ---- Summary actions ---- */
.summary-actions { margin-top: 12px; }
.btn-secondary {
  padding: 8px 14px;
  border-radius: 6px;
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--text);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  min-height: 40px;
}
.btn-secondary:hover { border-color: var(--border-strong); }

/* ---- Timeline ---- */
.timeline { list-style: none; margin: 0; padding: 0; }
.timeline-item { display: flex; gap: 12px; padding: 0 0 14px; position: relative; }
.timeline-item:not(:last-child)::before {
  content: '';
  position: absolute;
  left: 4px; top: 12px; bottom: 0;
  width: 2px;
  background: var(--border);
}
.timeline-dot {
  width: 10px; height: 10px;
  border-radius: 50%;
  background: var(--accent-strong);
  margin-top: 3px;
  flex-shrink: 0;
  position: relative;
  z-index: 1;
}
.timeline-label { font-size: 13.5px; color: var(--text); }
.timeline-at { font-size: 12px; color: var(--text-muted); }

/* ---- Car photos ---- */
.photo-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 8px;
  margin: 4px 0 12px;
}
.photo-link { display: block; }
.photo-grid img {
  width: 100%; height: 100px; object-fit: cover;
  border-radius: 6px; border: 1px solid var(--border);
  display: block;
}

/* ---- Documents ---- */
.doc-subhead {
  margin: 12px 0 8px;
  font-size: 12px;
  font-weight: 700;
  color: var(--text-muted);
}
.doc-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 10px;
}
.doc-card {
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 10px;
  background: var(--bg);
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}
.doc-card-head {
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--text-muted);
  font-weight: 700;
}
.doc-entry { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.doc-thumb-link { display: block; }
.doc-thumb {
  width: 100%; height: 120px; object-fit: cover;
  border-radius: 6px; border: 1px solid var(--border);
  background: #fff; display: block;
}
.doc-open {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-height: 40px;
  padding: 8px 10px;
  border-radius: 6px;
  background: var(--accent-soft);
  color: var(--accent-strong);
  font-size: 12.5px;
  font-weight: 500;
  text-decoration: none;
  min-width: 0;
}
.doc-icon { flex-shrink: 0; }
.doc-open-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.doc-missing {
  font-size: 13px;
  color: var(--text-subtle);
  font-style: italic;
  padding: 8px 0;
}

/* ---- Buyer/seller party cards ---- */
.party-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}
.party-card {
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 12px;
  background: var(--bg);
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.party-role {
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--text-muted);
  font-weight: 700;
}
.party-kv { grid-template-columns: 64px 1fr; gap: 6px 12px; }
.id-doc { display: flex; flex-direction: column; gap: 6px; }
.id-doc-label { font-size: 11px; text-transform: uppercase; color: var(--text-muted); font-weight: 600; }
.id-doc-preview { display: block; }
.id-doc-preview img {
  max-width: 100%;
  max-height: 160px;
  border-radius: 6px;
  border: 1px solid var(--border);
  background: #fff;
  display: block;
}

/* ---- Inspection checklist ---- */
.checklist { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
.checklist-item { display: flex; align-items: flex-start; gap: 10px; font-size: 13.5px; }
.check-icon {
  flex-shrink: 0;
  width: 20px; height: 20px;
  border-radius: 50%;
  display: inline-flex; align-items: center; justify-content: center;
  font-size: 12px; font-weight: 700;
  background: var(--bg); color: var(--text-subtle);
  border: 1px solid var(--border);
}
.check-icon.ok { background: #d1fae5; color: #047857; border-color: #a7f3d0; }
.check-label { padding-top: 1px; }
.checklist-at { margin: 10px 0 0; font-size: 12px; }

/* ---- Signatures ---- */
.sig-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
  margin-top: 16px;
}
.sig-box {
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 10px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}
.sig-label { font-size: 11px; text-transform: uppercase; color: var(--text-muted); font-weight: 600; }
.sig-img { max-width: 100%; background: #fff; border-radius: 6px; }
.sig-empty { color: var(--text-muted); font-size: 13px; padding: 20px 0; text-align: center; }
.sig-date { font-size: 11px; color: var(--text-muted); }
.pdf-block {
  margin: 14px 0 0;
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.pdf-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 8px 14px;
  border-radius: 6px;
  background: var(--accent-soft);
  color: var(--accent-strong);
  font-size: 13px;
  font-weight: 500;
  text-decoration: none;
  min-height: 36px;
}
.pdf-empty { margin: 0; font-size: 13px; }

/* ---- Admin actions ---- */
.admin-label {
  display: block;
  margin-top: 12px;
  font-size: 12px;
  color: var(--text-muted);
}
.admin-actions {
  display: flex;
  gap: 8px;
  margin-top: 12px;
  flex-wrap: wrap;
}
.btn-primary, .btn-danger {
  padding: 8px 14px;
  border-radius: 6px;
  border: none;
  cursor: pointer;
  font-size: 13px;
  font-weight: 500;
  min-height: 36px;
}
.btn-primary { background: var(--accent-strong); color: #fff; }
.btn-danger  { background: var(--danger); color: #fff; }
.btn-primary:disabled, .btn-danger:disabled { opacity: 0.5; cursor: default; }

textarea {
  width: 100%;
  min-height: 60px;
  padding: 8px 10px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 6px;
  font-family: inherit;
  font-size: 13px;
  color: var(--text);
  margin-top: 4px;
  box-sizing: border-box;
}

/* ---- Evidence gallery ---- */
.evidence-gallery {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(120px, 1fr));
  gap: 8px;
  margin-top: 12px;
}
.evidence-item { position: relative; }
.evidence-btn {
  padding: 0;
  border: 1px solid var(--border);
  border-radius: 8px;
  overflow: hidden;
  background: var(--bg);
  cursor: pointer;
  width: 100%;
  aspect-ratio: 1 / 1;
  display: block;
  position: relative;
}
.evidence-btn img,
.evidence-btn video {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
.evidence-btn .play {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  font-size: 24px;
  background: rgba(0,0,0,0.25);
  pointer-events: none;
}
.evidence-doc {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 12px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 8px;
  text-decoration: none;
  color: var(--text);
  font-size: 12px;
  aspect-ratio: 1 / 1;
  flex-direction: column;
  justify-content: center;
  text-align: center;
}
.doc-icon { font-size: 24px; }
.doc-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 100%; }

/* ---- Modal (evidence preview) ---- */
.modal-overlay {
  position: fixed; inset: 0;
  background: rgba(17, 24, 39, 0.55);
  display: flex; align-items: center; justify-content: center;
  z-index: 120;
  padding: 16px;
}
.modal {
  background: var(--surface);
  border-radius: 12px;
  width: min(880px, 96vw);
  max-height: 90vh;
  display: flex; flex-direction: column;
  box-shadow: 0 12px 32px rgba(0,0,0,0.24);
}
.modal header {
  display: flex; align-items: center; justify-content: space-between;
  padding: 14px 18px;
  border-bottom: 1px solid var(--border);
}
.modal header h2 { margin: 0; font-size: 16px; }
.modal .close { font-size: 22px; line-height: 1; padding: 0 8px; background: transparent; border: none; }
.modal-body {
  padding: 16px 18px;
  overflow: auto;
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 200px;
}
.preview-img { max-width: 100%; max-height: 75vh; }
.preview-video { max-width: 100%; max-height: 75vh; }
.preview-iframe { width: 100%; height: 75vh; border: none; }

/* ---- Responsive: cards on phones, table everywhere else ---- */
.mobile-only { display: none; }
.desktop-only { display: block; }
@media (max-width: 640px) {
  .mobile-only { display: block; }
  .desktop-only { display: none; }
  .kv { grid-template-columns: 104px 1fr; }
  .party-kv { grid-template-columns: 64px 1fr; }
  .sig-row { grid-template-columns: 1fr; }
  .party-grid { grid-template-columns: 1fr; }
  .photo-grid { grid-template-columns: repeat(2, 1fr); }
  .doc-grid { grid-template-columns: 1fr; }
}

/* ---- Card list (mobile) ---- */
.card-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.card-state {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 32px;
  text-align: center;
  color: var(--text-muted);
}
.purchase-card {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 12px;
  box-shadow: var(--shadow-sm);
  cursor: pointer;
}
.card-top { display: flex; gap: 12px; align-items: flex-start; }
.card-thumb {
  width: 64px; height: 64px;
  border-radius: 8px;
  object-fit: cover;
  border: 1px solid var(--border);
  flex-shrink: 0;
}
.card-thumb-placeholder { background: var(--bg); }
.card-meta { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 4px; }
.card-title { font-weight: 600; font-size: 15px; color: var(--text); word-break: break-word; }
.card-sub { font-size: 13px; color: var(--text-muted); }
.card-parties { font-size: 12px; color: var(--text); display: flex; flex-direction: column; gap: 2px; margin-top: 4px; }
.card-parties .muted { display: inline-block; min-width: 46px; }
.card-badges { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 6px; }

.mobile-pager {
  display: flex;
  flex-direction: column;
  gap: 8px;
  align-items: center;
  padding: 12px 4px 4px;
  color: var(--text-muted);
  font-size: 13px;
}
.mobile-pager .pager { display: flex; align-items: center; gap: 8px; }
.mobile-pager .pager button { min-height: 44px; min-width: 72px; }
.mobile-pager .page-num { color: var(--text-muted); }
</style>
