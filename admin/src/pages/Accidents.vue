<script setup lang="ts">
import { ref } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import DataTable from '../components/DataTable.vue'
import { adminApi } from '../api/admin'
import type { AdminAccident } from '../api/types'
import { useToastStore } from '../stores/toast'
import { fmtDate } from '../utils/format'

const toast = useToastStore()
const rows = ref<AdminAccident[]>([])
const total = ref(0)
const page = ref(1)
const limit = ref(50)
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    const res = await adminApi.listAccidents()
    rows.value = res.items
    total.value = res.total
  } catch (e: any) {
    toast.error(e?.message || 'Failed to load accidents')
  } finally {
    loading.value = false
  }
}
load()
</script>

<template>
  <PageHeader title="Accidents" />

  <p class="note">
    Accident reports are not yet captured by the backend; this list will populate
    once the reporting flow ships. The page is wired so it can switch on without UI changes.
  </p>

  <DataTable
    :rows :loading :total :page :limit
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
      <td>—</td>
    </template>
    <template #empty>No accident reports yet.</template>
  </DataTable>
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
</style>
