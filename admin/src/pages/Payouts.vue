<script setup lang="ts">
// Owner payouts — every owner balance and where it currently is. The point
// of this page is the UNPAID money: balances waiting on onboarding age
// visibly (Age column, reminder count, escalation marker) so nothing sits
// in escrow silently.
import { computed, ref } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import DataTable from '../components/DataTable.vue'
import StatusBadge from '../components/StatusBadge.vue'
import Drawer from '../components/Drawer.vue'
import { adminApi } from '../api/admin'
import type { AdminPayout } from '../api/types'
import { useToastStore } from '../stores/toast'
import { fmtDateTime, fmtMoney } from '../utils/format'

const toast = useToastStore()

const rows = ref<AdminPayout[]>([])
const loading = ref(false)
// '' = all; 'unpaid' is a client-side bucket (awaiting_onboarding, pending,
// failed) — the balances an admin actually needs to watch.
const filter = ref<'' | 'unpaid' | 'paid' | 'withheld'>('unpaid')

async function load() {
  loading.value = true
  try {
    const res = await adminApi.listPayouts()
    rows.value = res.payouts ?? []
  } catch (e: any) {
    toast.error(e?.message || 'Failed to load payouts')
  } finally {
    loading.value = false
  }
}
load()

const UNPAID = new Set(['awaiting_onboarding', 'pending', 'failed'])
const visible = computed(() => {
  if (filter.value === 'unpaid') return rows.value.filter(r => UNPAID.has(r.status))
  if (filter.value === 'paid') return rows.value.filter(r => r.status === 'paid')
  if (filter.value === 'withheld') return rows.value.filter(r => r.status === 'withheld')
  return rows.value
})

const unpaidTotalCents = computed(() =>
  rows.value.filter(r => UNPAID.has(r.status)).reduce((s, r) => s + r.owner_amount_cents, 0))

function fmtCents(cents?: number | null, currency = 'USD'): string {
  if (cents == null) return '—'
  return fmtMoney(cents / 100, currency)
}

function statusLabel(s: AdminPayout['status']): string {
  switch (s) {
    case 'awaiting_onboarding': return 'Awaiting setup'
    case 'pending': return 'Sending'
    case 'paid': return 'Paid'
    case 'failed': return 'Failed'
    case 'withheld': return 'Withheld'
  }
}
function statusTone(s: AdminPayout['status']): 'success' | 'danger' | 'warning' | 'neutral' {
  switch (s) {
    case 'paid': return 'success'
    case 'failed': return 'danger'
    case 'withheld': return 'neutral'
    default: return 'warning'
  }
}

// Age reads as "how long has this owner's money been waiting". Only unpaid
// rows get the loud treatment.
function ageLabel(r: AdminPayout): string {
  const d = Math.floor(r.age_days)
  if (d < 1) return 'today'
  return `${d}d`
}
function ageTone(r: AdminPayout): 'danger' | 'warning' | 'neutral' | null {
  if (!UNPAID.has(r.status)) return null
  const d = r.age_days
  if (d >= 60) return 'danger'
  if (d >= 30) return 'warning'
  return 'neutral'
}

const detail = ref<AdminPayout | null>(null)
</script>

<template>
  <PageHeader title="Owner payouts" />

  <div class="filters">
    <div class="pills">
      <button :class="{ active: filter === 'unpaid' }" @click="filter = 'unpaid'">Unpaid</button>
      <button :class="{ active: filter === 'paid' }" @click="filter = 'paid'">Paid</button>
      <button :class="{ active: filter === 'withheld' }" @click="filter = 'withheld'">Withheld</button>
      <button :class="{ active: filter === '' }" @click="filter = ''">All</button>
    </div>
    <span v-if="unpaidTotalCents > 0" class="unpaid-total">
      Unpaid to owners: <strong>{{ fmtCents(unpaidTotalCents) }}</strong>
    </span>
  </div>

  <DataTable
    :rows="visible" :loading :total="visible.length" :page="1" :limit="visible.length || 1"
    :on-row-click="(r: AdminPayout) => detail = r"
  >
    <template #header>
      <th>Age</th>
      <th>Owner</th>
      <th>Car</th>
      <th>Owner amount</th>
      <th>Fee</th>
      <th>Status</th>
      <th>Source</th>
    </template>
    <template #row="{ row }">
      <td>
        {{ ageLabel(row) }}
        <StatusBadge v-if="ageTone(row) === 'danger'" label="Stale" tone="danger" />
        <StatusBadge v-else-if="ageTone(row) === 'warning'" label="Aging" tone="warning" />
      </td>
      <td>{{ row.owner_name || row.owner_email }}</td>
      <td>{{ row.car_title }}</td>
      <td><strong>{{ fmtCents(row.owner_amount_cents, row.currency) }}</strong></td>
      <td class="muted">{{ fmtCents(row.fee_cents, row.currency) }}</td>
      <td><StatusBadge :label="statusLabel(row.status)" :tone="statusTone(row.status)" /></td>
      <td class="muted">{{ row.source === 'admin_settlement' ? 'Admin settlement' : 'Return completed' }}</td>
    </template>
    <template #empty>
      {{ filter === 'unpaid' ? 'No unpaid owner balances — everything has settled.' : 'No payouts found.' }}
    </template>
  </DataTable>

  <Drawer v-if="detail" :title="`Payout — ${detail.car_title}`" @close="detail = null">
    <dl class="kv">
      <dt>Status</dt><dd><StatusBadge :label="statusLabel(detail.status)" :tone="statusTone(detail.status)" /></dd>
      <dt>Owner</dt><dd>{{ detail.owner_name }} ({{ detail.owner_email }})</dd>
      <dt>Kept from rent</dt><dd>{{ fmtCents(detail.gross_kept_cents, detail.currency) }}</dd>
      <dt>Platform fee</dt><dd>{{ fmtCents(detail.fee_cents, detail.currency) }} ({{ (detail.fee_bps / 100).toFixed(2) }}%)</dd>
      <dt>Owner amount</dt><dd><strong>{{ fmtCents(detail.owner_amount_cents, detail.currency) }}</strong></dd>
      <dt>Source</dt><dd>{{ detail.source === 'admin_settlement' ? 'Admin settlement' : 'Return completed' }}</dd>
      <dt>Created</dt><dd>{{ fmtDateTime(detail.created_at) }} ({{ ageLabel(detail) }} ago)</dd>
      <template v-if="detail.paid_at">
        <dt>Paid</dt><dd>{{ fmtDateTime(detail.paid_at) }}</dd>
      </template>
      <template v-if="detail.stripe_transfer_id">
        <dt>Stripe transfer</dt><dd>{{ detail.stripe_transfer_id }}</dd>
      </template>
      <template v-if="detail.failure_reason">
        <dt>Failure</dt><dd class="danger-text">{{ detail.failure_reason }}</dd>
      </template>
      <template v-if="detail.note">
        <dt>Note</dt><dd>{{ detail.note }}</dd>
      </template>
      <template v-if="detail.status === 'awaiting_onboarding'">
        <dt>Reminders sent</dt><dd>{{ detail.reminder_count }}<template v-if="detail.last_reminder_at"> (last {{ fmtDateTime(detail.last_reminder_at) }})</template></dd>
        <dt>Escalated</dt>
        <dd>
          <template v-if="detail.escalated_at">{{ fmtDateTime(detail.escalated_at) }} — a support ticket exists</template>
          <span v-else class="muted">Not yet (ticket opens at 60 days)</span>
        </dd>
      </template>
      <dt>Rent</dt><dd class="mono">{{ detail.lease_request_id }}</dd>
    </dl>
    <p v-if="detail.status === 'awaiting_onboarding'" class="muted hint">
      This owner hasn't finished payout setup. The money is safe in the
      platform balance and transfers automatically the moment their account
      is ready. Weekly reminders go out automatically; use the Rents page
      settle actions only for deliberate decisions (withhold / reversal).
    </p>
  </Drawer>
</template>

<style scoped>
.filters { display: flex; gap: 16px; align-items: center; margin-bottom: 16px; flex-wrap: wrap; }
.pills { display: flex; gap: 4px; padding: 4px; background: var(--surface); border: 1px solid var(--border); border-radius: 999px; }
.pills button { border: none; background: transparent; padding: 6px 14px; border-radius: 999px; color: var(--text-muted); }
.pills button.active { background: var(--accent-soft); color: var(--accent-strong); }
.unpaid-total { color: var(--text-muted); font-size: 13px; }
.unpaid-total strong { color: var(--text); }

.kv { display: grid; grid-template-columns: 160px 1fr; gap: 12px 16px; margin: 0; }
.kv dt { color: var(--text-muted); }
.kv dd { margin: 0; overflow-wrap: anywhere; }
.muted { color: var(--text-muted); }
.danger-text { color: var(--danger); }
.mono { font-family: ui-monospace, monospace; font-size: 12px; }
.hint { margin-top: 14px; font-size: 13px; line-height: 1.5; }

@media (max-width: 640px) {
  .kv { grid-template-columns: 1fr; gap: 2px 0; }
  .kv dt { margin-top: 8px; }
}
</style>
