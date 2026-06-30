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

function pageCount(t: number, l: number) {
  return Math.max(1, Math.ceil(t / l))
}
</script>

<template>
  <PageHeader title="Vehicles" />

  <div class="filters">
    <input v-model="query" placeholder="Search by title, make, model, or owner email…" class="search" />
  </div>

  <!-- Desktop / tablet: full data table. Hidden on phones in favour of cards. -->
  <div class="desktop-only">
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
  </div>

  <!-- Mobile: stacked-card list with visible approve/hide actions.
       Approving is the road-side workflow we're optimising for. -->
  <div class="mobile-only card-list">
    <div v-if="loading" class="card-state">Loading…</div>
    <div v-else-if="!rows.length" class="card-state">No vehicles found.</div>
    <article v-for="row in rows" :key="row.id" class="vehicle-card">
      <div class="card-top">
        <img v-if="row.cover_photo_url" :src="imgUrl(row.cover_photo_url)" alt="" class="card-thumb" />
        <div v-else class="card-thumb card-thumb-placeholder" />
        <div class="card-meta">
          <div class="card-title">{{ row.title || `${row.make} ${row.model}` }}</div>
          <div class="card-sub">{{ row.year }} &middot; {{ row.owner_email || '—' }}</div>
          <div class="card-badges">
            <StatusBadge :label="row.status" :tone="statusTone(row.status)" />
            <StatusBadge
              :label="row.is_approved ? 'Approved' : 'Not approved'"
              :tone="row.is_approved ? 'success' : 'warning'"
            />
          </div>
        </div>
      </div>
      <div class="card-actions">
        <button class="ghost details-btn" @click="openDetails(row)">Details</button>
        <button
          v-if="!row.is_approved"
          class="primary action-btn"
          @click="onToggle(row, true)"
        >Approve</button>
        <button
          v-else
          class="danger action-btn"
          @click="onToggle(row, false)"
        >Hide</button>
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

/* ---- Responsive split: cards on phones, table everywhere else ---- */
.mobile-only { display: none; }
.desktop-only { display: block; }

@media (max-width: 640px) {
  .mobile-only { display: block; }
  .desktop-only { display: none; }
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
.vehicle-card {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 12px;
  box-shadow: var(--shadow-sm);
}
.card-top {
  display: flex;
  gap: 12px;
  align-items: flex-start;
}
.card-thumb {
  width: 64px; height: 64px;
  border-radius: 8px;
  object-fit: cover;
  border: 1px solid var(--border);
  flex-shrink: 0;
}
.card-thumb-placeholder { background: var(--bg); }
.card-meta {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.card-title {
  font-weight: 600;
  font-size: 15px;
  color: var(--text);
  word-break: break-word;
}
.card-sub {
  font-size: 13px;
  color: var(--text-muted);
  word-break: break-word;
}
.card-badges {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 4px;
}
.card-actions {
  display: flex;
  gap: 8px;
  border-top: 1px solid var(--border);
  padding-top: 10px;
}
.card-actions .details-btn {
  flex: 0 0 auto;
  min-height: 44px;
  padding: 8px 14px;
}
.card-actions .action-btn {
  flex: 1;
  min-height: 44px;
  font-weight: 600;
  font-size: 15px;
}

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
.muted { color: var(--text-muted); }
</style>
