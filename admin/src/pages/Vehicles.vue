<script setup lang="ts">
import { ref, watch } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import DataTable from '../components/DataTable.vue'
import StatusBadge from '../components/StatusBadge.vue'
import Toggle from '../components/Toggle.vue'
import Drawer from '../components/Drawer.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import { adminApi } from '../api/admin'
import type { AdminCar, AdminCarDetail } from '../api/types'
import { useToastStore } from '../stores/toast'
import { fmtDate, fmtMoney, imgUrl } from '../utils/format'

const toast = useToastStore()

const rows = ref<AdminCar[]>([])
const total = ref(0)
const page = ref(1)
const limit = ref(50)
const loading = ref(false)
const query = ref('')

let searchTimer: number | undefined
watch(query, () => {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => { page.value = 1; load() }, 250)
})

async function load() {
  loading.value = true
  try {
    const res = await adminApi.listCars({ query: query.value, page: page.value, limit: limit.value })
    rows.value = res.items
    total.value = res.total
  } catch (e: any) {
    toast.error(e?.message || 'Failed to load vehicles')
  } finally {
    loading.value = false
  }
}
load()

// --- detail drawer ---
const detail = ref<AdminCarDetail | null>(null)
const detailLoading = ref(false)
async function openDetails(c: AdminCar) {
  detailLoading.value = true
  detail.value = null
  try {
    detail.value = await adminApi.getCar(c.id)
  } catch (e: any) {
    toast.error(e?.message || 'Failed to load vehicle')
  } finally {
    detailLoading.value = false
  }
}

// --- approval toggle ---
// Toggling OFF on an already-approved car will hide it from Discover for ALL drivers,
// so we confirm before turning it off. Turning ON is reversible/expected, so no confirm.
const pending = ref<{ car: AdminCar; next: boolean } | null>(null)
function onToggle(car: AdminCar, next: boolean) {
  if (car.is_approved && !next) {
    pending.value = { car, next }
  } else {
    apply(car, next)
  }
}
async function apply(car: AdminCar, next: boolean) {
  try {
    await adminApi.approveCar(car.id, next)
    car.is_approved = next
    if (detail.value?.id === car.id) detail.value = { ...detail.value, is_approved: next }
    toast.success(next ? 'Vehicle approved — visible in Discover' : 'Vehicle hidden from Discover')
  } catch (e: any) {
    toast.error(e?.message || 'Action failed')
  } finally {
    pending.value = null
  }
}

function statusTone(s: string): 'success' | 'warning' | 'neutral' {
  if (s === 'available') return 'success'
  if (s === 'pending')   return 'warning'
  return 'neutral'
}
</script>

<template>
  <PageHeader title="Vehicles" />

  <div class="filters">
    <input v-model="query" placeholder="Search by title, make, model, or owner email…" class="search" />
  </div>

  <DataTable
    :rows :loading :total :page :limit
    @page="(p: number) => { page = p; load() }"
  >
    <template #header>
      <th>Vehicle</th>
      <th>Year</th>
      <th>Owner</th>
      <th>Status</th>
      <th>Approved</th>
      <th></th>
    </template>
    <template #row="{ row }">
      <td>
        <div class="title-cell">
          <img v-if="row.cover_photo_url" :src="imgUrl(row.cover_photo_url)" alt="" class="thumb" />
          <div v-else class="thumb thumb-placeholder" />
          <span>{{ row.title || `${row.make} ${row.model}` }}</span>
        </div>
      </td>
      <td>{{ row.year }}</td>
      <td>{{ row.owner_email || '—' }}</td>
      <td><StatusBadge :label="row.status" :tone="statusTone(row.status)" /></td>
      <td @click.stop>
        <Toggle
          :model-value="row.is_approved"
          :aria-label="`Toggle approval for ${row.title}`"
          @update:model-value="(v: boolean) => onToggle(row, v)"
        />
      </td>
      <td class="actions" @click.stop>
        <button class="ghost" @click="openDetails(row)">Details</button>
      </td>
    </template>
    <template #empty>No vehicles found.</template>
  </DataTable>

  <Drawer v-if="detail || detailLoading" :title="detail?.title || 'Vehicle'" @close="detail = null">
    <div v-if="detailLoading" class="loading">Loading…</div>
    <template v-else-if="detail">
      <div v-if="detail.photos.length" class="photo-grid">
        <img v-for="p in detail.photos" :key="p.id" :src="imgUrl(p.file_url)" :alt="p.slot_type" />
      </div>
      <dl class="kv">
        <dt>Make / Model</dt><dd>{{ detail.make }} {{ detail.model }}</dd>
        <dt>Year</dt><dd>{{ detail.year }}</dd>
        <dt>Owner</dt><dd>{{ detail.owner_email || '—' }}</dd>
        <dt>Status</dt><dd><StatusBadge :label="detail.status" :tone="statusTone(detail.status)" /></dd>
        <dt>Weekly rent</dt><dd>{{ fmtMoney(detail.weekly_rent_price ?? null, detail.currency) }}</dd>
        <dt>Sale price</dt><dd>{{ fmtMoney(detail.sale_price ?? null, detail.currency) }}</dd>
        <dt>Address</dt><dd>{{ detail.address || '—' }}</dd>
        <dt>Description</dt><dd>{{ detail.description || '—' }}</dd>
        <dt>Created</dt><dd>{{ fmtDate(detail.created_at) }}</dd>
        <dt>Approved</dt>
        <dd>
          <Toggle
            :model-value="detail.is_approved"
            @update:model-value="(v: boolean) => {
              const row = rows.find(r => r.id === detail!.id)
              if (row) onToggle(row, v)
              else apply(detail as any, v)
            }"
          />
        </dd>
      </dl>
    </template>
  </Drawer>

  <ConfirmDialog
    :open="!!pending"
    title="Hide vehicle from Discover?"
    :message="`${pending?.car.title} will no longer appear to drivers in the marketplace until you re-approve it.`"
    confirm-label="Hide"
    destructive
    @confirm="pending && apply(pending.car, pending.next)"
    @cancel="pending = null"
  />
</template>

<style scoped>
.filters { margin-bottom: 16px; }
.search { width: 100%; max-width: 480px; }

.title-cell { display: flex; align-items: center; gap: 12px; }
.thumb {
  width: 40px; height: 40px; border-radius: 6px; object-fit: cover;
  border: 1px solid var(--border); flex-shrink: 0;
}
.thumb-placeholder { background: var(--bg); }

.actions { display: flex; gap: 8px; justify-content: flex-end; }

.photo-grid {
  display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px;
  margin-bottom: 20px;
}
.photo-grid img {
  width: 100%; height: 110px; object-fit: cover;
  border-radius: 6px; border: 1px solid var(--border);
}

.kv { display: grid; grid-template-columns: 140px 1fr; gap: 12px 16px; margin: 0; }
.kv dt { color: var(--text-muted); }
.kv dd { margin: 0; }
.loading { color: var(--text-muted); padding: 32px; text-align: center; }
</style>
