<script setup lang="ts">
import { ref, watch } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import DataTable from '../components/DataTable.vue'
import StatusBadge from '../components/StatusBadge.vue'
import Toggle from '../components/Toggle.vue'
import Drawer from '../components/Drawer.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import { adminApi } from '../api/admin'
import { ApiError } from '../api/client'
import type { AdminCar, AdminCarDetail, AdminCarDocument } from '../api/types'
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
    // Approval is refused (422 MISSING_REQUIRED_DOCUMENTS) while the owner
    // hasn't uploaded registration + inspection + insurance (+ title when
    // for-sale). Tell the admin exactly which documents are missing instead
    // of a generic failure, and sync the row badge with the server's answer.
    if (e instanceof ApiError && e.code === 'MISSING_REQUIRED_DOCUMENTS') {
      const missing = Array.isArray(e.details?.missing)
        ? (e.details!.missing as string[])
        : []
      if (missing.length) {
        car.missing_required_documents = missing
        if (detail.value?.id === car.id) {
          detail.value = { ...detail.value, missing_required_documents: missing }
        }
      }
      const list = missing.map(docLabel).join(', ')
      toast.error(
        list
          ? `Can't approve — missing required documents: ${list}. The owner must upload them from the app first.`
          : e.message || "Can't approve — required documents are missing.",
      )
    } else {
      toast.error(e?.message || 'Action failed')
    }
  } finally {
    pending.value = null
  }
}

function statusTone(s: string): 'success' | 'warning' | 'neutral' {
  if (s === 'available') return 'success'
  if (s === 'pending')   return 'warning'
  return 'neutral'
}

// ---- Required documents (Point 10) ----
// The backend computes missing_required_documents per car (registration +
// inspection + insurance, plus title when is_for_sale). Approved cars are
// grandfathered, so the warning badge only matters for not-yet-approved cars.
const DOC_LABELS: Record<string, string> = {
  registration: 'Registration',
  inspection: 'Inspection',
  insurance: 'Insurance',
  title: 'Title',
}
function docLabel(t: string): string {
  return DOC_LABELS[t] || t
}
function missingDocs(car: AdminCar): string[] {
  return car.missing_required_documents ?? []
}
function missingDocsLabel(car: AdminCar): string {
  return missingDocs(car).map(docLabel).join(', ')
}
/** Warn only where it blocks something: unapproved cars with missing docs. */
function showMissingDocsWarning(car: AdminCar): boolean {
  return !car.is_approved && missingDocs(car).length > 0
}

function pageCount(t: number, l: number) {
  return Math.max(1, Math.ceil(t / l))
}

// ---- Documents (Point 7) ----
// The admin car-detail endpoint now returns documents[] with SIGNED file_url.
// We group them by the canonical required types (title / registration /
// inspection / insurance) so the admin can eyeball everything BEFORE approving,
// and show a "Not uploaded" placeholder per missing type. Any document of an
// unexpected type is appended as its own group so nothing is silently hidden.
const CAR_DOC_TYPES = ['title', 'registration', 'inspection', 'insurance']

function carDocGroups(d: AdminCarDetail): { type: string; label: string; docs: AdminCarDocument[] }[] {
  const present = d.documents ?? []
  const extra = [...new Set(present.map((x) => x.document_type))].filter((t) => !CAR_DOC_TYPES.includes(t))
  return [...CAR_DOC_TYPES, ...extra].map((type) => ({
    type,
    label: docLabel(type),
    docs: present.filter((x) => x.document_type === type),
  }))
}

/** Detect image documents by extension so we can inline-preview them.
 *  Signed URLs carry a ?sig=&exp= tail, so strip the query first and prefer
 *  the stored file_name when present. */
function isImageDoc(doc: { file_name?: string | null; file_url: string }): boolean {
  const name = (doc.file_name || doc.file_url).split('?')[0].toLowerCase()
  return /\.(png|jpe?g|gif|webp|heic|heif|bmp)$/.test(name)
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
        <th>Docs</th>
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
        <td>
          <StatusBadge
            v-if="showMissingDocsWarning(row)"
            label="Missing docs"
            tone="warning"
            :title="`Missing: ${missingDocsLabel(row)} — approval will be refused`"
          />
          <span v-else class="muted">—</span>
        </td>
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
            <StatusBadge
              v-if="showMissingDocsWarning(row)"
              :label="`Missing: ${missingDocsLabel(row)}`"
              tone="warning"
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
      <!-- Photos: tap/click any photo to open it full-size in a new tab. -->
      <section v-if="detail.photos.length" class="drawer-block">
        <h4 class="section-title">Photos</h4>
        <div class="photo-grid">
          <a
            v-for="p in detail.photos"
            :key="p.id"
            :href="imgUrl(p.file_url)"
            target="_blank"
            rel="noopener"
            class="photo-link"
          >
            <img :src="imgUrl(p.file_url)" :alt="p.slot_type" />
          </a>
        </div>
      </section>

      <!-- Documents (Point 7): grouped by required type, inline preview for
           images + open/download link for everything, "Not uploaded" per
           missing type. Signed URLs are rendered verbatim. -->
      <section class="drawer-block">
        <h4 class="section-title">Documents</h4>
        <div class="doc-grid">
          <div v-for="g in carDocGroups(detail)" :key="g.type" class="doc-card">
            <div class="doc-card-head">{{ g.label }}</div>
            <template v-if="g.docs.length">
              <div v-for="doc in g.docs" :key="doc.id" class="doc-entry">
                <a
                  v-if="isImageDoc(doc)"
                  :href="imgUrl(doc.file_url)"
                  target="_blank"
                  rel="noopener"
                  class="doc-thumb-link"
                  :title="doc.file_name || g.label"
                >
                  <img :src="imgUrl(doc.file_url)" :alt="`${g.label} document`" class="doc-thumb" />
                </a>
                <a
                  :href="imgUrl(doc.file_url)"
                  target="_blank"
                  rel="noopener"
                  class="doc-open"
                >
                  <span v-if="!isImageDoc(doc)" class="doc-icon">📄</span>
                  <span class="doc-open-label">{{ doc.file_name || 'Open / download' }}</span>
                </a>
              </div>
            </template>
            <div v-else class="doc-missing">Not uploaded</div>
          </div>
        </div>
      </section>

      <dl class="kv">
        <dt>Make / Model</dt><dd>{{ detail.make }} {{ detail.model }}</dd>
        <dt>Year</dt><dd>{{ detail.year }}</dd>
        <dt>Owner</dt><dd>{{ detail.owner_email || '—' }}</dd>
        <dt>Status</dt><dd><StatusBadge :label="detail.status" :tone="statusTone(detail.status)" /></dd>
        <dt>Required docs</dt>
        <dd>
          <template v-if="missingDocs(detail).length">
            <StatusBadge :label="`Missing: ${missingDocsLabel(detail)}`" tone="warning" />
            <p v-if="!detail.is_approved" class="docs-hint">
              Approval will be refused until the owner uploads these from the app.
            </p>
            <p v-else class="docs-hint">
              Grandfathered — approved before documents became required.
            </p>
          </template>
          <StatusBadge
            v-else-if="detail.missing_required_documents"
            label="All uploaded"
            tone="success"
          />
          <!-- Field absent = backend not reporting doc status yet. -->
          <span v-else class="muted">—</span>
        </dd>
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

.drawer-block {
  padding-bottom: 16px;
  margin-bottom: 16px;
  border-bottom: 1px solid var(--border);
}
.section-title {
  margin: 0 0 12px;
  font-size: 13px;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-muted);
  font-weight: 700;
}

.photo-grid {
  display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px;
}
.photo-link { display: block; }
.photo-grid img {
  width: 100%; height: 110px; object-fit: cover;
  border-radius: 6px; border: 1px solid var(--border);
  display: block;
}

/* ---- Documents ---- */
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
  width: 100%;
  height: 120px;
  object-fit: cover;
  border-radius: 6px;
  border: 1px solid var(--border);
  background: #fff;
  display: block;
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

.kv { display: grid; grid-template-columns: 140px 1fr; gap: 12px 16px; margin: 0; }
.kv dt { color: var(--text-muted); }
.kv dd { margin: 0; }
.docs-hint { margin: 6px 0 0; font-size: 12.5px; color: var(--text-muted); line-height: 1.4; }
.loading { color: var(--text-muted); padding: 32px; text-align: center; }

/* ---- Responsive split: cards on phones, table everywhere else ---- */
.mobile-only { display: none; }
.desktop-only { display: block; }

@media (max-width: 640px) {
  .mobile-only { display: block; }
  .desktop-only { display: none; }

  /* Drawer: stack the key/value rows and tighten the grids for phones. */
  .kv { grid-template-columns: 1fr; gap: 4px 0; }
  .kv dt { margin-top: 8px; }
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
