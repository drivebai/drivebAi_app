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
</style>
