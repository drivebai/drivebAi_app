<script setup lang="ts">
import { ref } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import DataTable from '../components/DataTable.vue'
import { adminApi } from '../api/admin'
import type { AdminCarSell } from '../api/types'
import { useToastStore } from '../stores/toast'
import { fmtDate } from '../utils/format'

const toast = useToastStore()
const rows = ref<AdminCarSell[]>([])
const total = ref(0)
const page = ref(1)
const limit = ref(50)
const loading = ref(false)
const expandedId = ref<string | null>(null)

async function load() {
  loading.value = true
  try {
    const res = await adminApi.listCarSells()
    rows.value = res.items
    total.value = res.total
  } catch (e: any) {
    toast.error(e?.message || 'Failed to load car sells')
  } finally {
    loading.value = false
  }
}
load()
</script>

<template>
  <PageHeader title="Car Sell" />

  <p class="note">
    Sale agreements are not yet captured by the backend. The two-form layout below
    matches the prototype and is ready to wire up once the schema lands.
  </p>

  <div class="desktop-only">
  <DataTable
    :rows :loading :total :page :limit
    :on-row-click="(r: AdminCarSell) => expandedId = expandedId === r.id ? null : r.id"
    @page="(p: number) => { page = p; load() }"
  >
    <template #header>
      <th>Creation Date</th>
      <th>Driver</th>
      <th>Car Owner</th>
      <th>Car</th>
      <th>File</th>
    </template>
    <template #row="{ row }">
      <td>{{ fmtDate(row.created_at) }}</td>
      <td>{{ row.driver_name || '—' }}</td>
      <td>{{ row.owner_name || '—' }}</td>
      <td>{{ row.car_title || '—' }}</td>
      <td><a href="#" @click.prevent="expandedId = expandedId === row.id ? null : row.id">Check</a></td>
    </template>
    <template #empty>No sale records yet.</template>
  </DataTable>
  </div>

  <!-- Phone: stacked cards; tapping one expands the form pair below. -->
  <div class="mobile-only">
    <div v-if="loading" class="card-state">Loading…</div>
    <div v-else-if="!rows.length" class="card-state">No sale records yet.</div>
    <template v-else>
      <button
        v-for="row in rows" :key="row.id"
        class="list-card"
        @click="expandedId = expandedId === row.id ? null : row.id"
      >
        <div class="lc-top">
          <span class="lc-title">{{ row.car_title || '—' }}</span>
          <span class="lc-check">{{ expandedId === row.id ? 'Hide' : 'Check' }}</span>
        </div>
        <div class="lc-name">{{ row.driver_name || '—' }} → {{ row.owner_name || '—' }}</div>
        <div class="lc-sub">{{ fmtDate(row.created_at) }}</div>
      </button>
    </template>
  </div>

  <!-- Inline preview pair (matches prototype) — content is placeholder until backend lands. -->
  <div v-if="expandedId" class="forms">
    <section class="form-card">
      <h3>Driver Sell Form</h3>
      <label>Driver Name (Last, First, M.I.)</label>
      <input disabled value="—" />
      <label>Address (Number &amp; Street)</label>
      <input disabled value="—" />
      <label>Terms &amp; Conditions</label>
      <textarea disabled rows="4" value="—" />
    </section>
    <section class="form-card">
      <h3>Seller Sell Form</h3>
      <label>Car Owner Name (Last, First, M.I.)</label>
      <input disabled value="—" />
      <label>Address (Number &amp; Street)</label>
      <input disabled value="—" />
      <label>Vehicle or Hull Identification Number</label>
      <input disabled value="—" />
      <label>Terms &amp; Conditions</label>
      <textarea disabled rows="4" value="—" />
    </section>
  </div>
</template>

<style scoped>
.note {
  background: var(--accent-soft);
  color: var(--accent-strong);
  padding: 10px 14px;
  border-radius: var(--radius);
  margin: 0 0 16px;
  font-size: 13px;
}
.forms {
  display: grid; grid-template-columns: 1fr 1fr; gap: 16px;
  margin-top: 16px;
}
.form-card {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 24px;
}
.form-card h3 { margin: 0 0 20px; text-align: center; font-weight: 600; }
.form-card label { margin-top: 12px; }

.mobile-only { display: none; }

/* ── Phone: table → cards + the two forms stack (≤640px) ─────── */
@media (max-width: 640px) {
  .desktop-only { display: none; }
  .mobile-only {
    display: block;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    overflow: hidden;
  }
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
  .lc-check { color: var(--accent-strong); font-size: 13px; font-weight: 600; }
  .lc-name { font-size: 13px; color: var(--text); }
  .lc-sub { font-size: 12px; color: var(--text-muted); margin-top: 2px; }
  .card-state { padding: 32px; text-align: center; color: var(--text-muted); }

  /* The two forms stack instead of being squeezed into ~127px each. */
  .forms { grid-template-columns: 1fr; }
  .form-card { padding: 16px; }
}
</style>
