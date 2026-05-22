<script setup lang="ts">
import { computed, ref, watch, onMounted } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import { adminApi } from '../api/admin'
import type { AdminAccident, AccidentAttachment } from '../api/types'
import { useToastStore } from '../stores/toast'
import { fmtDateTime, imgUrl } from '../utils/format'

const toast = useToastStore()

// ── State ─────────────────────────────────────────────────────────────────────

const accidents = ref<AdminAccident[]>([])
const loading = ref(false)
const statusFilter = ref('')
const selected = ref<AdminAccident | null>(null)
const detailLoading = ref(false)
const activeTab = ref(0)
const savingStatus = ref(false)
const editStatus = ref<string>('')

const TABS = ['Photos & Videos', 'Driver 1', 'Driver 2', 'Damage', 'Description', 'Insurance', 'Other', 'Signature']

const STATUS_LABELS: Record<string, string> = {
  draft: 'Draft',
  submitted: 'Submitted',
  in_review: 'In Review',
  resolved: 'Resolved',
}

const STATUS_COLORS: Record<string, string> = {
  draft: '#718096',
  submitted: '#3182ce',
  in_review: '#d69e2e',
  resolved: '#38a169',
}

// ── Load ──────────────────────────────────────────────────────────────────────

async function loadAccidents() {
  loading.value = true
  try {
    const res = await adminApi.listAccidents({ status: statusFilter.value || undefined, limit: 100 })
    accidents.value = res.items
  } catch (e: any) {
    toast.error(e?.message || 'Failed to load accidents')
  } finally {
    loading.value = false
  }
}

async function selectAccident(a: AdminAccident) {
  activeTab.value = 0
  detailLoading.value = true
  selected.value = a
  editStatus.value = a.status
  try {
    const full = await adminApi.getAccident(a.id)
    selected.value = full
    editStatus.value = full.status
    // Update in list too
    const idx = accidents.value.findIndex(x => x.id === a.id)
    if (idx !== -1) accidents.value[idx] = full
  } catch {
    // keep what we have
  } finally {
    detailLoading.value = false
  }
}

async function saveStatus() {
  if (!selected.value || savingStatus.value) return
  savingStatus.value = true
  try {
    await adminApi.updateAccidentStatus(selected.value.id, editStatus.value)
    selected.value = { ...selected.value, status: editStatus.value as any }
    const idx = accidents.value.findIndex(x => x.id === selected.value!.id)
    if (idx !== -1) accidents.value[idx] = { ...accidents.value[idx], status: editStatus.value as any }
    toast.success('Status updated')
  } catch (e: any) {
    toast.error(e?.message || 'Failed to update status')
  } finally {
    savingStatus.value = false
  }
}

watch(statusFilter, loadAccidents)
onMounted(loadAccidents)

// ── Helpers ───────────────────────────────────────────────────────────────────

function attachmentsBySlot(attachments: AccidentAttachment[], slot: string) {
  return attachments.filter(a => a.slot === slot)
}

function isImage(mime: string) {
  return mime.startsWith('image/')
}

function isVideo(mime: string) {
  return mime.startsWith('video/')
}

function field(val?: string | null) {
  return val && val.trim() ? val : '—'
}

function diagramLabel(n?: number) {
  const labels: Record<number, string> = {
    0: '0 – Left Turn', 1: '1 – Rear End', 2: '2 – Sideswipe (same direction)',
    3: '3 – Left Turn', 4: '4 – Right Angle', 5: '5 – Right Turn',
    6: '6 – Right Turn', 7: '7 – Head On', 8: '8 – Sideswipe (opposite)',
  }
  if (n == null) return '—'
  return labels[n] ?? `Diagram ${n}`
}
</script>

<template>
  <PageHeader title="Accidents" />

  <div class="accidents-layout" :class="{ 'has-detail': !!selected }">
    <!-- ── List ── -->
    <section class="list-pane">
      <!-- Status filter -->
      <div class="filter-bar">
        <button
          v-for="(label, val) in { '': 'All', submitted: 'Submitted', in_review: 'In Review', resolved: 'Resolved' }"
          :key="val"
          class="filter-btn"
          :class="{ active: statusFilter === val }"
          @click="statusFilter = val"
        >{{ label }}</button>
      </div>

      <div v-if="loading" class="state-msg">Loading…</div>
      <div v-else-if="!accidents.length" class="state-msg">No accident reports yet.</div>

      <table v-else class="accidents-table">
        <thead>
          <tr>
            <th>Date</th>
            <th>Reporter</th>
            <th>Car</th>
            <th>Status</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="a in accidents"
            :key="a.id"
            class="row"
            :class="{ active: selected?.id === a.id }"
            @click="selectAccident(a)"
          >
            <td>{{ fmtDateTime(a.created_at) }}</td>
            <td>
              <div class="reporter-name">{{ a.reporter_name }}</div>
              <div class="reporter-email">{{ a.reporter_email }}</div>
            </td>
            <td>{{ a.car_title || '—' }}</td>
            <td>
              <span class="status-chip" :style="{ background: STATUS_COLORS[a.status] + '22', color: STATUS_COLORS[a.status] }">
                {{ STATUS_LABELS[a.status] }}
              </span>
            </td>
          </tr>
        </tbody>
      </table>
    </section>

    <!-- ── Detail ── -->
    <section v-if="selected" class="detail-pane">
      <div class="detail-header">
        <div>
          <div class="detail-title">{{ selected.reporter_name }}</div>
          <div class="detail-sub">{{ selected.reporter_email }} · Reported {{ fmtDateTime(selected.created_at) }}</div>
        </div>
        <div class="status-edit">
          <select v-model="editStatus" class="status-select">
            <option value="draft">Draft</option>
            <option value="submitted">Submitted</option>
            <option value="in_review">In Review</option>
            <option value="resolved">Resolved</option>
          </select>
          <button class="btn-primary" :disabled="savingStatus || editStatus === selected.status" @click="saveStatus">
            {{ savingStatus ? 'Saving…' : 'Save' }}
          </button>
        </div>
      </div>

      <!-- Tabs -->
      <div class="tabs">
        <button
          v-for="(tab, i) in TABS"
          :key="i"
          class="tab"
          :class="{ active: activeTab === i }"
          @click="activeTab = i"
        >{{ tab }}</button>
      </div>

      <div v-if="detailLoading" class="state-msg">Loading details…</div>
      <div v-else class="tab-content">

        <!-- 0: Photos & Videos -->
        <template v-if="activeTab === 0">
          <div v-if="!selected.attachments?.length" class="state-msg">No attachments uploaded.</div>
          <template v-else>
            <div v-for="slot in ['accident_photo','accident_video','driver1_license','driver2_plate','second_vehicle_docs']" :key="slot">
              <div v-if="attachmentsBySlot(selected.attachments, slot).length" class="attach-group">
                <div class="attach-label">{{ slot.replace(/_/g, ' ').replace(/\b\w/g, l => l.toUpperCase()) }}</div>
                <div class="attach-grid">
                  <div v-for="att in attachmentsBySlot(selected.attachments, slot)" :key="att.id" class="attach-item">
                    <img v-if="isImage(att.mime_type)" :src="imgUrl(att.file_url)" class="attach-img" />
                    <video v-else-if="isVideo(att.mime_type)" :src="imgUrl(att.file_url)" controls class="attach-video" />
                    <a v-else :href="imgUrl(att.file_url)" target="_blank" class="attach-doc">&#128196; Download</a>
                  </div>
                </div>
              </div>
            </div>
          </template>
        </template>

        <!-- 1: Driver 1 -->
        <template v-else-if="activeTab === 1">
          <div v-if="!selected.driver1_info" class="state-msg">No Driver 1 info provided.</div>
          <template v-else>
            <div class="section-title">Driver Info</div>
            <div class="field-grid">
              <div class="field-row"><span class="fl">License ID</span><span class="fv">{{ field(selected.driver1_info.driver_license_id) }}</span></div>
              <div class="field-row"><span class="fl">State of License</span><span class="fv">{{ field(selected.driver1_info.state_of_license) }}</span></div>
              <div class="field-row"><span class="fl">Driver Name</span><span class="fv">{{ field(selected.driver1_info.driver_name) }}</span></div>
              <div class="field-row"><span class="fl">Address</span><span class="fv">{{ field(selected.driver1_info.address) }}</span></div>
              <div class="field-row"><span class="fl">City</span><span class="fv">{{ field(selected.driver1_info.city) }}</span></div>
              <div class="field-row"><span class="fl">State</span><span class="fv">{{ field(selected.driver1_info.state) }}</span></div>
              <div class="field-row"><span class="fl">ZIP</span><span class="fv">{{ field(selected.driver1_info.zip) }}</span></div>
              <div class="field-row"><span class="fl">Date of Birth</span><span class="fv">{{ field(selected.driver1_info.dob) }}</span></div>
              <div class="field-row"><span class="fl">People in Vehicle</span><span class="fv">{{ field(selected.driver1_info.people_in_vehicle) }}</span></div>
              <div class="field-row"><span class="fl">Public Property Damaged</span><span class="fv">{{ field(selected.driver1_info.public_property_damaged) }}</span></div>
              <div class="field-row"><span class="fl">Injuries</span><span class="fv">{{ field(selected.driver1_info.injuries) }}</span></div>
            </div>
            <div class="section-title" style="margin-top:20px">Registrant</div>
            <div class="field-grid">
              <div class="field-row"><span class="fl">Name</span><span class="fv">{{ field(selected.driver1_info.registrant_name) }}</span></div>
              <div class="field-row"><span class="fl">Address</span><span class="fv">{{ field(selected.driver1_info.registrant_address) }}</span></div>
              <div class="field-row"><span class="fl">City</span><span class="fv">{{ field(selected.driver1_info.registrant_city) }}</span></div>
              <div class="field-row"><span class="fl">State</span><span class="fv">{{ field(selected.driver1_info.registrant_state) }}</span></div>
              <div class="field-row"><span class="fl">ZIP</span><span class="fv">{{ field(selected.driver1_info.registrant_zip) }}</span></div>
              <div class="field-row"><span class="fl">Plate Number</span><span class="fv">{{ field(selected.driver1_info.plate_number) }}</span></div>
              <div class="field-row"><span class="fl">State of Reg.</span><span class="fv">{{ field(selected.driver1_info.state_of_reg) }}</span></div>
              <div class="field-row"><span class="fl">Vehicle Year &amp; Make</span><span class="fv">{{ field(selected.driver1_info.vehicle_year_make) }}</span></div>
              <div class="field-row"><span class="fl">Vehicle Type</span><span class="fv">{{ field(selected.driver1_info.vehicle_type) }}</span></div>
              <div class="field-row"><span class="fl">Ins. Code</span><span class="fv">{{ field(selected.driver1_info.ins_code) }}</span></div>
            </div>
          </template>
        </template>

        <!-- 2: Driver 2 -->
        <template v-else-if="activeTab === 2">
          <div v-if="!selected.driver2_info" class="state-msg">No second driver info provided.</div>
          <template v-else>
            <div class="section-title">Driver Info</div>
            <div class="field-grid">
              <div class="field-row"><span class="fl">License ID</span><span class="fv">{{ field(selected.driver2_info.driver_license_id) }}</span></div>
              <div class="field-row"><span class="fl">State of License</span><span class="fv">{{ field(selected.driver2_info.state_of_license) }}</span></div>
              <div class="field-row"><span class="fl">Driver Name</span><span class="fv">{{ field(selected.driver2_info.driver_name) }}</span></div>
              <div class="field-row"><span class="fl">Address</span><span class="fv">{{ field(selected.driver2_info.address) }}</span></div>
              <div class="field-row"><span class="fl">City</span><span class="fv">{{ field(selected.driver2_info.city) }}</span></div>
              <div class="field-row"><span class="fl">State</span><span class="fv">{{ field(selected.driver2_info.state) }}</span></div>
              <div class="field-row"><span class="fl">ZIP</span><span class="fv">{{ field(selected.driver2_info.zip) }}</span></div>
              <div class="field-row"><span class="fl">Date of Birth</span><span class="fv">{{ field(selected.driver2_info.dob) }}</span></div>
              <div class="field-row"><span class="fl">Injuries</span><span class="fv">{{ field(selected.driver2_info.injuries) }}</span></div>
            </div>
            <div class="section-title" style="margin-top:20px">Registrant</div>
            <div class="field-grid">
              <div class="field-row"><span class="fl">Name</span><span class="fv">{{ field(selected.driver2_info.registrant_name) }}</span></div>
              <div class="field-row"><span class="fl">Plate Number</span><span class="fv">{{ field(selected.driver2_info.plate_number) }}</span></div>
              <div class="field-row"><span class="fl">Vehicle Year &amp; Make</span><span class="fv">{{ field(selected.driver2_info.vehicle_year_make) }}</span></div>
              <div class="field-row"><span class="fl">Ins. Code</span><span class="fv">{{ field(selected.driver2_info.ins_code) }}</span></div>
            </div>
          </template>
        </template>

        <!-- 3: Vehicle Damage -->
        <template v-else-if="activeTab === 3">
          <div v-if="!selected.vehicle_damage" class="state-msg">No vehicle damage info provided.</div>
          <template v-else>
            <div class="field-grid">
              <div class="field-row full"><span class="fl">Damage Description</span><span class="fv">{{ field(selected.vehicle_damage.description) }}</span></div>
              <div class="field-row full"><span class="fl">Accident Diagram</span><span class="fv">{{ diagramLabel(selected.vehicle_damage.diagram) }}</span></div>
            </div>
          </template>
        </template>

        <!-- 4: Accident Description -->
        <template v-else-if="activeTab === 4">
          <div v-if="!selected.accident_description" class="state-msg">No accident description provided.</div>
          <p v-else class="description-text">{{ selected.accident_description }}</p>
        </template>

        <!-- 5: Insurance -->
        <template v-else-if="activeTab === 5">
          <div v-if="!selected.insurance_info" class="state-msg">No insurance info provided.</div>
          <template v-else>
            <div class="field-grid">
              <div class="field-row full"><span class="fl">Insurance Company</span><span class="fv">{{ field(selected.insurance_info.insurance_company) }}</span></div>
              <div class="field-row"><span class="fl">VIN</span><span class="fv">{{ field(selected.insurance_info.vin) }}</span></div>
              <div class="field-row"><span class="fl">Policy Number</span><span class="fv">{{ field(selected.insurance_info.policy_number) }}</span></div>
              <div class="field-row"><span class="fl">Policy From</span><span class="fv">{{ field(selected.insurance_info.policy_period_from) }}</span></div>
              <div class="field-row"><span class="fl">Policy To</span><span class="fv">{{ field(selected.insurance_info.policy_period_to) }}</span></div>
            </div>
          </template>
        </template>

        <!-- 6: Other Info -->
        <template v-else-if="activeTab === 6">
          <div v-if="!selected.other_info" class="state-msg">No other info provided.</div>
          <template v-else>
            <div class="field-grid">
              <div class="field-row"><span class="fl">Month</span><span class="fv">{{ field(selected.other_info.month) }}</span></div>
              <div class="field-row"><span class="fl">Day</span><span class="fv">{{ field(selected.other_info.day) }}</span></div>
              <div class="field-row"><span class="fl">Year</span><span class="fv">{{ field(selected.other_info.year) }}</span></div>
              <div class="field-row"><span class="fl">Day of Week</span><span class="fv">{{ field(selected.other_info.day_of_week) }}</span></div>
              <div class="field-row"><span class="fl">Time</span><span class="fv">{{ field(selected.other_info.time) }}</span></div>
              <div class="field-row"><span class="fl">Number of Vehicles</span><span class="fv">{{ field(selected.other_info.num_vehicles) }}</span></div>
              <div class="field-row"><span class="fl">Number Injured</span><span class="fv">{{ field(selected.other_info.num_injured) }}</span></div>
              <div class="field-row"><span class="fl">Number Killed</span><span class="fv">{{ field(selected.other_info.num_killed) }}</span></div>
              <div class="field-row full"><span class="fl">Police Investigated</span><span class="fv">{{ field(selected.other_info.police_investigated) }}</span></div>
            </div>
          </template>
        </template>

        <!-- 7: Signature -->
        <template v-else-if="activeTab === 7">
          <div v-if="!selected.signature_url" class="state-msg">No signature provided yet.</div>
          <template v-else>
            <div class="sig-container">
              <img :src="imgUrl(selected.signature_url)" alt="Signature" class="sig-img" />
              <p v-if="selected.signature_signed_at" class="sig-date">Signed at {{ fmtDateTime(selected.signature_signed_at) }}</p>
            </div>
          </template>
        </template>

      </div>
    </section>
  </div>
</template>

<style scoped>
.accidents-layout {
  display: grid;
  grid-template-columns: 1fr;
  gap: 24px;
}
.accidents-layout.has-detail {
  grid-template-columns: 420px 1fr;
  height: calc(100vh - 180px);
}

/* ── List pane ── */
.list-pane {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.filter-bar {
  display: flex;
  gap: 4px;
  padding: 12px;
  border-bottom: 1px solid var(--border);
  flex-wrap: wrap;
}
.filter-btn {
  padding: 5px 12px;
  border-radius: 6px;
  border: 1px solid var(--border);
  background: transparent;
  cursor: pointer;
  font-size: 13px;
  color: var(--text-muted);
}
.filter-btn.active {
  background: var(--accent-soft);
  color: var(--accent-strong);
  border-color: var(--accent-strong);
}

.accidents-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.accidents-table th {
  padding: 10px 14px;
  text-align: left;
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  color: var(--text-muted);
  border-bottom: 1px solid var(--border);
  background: var(--bg);
}
.accidents-table td { padding: 12px 14px; border-bottom: 1px solid var(--border); vertical-align: top; }
.row { cursor: pointer; }
.row:hover td { background: var(--bg); }
.row.active td { background: var(--accent-soft); }

.reporter-name { font-weight: 500; color: var(--text); }
.reporter-email { font-size: 11px; color: var(--text-muted); margin-top: 2px; }

.status-chip {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 600;
}

/* ── Detail pane ── */
.detail-pane {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.detail-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  padding: 16px 20px;
  border-bottom: 1px solid var(--border);
  gap: 16px;
}
.detail-title { font-weight: 600; font-size: 16px; }
.detail-sub { font-size: 12px; color: var(--text-muted); margin-top: 3px; }

.status-edit { display: flex; gap: 8px; align-items: center; flex-shrink: 0; }
.status-select {
  padding: 6px 10px;
  border: 1px solid var(--border);
  border-radius: 6px;
  font-size: 13px;
  background: var(--bg);
}

.tabs {
  display: flex;
  border-bottom: 1px solid var(--border);
  overflow-x: auto;
  flex-shrink: 0;
}
.tab {
  padding: 10px 14px;
  background: none;
  border: none;
  border-bottom: 2px solid transparent;
  cursor: pointer;
  font-size: 12px;
  font-weight: 500;
  color: var(--text-muted);
  white-space: nowrap;
}
.tab.active { color: var(--accent-strong); border-bottom-color: var(--accent-strong); }

.tab-content {
  flex: 1;
  overflow-y: auto;
  padding: 20px;
}

/* ── Fields ── */
.section-title {
  font-size: 12px;
  font-weight: 700;
  text-transform: uppercase;
  color: var(--text-muted);
  margin-bottom: 10px;
  letter-spacing: 0.05em;
}

.field-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.field-row {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 10px 12px;
  background: var(--bg);
  border-radius: 6px;
}
.field-row.full { grid-column: 1 / -1; }
.fl { font-size: 11px; color: var(--text-muted); font-weight: 500; }
.fv { font-size: 13px; color: var(--text); }

.description-text {
  font-size: 14px;
  line-height: 1.6;
  color: var(--text);
  white-space: pre-wrap;
  padding: 12px;
  background: var(--bg);
  border-radius: 8px;
}

/* ── Attachments ── */
.attach-group { margin-bottom: 20px; }
.attach-label {
  font-size: 11px;
  font-weight: 700;
  text-transform: uppercase;
  color: var(--text-muted);
  margin-bottom: 8px;
  letter-spacing: 0.05em;
}
.attach-grid { display: flex; flex-wrap: wrap; gap: 8px; }
.attach-img { width: 120px; height: 90px; object-fit: cover; border-radius: 8px; border: 1px solid var(--border); }
.attach-video { width: 200px; border-radius: 8px; }
.attach-doc {
  display: flex;
  align-items: center;
  padding: 8px 12px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 8px;
  font-size: 13px;
  text-decoration: none;
  color: var(--accent-strong);
}

/* ── Signature ── */
.sig-container { display: flex; flex-direction: column; align-items: flex-start; gap: 10px; }
.sig-img { max-width: 340px; border: 1px solid var(--border); border-radius: 8px; background: #fff; }
.sig-date { font-size: 12px; color: var(--text-muted); }

/* ── Misc ── */
.state-msg { padding: 32px; text-align: center; color: var(--text-muted); font-size: 14px; }
.btn-primary {
  padding: 7px 16px;
  background: var(--accent-strong);
  color: #fff;
  border: none;
  border-radius: 6px;
  cursor: pointer;
  font-size: 13px;
  font-weight: 500;
}
.btn-primary:disabled { opacity: 0.5; cursor: default; }
</style>
