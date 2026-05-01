<script setup lang="ts">
import { ref, watch } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import DataTable from '../components/DataTable.vue'
import StatusBadge from '../components/StatusBadge.vue'
import Drawer from '../components/Drawer.vue'
import { adminApi } from '../api/admin'
import type { AdminRent } from '../api/types'
import { useToastStore } from '../stores/toast'
import { fmtDate, fmtMoney } from '../utils/format'

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
    </template>
    <template #row="{ row }">
      <td>{{ fmtDate(row.created_at) }}</td>
      <td>{{ row.driver_name || row.driver_email }}</td>
      <td>{{ row.owner_name || row.owner_email }}</td>
      <td>{{ row.car_title }} {{ row.car_year }}</td>
      <td>{{ fmtDate(row.start_date) }}</td>
      <td>{{ row.end_date ? fmtDate(row.end_date) : '—' }}</td>
      <td><StatusBadge :label="statusLabel(row.status)" :tone="statusTone(row.status)" /></td>
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
  </Drawer>
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

.kv { display: grid; grid-template-columns: 140px 1fr; gap: 12px 16px; margin: 0; }
.kv dt { color: var(--text-muted); }
.kv dd { margin: 0; }
</style>
