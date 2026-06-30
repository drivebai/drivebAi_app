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
      <td>{{ row.end_date ? fmtDate(row.end_date) : '—' }}</td>
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
      <dt>End</dt><dd>{{ detail.end_date ? fmtDate(detail.end_date) : '—' }}</dd>
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
      </dl>
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
          <template v-if="returnFocus.return_refund_failure_reason">
            <dt>Refund error</dt><dd class="danger-text">{{ returnFocus.return_refund_failure_reason }}</dd>
          </template>
        </dl>
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
</style>
