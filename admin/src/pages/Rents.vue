<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import DataTable from '../components/DataTable.vue'
import StatusBadge from '../components/StatusBadge.vue'
import Drawer from '../components/Drawer.vue'
import { adminApi } from '../api/admin'
import type { AdminRent } from '../api/types'
import { useToastStore } from '../stores/toast'
import { fmtDate, fmtDateTime, fmtMoney } from '../utils/format'

const toast = useToastStore()

const rows = ref<AdminRent[]>([])
const total = ref(0)
const page = ref(1)
const limit = ref(50)
const loading = ref(false)
const query = ref('')
const status = ref<'' | 'active' | 'finished'>('')

let timer: number | undefined
watch(query, () => {
  if (timer) clearTimeout(timer)
  timer = window.setTimeout(() => { page.value = 1; load() }, 250)
})
watch(status, () => { page.value = 1; load() })

async function load() {
  loading.value = true
  try {
    const res = await adminApi.listRents({
      query: query.value, status: status.value, page: page.value, limit: limit.value,
    })
    rows.value = res.items
    total.value = res.total
  } catch (e: any) {
    toast.error(e?.message || 'Failed to load rents')
  } finally {
    loading.value = false
  }
}
load()

const detail = ref<AdminRent | null>(null)
function openDetails(r: AdminRent) { detail.value = r }

// Focused vehicle-return dialog. Lets admins drill into the return without
// the rest of the rental drawer noise; reuses the same AdminRent payload
// (no extra fetch — server already returns return_* fields on /rents).
const returnFocus = ref<AdminRent | null>(null)
function openReturnFocus(r: AdminRent) { returnFocus.value = r }

const ACTIVE = new Set(['accepted', 'payment_pending', 'paid'])
function statusTone(s: string): 'success' | 'neutral' | 'danger' | 'warning' {
  if (ACTIVE.has(s)) return 'success'
  if (s === 'declined' || s === 'cancelled' || s === 'expired') return 'neutral'
  if (s === 'requested') return 'warning'
  return 'neutral'
}
function statusLabel(s: string): string {
  if (ACTIVE.has(s)) return 'Active'
  if (s === 'declined' || s === 'cancelled' || s === 'expired' || s === 'paid') return 'Finished'
  return s
}

// ---- Vehicle return helpers ----
// We treat the row as having a return iff return_status is set. Anything else
// renders as a dash so a partial backend rollout doesn't show "—" badges
// where there is genuinely no return yet.
function hasReturn(r: AdminRent): boolean {
  return !!r.return_status
}

function returnLabel(r: AdminRent): string {
  switch (r.return_status) {
    case 'driver_initiated': return 'Pending owner'
    case 'owner_confirmed':  return 'Refund pending'
    case 'disputed':         return 'Disputed'
    case 'completed':        return refundFailed(r) ? 'Refund failed' : 'Completed'
    case 'cancelled':        return 'Cancelled'
    default:                 return '—'
  }
}

// "Manual" review (red) is anything where money is owed but Stripe is stuck
// or the owner is contesting. Greens are only true success states.
function refundFailed(r: AdminRent): boolean {
  return r.return_refund_status === 'failed'
}

function returnTone(r: AdminRent): 'success' | 'danger' | 'warning' | 'neutral' {
  if (!r.return_status) return 'neutral'
  if (r.return_status === 'disputed') return 'danger'
  if (refundFailed(r)) return 'danger'
  if (r.return_status === 'completed') return 'success'
  if (r.return_status === 'cancelled') return 'neutral'
  // driver_initiated and owner_confirmed are in-flight.
  return 'warning'
}

// fmtMoney expects dollars; the backend ships refund as cents.
function fmtCents(cents?: number | null, currency = 'USD'): string {
  if (cents == null) return '—'
  return fmtMoney(cents / 100, currency)
}

// Total paid-day window for the lease. Used to show "X of Y days used".
function totalDays(r: AdminRent): number {
  return Math.max(0, (r.weeks || 0) * 7)
}

function initiatedBy(r: AdminRent): string {
  if (r.return_initiated_by_name) return r.return_initiated_by_name
  if (r.return_initiated_by_email) return r.return_initiated_by_email
  // Fallback: the spec says driver initiates returns, so default to the
  // lease driver when the backend doesn't ship the explicit initiator.
  return r.driver_name || r.driver_email || '—'
}

const drawerReturnRows = computed(() => {
  const r = detail.value
  if (!r || !hasReturn(r)) return []
  return [
    { k: 'Return status',  v: returnLabel(r) },
    { k: 'Initiated by',   v: initiatedBy(r) },
    { k: 'Driver returned', v: fmtDateTime(r.return_driver_confirmed_at) },
    { k: 'Owner confirmed', v: fmtDateTime(r.return_owner_confirmed_at) },
    { k: 'Completed',      v: fmtDateTime(r.return_completed_at) },
    { k: 'Days used',
      v: r.return_used_days != null
        ? `${r.return_used_days} of ${totalDays(r)}`
        : '—' },
    { k: 'Unused days',
      v: r.return_unused_days != null
        ? String(r.return_unused_days)
        : '—' },
    { k: 'Refund amount',  v: fmtCents(r.return_refund_amount_cents, r.currency) },
    { k: 'Refund status',  v: r.return_refund_status || '—' },
  ]
})

// ---- Scheduled term end (lifecycle batch, defect 2) ----
// rental_ends_at is the LIVE end of the paid term (set at pickup); end_date
// stays what it always was — the terminal-status timestamp for history rows.
function termCell(r: AdminRent): string {
  if (r.end_date) return fmtDate(r.end_date)
  if (r.rental_ends_at) return fmtDate(r.rental_ends_at)
  return '—'
}
function termChip(r: AdminRent): { label: string; tone: 'danger' | 'warning' | 'neutral' } | null {
  if (r.end_date || !r.rental_ends_at || r.status !== 'paid') return null
  const days = Math.ceil((new Date(r.rental_ends_at).getTime() - Date.now()) / 86_400_000)
  if (days < 0) return { label: `Overdue ${-days}d`, tone: 'danger' }
  if (days === 0) return { label: 'Due today', tone: 'danger' }
  if (days === 1) return { label: 'Ends tomorrow', tone: 'warning' }
  return { label: `${days}d left`, tone: 'neutral' }
}

// ---- Dispute resolution (lifecycle batch, defect 4) ----
// The backend resolve endpoint existed all along; the console just never
// called it. Accept = side with the driver (refund + release the car);
// Reject = side with the owner (return cancelled, rental continues). The
// note is REQUIRED — both parties receive the outcome with it.
const resolving = ref<{ rent: AdminRent; resolution: 'accept' | 'reject' } | null>(null)
const resolutionNote = ref('')
const savingResolution = ref(false)
const resolutionError = ref<string | null>(null)

function isDisputed(r: AdminRent): boolean {
  return r.return_status === 'disputed'
}

function startResolve(r: AdminRent, resolution: 'accept' | 'reject') {
  resolving.value = { rent: r, resolution }
  resolutionNote.value = ''
  resolutionError.value = null
}

async function confirmResolve() {
  const target = resolving.value
  if (!target || savingResolution.value) return
  if (!target.rent.return_id) {
    resolutionError.value = 'This rental has no return record.'
    return
  }
  const note = resolutionNote.value.trim()
  if (note.length < 5) {
    resolutionError.value = 'A note is required — both parties will see the outcome.'
    return
  }
  savingResolution.value = true
  resolutionError.value = null
  try {
    await adminApi.resolveVehicleReturn(target.rent.return_id, target.resolution, note)
    toast.success(target.resolution === 'accept'
      ? 'Return confirmed — refund on its way; both parties notified'
      : 'Dispute upheld — the rental continues; both parties notified')
    resolving.value = null
    returnFocus.value = null
    detail.value = null
    // The resolve response is a per-viewer return shape, not an AdminRent
    // row — refetch instead of merging.
    load()
  } catch (e: any) {
    // Includes the 409 when someone else resolved it first — surface and
    // let the admin refresh.
    resolutionError.value = e?.message || 'Failed to resolve the dispute'
  } finally {
    savingResolution.value = false
  }
}
</script>

<template>
  <PageHeader title="Rents" />

  <div class="filters">
    <input v-model="query" placeholder="Search by Driver, Car Owner & Charge ID" class="search" />
    <div class="pills">
      <button :class="{ active: status === '' }"         @click="status = ''">All</button>
      <button :class="{ active: status === 'active' }"   @click="status = 'active'">Active</button>
      <button :class="{ active: status === 'finished' }" @click="status = 'finished'">Finished</button>
    </div>
  </div>

  <div class="desktop-only">
  <DataTable
    :rows :loading :total :page :limit
    :on-row-click="openDetails"
    @page="(p: number) => { page = p; load() }"
  >
    <template #header>
      <th>Creation Date</th>
      <th>Driver</th>
      <th>Car Owner</th>
      <th>Car</th>
      <th>Rent Start Date</th>
      <th>Rent End Date</th>
      <th>Status</th>
      <th>Return</th>
    </template>
    <template #row="{ row }">
      <td>{{ fmtDate(row.created_at) }}</td>
      <td>{{ row.driver_name || row.driver_email }}</td>
      <td>{{ row.owner_name || row.owner_email }}</td>
      <td>{{ row.car_title }} {{ row.car_year }}</td>
      <td>{{ fmtDate(row.start_date) }}</td>
      <td>
        {{ termCell(row) }}
        <StatusBadge
          v-if="termChip(row)"
          :label="termChip(row)!.label"
          :tone="termChip(row)!.tone"
        />
      </td>
      <td><StatusBadge :label="statusLabel(row.status)" :tone="statusTone(row.status)" /></td>
      <td>
        <button
          v-if="hasReturn(row)"
          type="button"
          class="return-chip"
          :title="returnLabel(row)"
          @click.stop="openReturnFocus(row)"
        >
          <StatusBadge :label="returnLabel(row)" :tone="returnTone(row)" />
        </button>
        <span v-else class="muted">—</span>
      </td>
    </template>
    <template #empty>No rentals found.</template>
  </DataTable>
  </div>

  <!-- Phone: stacked cards → tap opens the full-screen rental Drawer. -->
  <div class="mobile-only">
    <div v-if="loading" class="card-state">Loading…</div>
    <div v-else-if="!rows.length" class="card-state">No rentals found.</div>
    <template v-else>
      <button v-for="row in rows" :key="row.id" class="list-card" @click="openDetails(row)">
        <div class="lc-top">
          <span class="lc-title">{{ row.car_title }} {{ row.car_year }}</span>
          <StatusBadge :label="statusLabel(row.status)" :tone="statusTone(row.status)" />
        </div>
        <div class="lc-name">{{ row.driver_name || row.driver_email }} → {{ row.owner_name || row.owner_email }}</div>
        <div class="lc-sub">{{ fmtDate(row.start_date) }} – {{ termCell(row) }}</div>
        <div v-if="hasReturn(row)" class="lc-return">
          <StatusBadge :label="returnLabel(row)" :tone="returnTone(row)" />
        </div>
      </button>
      <footer v-if="total > 0" class="mobile-pager">
        <span class="muted">{{ ((page - 1) * limit) + 1 }}–{{ Math.min(page * limit, total) }} of {{ total }}</span>
        <div class="pager">
          <button :disabled="page <= 1" @click="page = page - 1; load()">‹ Prev</button>
          <span class="page-num">{{ page }} / {{ Math.max(1, Math.ceil(total / limit)) }}</span>
          <button :disabled="page >= Math.ceil(total / limit)" @click="page = page + 1; load()">Next ›</button>
        </div>
      </footer>
    </template>
  </div>

  <Drawer v-if="detail" :title="`Rental — ${detail.car_title} ${detail.car_year}`" @close="detail = null">
    <dl class="kv">
      <dt>Status</dt><dd><StatusBadge :label="detail.status" :tone="statusTone(detail.status)" /></dd>
      <dt>Driver</dt><dd>{{ detail.driver_name }} ({{ detail.driver_email }})</dd>
      <dt>Owner</dt><dd>{{ detail.owner_name }} ({{ detail.owner_email }})</dd>
      <dt>Weekly price</dt><dd>{{ fmtMoney(detail.weekly_price, detail.currency) }}</dd>
      <dt>Weeks</dt><dd>{{ detail.weeks }}</dd>
      <dt>Total</dt><dd>{{ fmtMoney(detail.weekly_price * detail.weeks, detail.currency) }}</dd>
      <dt>Created</dt><dd>{{ fmtDate(detail.created_at) }}</dd>
      <dt>Start</dt><dd>{{ fmtDate(detail.start_date) }}</dd>
      <dt>End</dt>
      <dd>
        {{ termCell(detail) }}
        <StatusBadge
          v-if="termChip(detail)"
          :label="termChip(detail)!.label"
          :tone="termChip(detail)!.tone"
        />
      </dd>
      <dt>Stripe intent</dt><dd>{{ detail.payment_intent_id || '—' }}</dd>
      <dt>Payment status</dt><dd>{{ detail.payment_status || '—' }}</dd>
    </dl>

    <template v-if="hasReturn(detail)">
      <h4 class="section">Vehicle Return</h4>
      <dl class="kv">
        <template v-for="row in drawerReturnRows" :key="row.k">
          <dt>{{ row.k }}</dt>
          <dd v-if="row.k === 'Return status'">
            <StatusBadge :label="returnLabel(detail)" :tone="returnTone(detail)" />
          </dd>
          <dd v-else>{{ row.v }}</dd>
        </template>
        <template v-if="detail.return_refund_id">
          <dt>Refund ID</dt><dd>{{ detail.return_refund_id }}</dd>
        </template>
        <template v-if="detail.return_refund_failure_reason">
          <dt>Refund error</dt><dd class="danger-text">{{ detail.return_refund_failure_reason }}</dd>
        </template>
        <template v-if="detail.return_dispute_reason">
          <dt>Dispute reason</dt><dd>{{ detail.return_dispute_reason }}</dd>
        </template>
        <template v-if="detail.return_resolution_note">
          <dt>Resolution note</dt><dd>{{ detail.return_resolution_note }}</dd>
        </template>
      </dl>
      <div v-if="isDisputed(detail)" class="resolve-actions">
        <button class="primary" @click="startResolve(detail, 'accept')">Accept return…</button>
        <button class="danger" @click="startResolve(detail, 'reject')">Reject dispute…</button>
      </div>
    </template>
    <template v-else>
      <h4 class="section">Vehicle Return</h4>
      <p class="muted">No return on file for this rental.</p>
    </template>
  </Drawer>

  <!-- Focused return dialog: a tiny modal so admins can scan return state
       without opening the full rental drawer. Same data, fewer fields. -->
  <div v-if="returnFocus" class="modal-overlay" @click.self="returnFocus = null">
    <div class="modal" role="dialog" aria-label="Vehicle return details">
      <header>
        <h2>Vehicle return — {{ returnFocus.car_title }} {{ returnFocus.car_year }}</h2>
        <button class="ghost close" @click="returnFocus = null" aria-label="Close">×</button>
      </header>
      <div class="modal-body">
        <dl class="kv">
          <dt>Status</dt>
          <dd><StatusBadge :label="returnLabel(returnFocus)" :tone="returnTone(returnFocus)" /></dd>
          <dt>Initiated by</dt><dd>{{ initiatedBy(returnFocus) }}</dd>
          <dt>Driver returned</dt><dd>{{ fmtDateTime(returnFocus.return_driver_confirmed_at) }}</dd>
          <dt>Owner confirmed</dt><dd>{{ fmtDateTime(returnFocus.return_owner_confirmed_at) }}</dd>
          <dt>Completed</dt><dd>{{ fmtDateTime(returnFocus.return_completed_at) }}</dd>
          <dt>Days used</dt>
          <dd>{{ returnFocus.return_used_days != null ? `${returnFocus.return_used_days} of ${totalDays(returnFocus)}` : '—' }}</dd>
          <dt>Unused days</dt>
          <dd>{{ returnFocus.return_unused_days ?? '—' }}</dd>
          <dt>Refund</dt>
          <dd>
            {{ fmtCents(returnFocus.return_refund_amount_cents, returnFocus.currency) }}
            <em class="muted">({{ returnFocus.return_refund_status || '—' }})</em>
          </dd>
          <template v-if="returnFocus.return_refund_id">
            <dt>Refund ID</dt><dd>{{ returnFocus.return_refund_id }}</dd>
          </template>
          <template v-if="returnFocus.return_dispute_reason">
            <dt>Dispute reason</dt><dd>{{ returnFocus.return_dispute_reason }}</dd>
          </template>
          <template v-if="returnFocus.return_resolution_note">
            <dt>Resolution note</dt><dd>{{ returnFocus.return_resolution_note }}</dd>
          </template>
          <template v-if="returnFocus.return_refund_failure_reason">
            <dt>Refund error</dt><dd class="danger-text">{{ returnFocus.return_refund_failure_reason }}</dd>
          </template>
        </dl>
        <!-- The parties for the decision, without leaving the page. -->
        <div v-if="isDisputed(returnFocus)" class="resolve-block">
          <p class="muted resolve-context">
            Driver: {{ returnFocus.driver_name }} ({{ returnFocus.driver_email }})<br />
            Owner: {{ returnFocus.owner_name }} ({{ returnFocus.owner_email }})<br />
            Accepting confirms the return{{ (returnFocus.return_refund_amount_cents ?? 0) > 0
              ? `, issues the ${fmtCents(returnFocus.return_refund_amount_cents, returnFocus.currency)} refund`
              : ' (no refund is due — the full period was used)' }} and releases
            the car. Rejecting cancels the return — the rental continues and the driver can submit a new
            return later.
          </p>
          <div class="resolve-actions">
            <button class="primary" @click="startResolve(returnFocus, 'accept')">Accept return…</button>
            <button class="danger" @click="startResolve(returnFocus, 'reject')">Reject dispute…</button>
          </div>
        </div>
      </div>
    </div>
  </div>

  <!-- Resolution note modal — required note, shown to both parties. Follows
       Users.vue's decline-with-reason pattern: validation and API errors stay
       in the modal; only success closes it. -->
  <div v-if="resolving" class="modal-overlay" @click.self="resolving = null">
    <div class="modal" role="dialog" aria-labelledby="resolveTitle">
      <header>
        <h2 id="resolveTitle">
          {{ resolving.resolution === 'accept' ? 'Accept the return?' : 'Reject the dispute?' }}
        </h2>
        <button class="ghost close" :disabled="savingResolution" @click="resolving = null" aria-label="Close">×</button>
      </header>
      <div class="modal-body">
        <p class="muted">
          {{ resolving.resolution === 'accept'
            ? `Confirms the driver's return of ${resolving.rent.car_title}${(resolving.rent.return_refund_amount_cents ?? 0) > 0
                ? `, issues the ${fmtCents(resolving.rent.return_refund_amount_cents, resolving.rent.currency)} refund,`
                : ' (no refund is due),'} and puts the car back on the market.`
            : `Sides with the owner: the return is cancelled, ${resolving.rent.car_title} stays rented, and the driver must submit a new return when the car is actually back.` }}
          Both parties are notified with your note.
        </p>
        <label class="resolve-label">
          Note (visible to both parties)
          <textarea
            v-model="resolutionNote"
            rows="3"
            maxlength="500"
            :disabled="savingResolution"
            placeholder="e.g. Photos from the owner show the car was not at the agreed location — please arrange the handover in chat."
          ></textarea>
        </label>
        <p v-if="resolutionError" class="danger-text">{{ resolutionError }}</p>
        <div class="resolve-actions">
          <button class="secondary" :disabled="savingResolution" @click="resolving = null">Cancel</button>
          <button
            :class="resolving.resolution === 'accept' ? 'primary' : 'danger'"
            :disabled="savingResolution"
            @click="confirmResolve"
          >
            {{ savingResolution ? 'Resolving…' : (resolving.resolution === 'accept' ? 'Accept return' : 'Reject dispute') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.filters {
  display: flex; gap: 16px; align-items: center;
  margin-bottom: 16px;
}
.search { flex: 1; max-width: 360px; }
.pills { display: flex; gap: 4px; padding: 4px; background: var(--surface); border: 1px solid var(--border); border-radius: 999px; }
.pills button { border: none; background: transparent; padding: 6px 14px; border-radius: 999px; color: var(--text-muted); }
.pills button.active { background: var(--accent-soft); color: var(--accent-strong); }

.kv { display: grid; grid-template-columns: 160px 1fr; gap: 12px 16px; margin: 0; }
.kv dt { color: var(--text-muted); }
.kv dd { margin: 0; }

.section { margin: 20px 0 8px; font-size: 14px; }
.muted { color: var(--text-muted); }
.danger-text { color: var(--danger); }

/* Make the return chip behave like a button without restyling the badge. */
.return-chip {
  background: transparent;
  border: none;
  padding: 0;
  cursor: pointer;
}
.return-chip:hover { opacity: 0.85; }

.modal-overlay {
  position: fixed; inset: 0;
  background: rgba(17, 24, 39, 0.45);
  display: flex; align-items: center; justify-content: center;
  z-index: 110;
}
.modal {
  background: var(--surface);
  border-radius: 12px;
  width: min(480px, 92vw);
  max-height: 80vh;
  display: flex; flex-direction: column;
  box-shadow: 0 12px 32px rgba(0,0,0,0.18);
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
  overflow-y: auto;
}

/* Dispute resolution */
.resolve-block { margin-top: 14px; border-top: 1px solid var(--border); padding-top: 12px; }
.resolve-context { font-size: 13px; line-height: 1.5; margin: 0 0 10px; }
.resolve-actions { display: flex; gap: 8px; justify-content: flex-end; margin-top: 12px; }
.resolve-label { display: block; margin-top: 10px; font-size: 13px; color: var(--text-muted); }
.resolve-label textarea {
  display: block; width: 100%; margin-top: 6px;
  padding: 10px 12px; border: 1px solid var(--border); border-radius: 8px;
  font-size: 14px; background: var(--bg, #fff); color: var(--text, #111);
  resize: vertical;
}

.mobile-only { display: none; }

/* ── Phone: table → stacked cards (≤640px) ───────────────────── */
@media (max-width: 640px) {
  .desktop-only { display: none; }
  .mobile-only {
    display: block;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    overflow: hidden;
  }

  /* Filter row wraps: search full-width, pills below (was clipping the search). */
  .filters { flex-wrap: wrap; align-items: stretch; }
  .search { flex-basis: 100%; max-width: none; }
  .pills { flex-wrap: wrap; }
  .pills button { min-height: 40px; }

  .list-card {
    display: block;
    width: 100%;
    text-align: left;
    background: transparent;
    border: none;
    border-bottom: 1px solid var(--border);
    padding: var(--m-row-py) var(--m-row-px);
    cursor: pointer;
  }
  .list-card:last-of-type { border-bottom: none; }
  .lc-top { display: flex; justify-content: space-between; align-items: center; gap: 8px; margin-bottom: 5px; }
  .lc-title { font-weight: 600; font-size: 15px; }
  .lc-name { font-size: 13px; color: var(--text); }
  .lc-sub { font-size: 12px; color: var(--text-muted); margin-top: 2px; }
  .lc-return { margin-top: 6px; }
  .card-state { padding: 32px; text-align: center; color: var(--text-muted); }

  /* Drawer/modal: stack the label/value grid and break long Stripe/refund IDs. */
  .kv { grid-template-columns: 1fr; gap: 2px 0; }
  .kv dt { margin-top: 8px; color: var(--text-muted); }
  .kv dd { overflow-wrap: anywhere; }

  .mobile-pager {
    display: flex; flex-direction: column; align-items: center; gap: 8px;
    padding: 12px; border-top: 1px solid var(--border);
    color: var(--text-muted); font-size: 13px;
  }
  .mobile-pager .pager { display: flex; align-items: center; gap: 12px; }
  .mobile-pager .pager button { min-height: 44px; min-width: 72px; }
}
</style>
