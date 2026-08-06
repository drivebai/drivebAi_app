<script setup lang="ts">
import { ref, watch, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import PageHeader from '../components/PageHeader.vue'
import { adminApi } from '../api/admin'
import type { AdminTicket, TicketAttachment, TicketCategory, TicketStatus } from '../api/types'
import { useToastStore } from '../stores/toast'
import { useTicketsStore } from '../stores/tickets'
import { fmtDateTime, imgUrl } from '../utils/format'

const toast = useToastStore()
const tickets = useTicketsStore()
const router = useRouter()

// ── State ─────────────────────────────────────────────────────────────────────

const rows = ref<AdminTicket[]>([])
const loading = ref(false)
// Default to the open queue — the whole point is "what needs a human now",
// oldest first (the backend orders by submitted_at ASC).
const statusFilter = ref<'' | TicketStatus>('open')
const selected = ref<AdminTicket | null>(null)
const detailLoading = ref(false)
const savingStatus = ref(false)
const editStatus = ref<string>('')
const openingChat = ref(false)

// ── Labels / colors ────────────────────────────────────────────────────────────
// Category labels mirror the backend categoryLabel() so the queue reads the
// same language the user picked. Status lifecycle: open → resolved → closed
// (drafts never reach the queue).

const CATEGORY_LABELS: Record<TicketCategory, string> = {
  account: 'Account & verification',
  listing: 'Listing a car',
  renting: 'Renting a car',
  buy_sell: 'Buying or selling a car',
  payments: 'Payments & refunds',
  other: 'Other',
}

const STATUS_LABELS: Record<string, string> = {
  draft: 'Draft', open: 'Open', resolved: 'Resolved', closed: 'Closed',
}
const STATUS_COLORS: Record<string, string> = {
  draft: '#a0aec0', open: '#3182ce', resolved: '#38a169', closed: '#718096',
}

const FILTERS: Record<string, string> = {
  open: 'Open', resolved: 'Resolved', closed: 'Closed', '': 'All',
}

// ── Load ──────────────────────────────────────────────────────────────────────

async function load() {
  loading.value = true
  try {
    const res = await adminApi.listTickets({ status: statusFilter.value || undefined, limit: 100 })
    rows.value = res.items
  } catch (e: any) {
    toast.error(e?.message || 'Failed to load requests')
  } finally {
    loading.value = false
  }
}

async function select(t: AdminTicket) {
  detailLoading.value = true
  selected.value = t
  editStatus.value = t.status
  try {
    const full = await adminApi.getTicket(t.id)
    selected.value = full
    editStatus.value = full.status
    const idx = rows.value.findIndex(x => x.id === t.id)
    if (idx !== -1) rows.value[idx] = full
  } catch {
    // keep the list row we already have
  } finally {
    detailLoading.value = false
  }
}

async function saveStatus() {
  if (!selected.value || savingStatus.value) return
  if (editStatus.value === selected.value.status) return
  savingStatus.value = true
  try {
    await adminApi.updateTicketStatus(selected.value.id, editStatus.value as 'open' | 'resolved' | 'closed')
    const next = editStatus.value as TicketStatus
    selected.value = { ...selected.value, status: next }
    const idx = rows.value.findIndex(x => x.id === selected.value!.id)
    if (idx !== -1) {
      // If it no longer matches the active filter, drop it from the visible list.
      if (statusFilter.value && statusFilter.value !== next) {
        rows.value.splice(idx, 1)
      } else {
        rows.value[idx] = { ...rows.value[idx], status: next }
      }
    }
    // Keep the sidebar open-count honest (open↔resolved/closed changes it).
    tickets.refreshOpenCount()
    toast.success('Status updated — the user was notified')
  } catch (e: any) {
    toast.error(e?.message || 'Failed to update status')
  } finally {
    savingStatus.value = false
  }
}

// Jump to the user's ONE support chat — the same thread their in-app support
// button uses — with it preselected. Tickets and the chat are linked by user,
// not merged, so a reply happens in the conversation the user already knows.
async function openChatWithUser() {
  if (!selected.value || openingChat.value) return
  openingChat.value = true
  try {
    const res = await adminApi.openSupportChat(selected.value.user_id)
    router.push({ name: 'support', query: { chat: res.chat_id } })
  } catch (e: any) {
    toast.error(e?.message || 'Failed to open chat')
  } finally {
    openingChat.value = false
  }
}

watch(statusFilter, load)
onMounted(load)

// ── Helpers ───────────────────────────────────────────────────────────────────

function categoryLabel(c: TicketCategory) { return CATEGORY_LABELS[c] || 'Request' }
function isImage(mime: string) { return mime.startsWith('image/') }
function roleBadge(role: string) {
  return role === 'car_owner' ? 'Owner' : role === 'driver' ? 'Driver' : role
}
function attachName(a: TicketAttachment) {
  return isImage(a.mime_type) ? 'Photo' : a.mime_type === 'application/pdf' ? 'PDF' : 'File'
}
</script>

<template>
  <PageHeader title="Requests" />

  <div class="tickets-layout" :class="{ 'has-detail': !!selected }">
    <!-- ── List ── -->
    <section class="list-pane">
      <div class="filter-bar">
        <button
          v-for="(label, val) in FILTERS"
          :key="val"
          class="filter-btn"
          :class="{ active: statusFilter === val }"
          @click="statusFilter = val as any"
        >{{ label }}</button>
      </div>

      <div v-if="loading" class="state-msg">Loading…</div>
      <div v-else-if="!rows.length" class="state-msg">
        {{ statusFilter === 'open' ? 'No open requests. All caught up.' : 'No requests here.' }}
      </div>

      <template v-else>
        <!-- Desktop: table -->
        <table class="tickets-table desktop-only">
          <thead>
            <tr>
              <th>Submitted</th>
              <th>Reporter</th>
              <th>Category</th>
              <th>Status</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="t in rows"
              :key="t.id"
              class="row"
              :class="{ active: selected?.id === t.id }"
              @click="select(t)"
            >
              <td>{{ fmtDateTime(t.submitted_at || t.created_at) }}</td>
              <td>
                <div class="reporter-name">{{ t.user_name }}</div>
                <div class="reporter-email">{{ t.user_email }}</div>
              </td>
              <td>{{ categoryLabel(t.category) }}</td>
              <td>
                <span class="status-chip" :style="{ background: STATUS_COLORS[t.status] + '22', color: STATUS_COLORS[t.status] }">
                  {{ STATUS_LABELS[t.status] }}
                </span>
              </td>
            </tr>
          </tbody>
        </table>

        <!-- Phone: stacked cards (same rhythm as the Support/Chats lists) -->
        <div class="mobile-cards">
          <button
            v-for="t in rows"
            :key="t.id"
            class="list-card"
            :class="{ active: selected?.id === t.id }"
            @click="select(t)"
          >
            <div class="lc-top">
              <span class="lc-title">{{ categoryLabel(t.category) }}</span>
              <span class="status-chip" :style="{ background: STATUS_COLORS[t.status] + '22', color: STATUS_COLORS[t.status] }">
                {{ STATUS_LABELS[t.status] }}
              </span>
            </div>
            <div class="lc-name">{{ t.user_name }}</div>
            <div class="lc-sub">{{ t.user_email }} · {{ fmtDateTime(t.submitted_at || t.created_at) }}</div>
          </button>
        </div>
      </template>
    </section>

    <!-- ── Detail ── -->
    <section v-if="selected" class="detail-pane">
      <div class="detail-header">
        <button type="button" class="detail-back" aria-label="Back to requests" @click="selected = null">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="22" height="22">
            <path d="M15 18l-6-6 6-6" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
        </button>
        <div>
          <div class="detail-title">
            {{ categoryLabel(selected.category) }}
            <span class="role-badge">{{ roleBadge(selected.user_role) }}</span>
          </div>
          <div class="detail-sub">
            {{ selected.user_name }} · {{ selected.user_email }} ·
            Submitted {{ fmtDateTime(selected.submitted_at || selected.created_at) }}
          </div>
        </div>
        <div class="status-edit">
          <select v-model="editStatus" class="status-select">
            <option value="open">Open</option>
            <option value="resolved">Resolved</option>
            <option value="closed">Closed</option>
          </select>
          <button class="btn-primary" :disabled="savingStatus || editStatus === selected.status" @click="saveStatus">
            {{ savingStatus ? 'Saving…' : 'Save' }}
          </button>
        </div>
      </div>

      <div v-if="detailLoading" class="state-msg">Loading details…</div>
      <div v-else class="detail-body">
        <button class="chat-cta" :disabled="openingChat" @click="openChatWithUser">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16">
            <path d="M21 12a8 8 0 11-3-6L21 4l-1 5a8 8 0 011 3z"/>
          </svg>
          {{ openingChat ? 'Opening…' : 'Open chat with user' }}
        </button>

        <div v-if="selected.subject" class="section-title">Subject</div>
        <p v-if="selected.subject" class="body-text">{{ selected.subject }}</p>

        <div class="section-title">What's happening</div>
        <p class="body-text">{{ selected.description || '—' }}</p>

        <template v-if="selected.attachments?.length">
          <div class="section-title">Evidence ({{ selected.attachments.length }})</div>
          <div class="attach-grid">
            <a
              v-for="att in selected.attachments"
              :key="att.id"
              :href="imgUrl(att.file_url)"
              target="_blank"
              rel="noopener"
              class="attach-item"
            >
              <img v-if="isImage(att.mime_type)" :src="imgUrl(att.file_url)" class="attach-img" />
              <div v-else class="attach-doc">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="22" height="22">
                  <path d="M14 3H7a2 2 0 00-2 2v14a2 2 0 002 2h10a2 2 0 002-2V8z"/><path d="M14 3v5h5"/>
                </svg>
                <span>{{ attachName(att) }}</span>
              </div>
            </a>
          </div>
        </template>

        <div class="section-title">Timeline</div>
        <div class="timeline">
          <div class="tl-row"><span class="tl-label">Submitted</span><span class="tl-val">{{ fmtDateTime(selected.submitted_at || selected.created_at) }}</span></div>
          <div v-if="selected.resolved_at" class="tl-row"><span class="tl-label">Resolved</span><span class="tl-val">{{ fmtDateTime(selected.resolved_at) }}</span></div>
          <div v-if="selected.closed_at" class="tl-row"><span class="tl-label">Closed</span><span class="tl-val">{{ fmtDateTime(selected.closed_at) }}</span></div>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.tickets-layout {
  display: grid;
  grid-template-columns: 1fr;
  gap: 24px;
}
.tickets-layout.has-detail {
  grid-template-columns: 440px 1fr;
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

.tickets-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.tickets-table th {
  padding: 10px 14px;
  text-align: left;
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  color: var(--text-muted);
  border-bottom: 1px solid var(--border);
  background: var(--bg);
}
.tickets-table td { padding: 12px 14px; border-bottom: 1px solid var(--border); vertical-align: top; }
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
.detail-title { font-weight: 600; font-size: 16px; display: flex; align-items: center; gap: 8px; }
.detail-sub { font-size: 12px; color: var(--text-muted); margin-top: 3px; }

.role-badge {
  display: inline-block;
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 10px;
  font-weight: 600;
  background: var(--accent-soft);
  color: var(--accent-strong);
  white-space: nowrap;
}

.status-edit { display: flex; gap: 8px; align-items: center; flex-shrink: 0; }
.status-select {
  padding: 6px 10px;
  border: 1px solid var(--border);
  border-radius: 6px;
  font-size: 13px;
  background: var(--bg);
}

.detail-body {
  flex: 1;
  overflow-y: auto;
  padding: 20px;
}

.chat-cta {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 8px 14px;
  margin-bottom: 20px;
  background: var(--accent-strong);
  color: #fff;
  border: none;
  border-radius: 8px;
  cursor: pointer;
  font-size: 13px;
  font-weight: 600;
}
.chat-cta:disabled { opacity: 0.6; cursor: default; }

.section-title {
  font-size: 12px;
  font-weight: 700;
  text-transform: uppercase;
  color: var(--text-muted);
  margin: 18px 0 8px;
  letter-spacing: 0.05em;
}
.section-title:first-of-type { margin-top: 0; }

.body-text {
  font-size: 14px;
  line-height: 1.6;
  color: var(--text);
  white-space: pre-wrap;
  word-break: break-word;
  margin: 0;
  padding: 12px;
  background: var(--bg);
  border-radius: 8px;
}

/* ── Attachments ── */
.attach-grid { display: flex; flex-wrap: wrap; gap: 8px; }
.attach-item { display: block; text-decoration: none; }
.attach-img {
  width: 120px; height: 90px;
  object-fit: cover;
  border-radius: 8px;
  border: 1px solid var(--border);
  display: block;
}
.attach-doc {
  width: 120px; height: 90px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 8px;
  color: var(--accent-strong);
  font-size: 12px;
  font-weight: 600;
}

/* ── Timeline ── */
.timeline {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.tl-row {
  display: flex;
  justify-content: space-between;
  padding: 8px 12px;
  background: var(--bg);
  border-radius: 6px;
  font-size: 13px;
}
.tl-label { color: var(--text-muted); font-weight: 500; }
.tl-val { color: var(--text); }

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

/* Mobile-only stacked card list + the detail Back arrow are hidden on desktop. */
.mobile-cards { display: none; }
.detail-back {
  display: none;
  align-items: center;
  justify-content: center;
  min-width: 44px;
  min-height: 44px;
  margin-left: -8px;
  border: none;
  background: transparent;
  color: var(--text);
  cursor: pointer;
  flex-shrink: 0;
}

/* ── Phone: single-view (≤640px) ───────────────────────────────
   The 440px+1fr detail overflowed the screen. Now: full-width list of stacked
   cards (shared :root --m-row-* rhythm) → tap → full-screen detail with a Back
   arrow, evidence + status control stacked. Natural page scroll (not a chat).
   Desktop (>640px) untouched. */
@media (max-width: 640px) {
  .tickets-layout,
  .tickets-layout.has-detail {
    grid-template-columns: 1fr;
    height: auto;
    gap: 0;
  }
  .tickets-layout.has-detail .list-pane { display: none; }
  /* min-width:0 lets the single grid column shrink to the viewport instead of
     blowing out to a child's min-content width (long email / wide content). */
  .list-pane, .detail-pane { overflow: visible; min-width: 0; }
  .detail-body { overflow-y: visible; }

  .desktop-only { display: none; }
  .mobile-cards { display: block; }
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
  .list-card.active { background: var(--accent-soft); }
  .lc-top { display: flex; justify-content: space-between; align-items: center; gap: 8px; margin-bottom: 5px; }
  .lc-title { font-weight: 600; font-size: 15px; }
  .lc-name { font-size: 14px; color: var(--text); }
  .lc-sub { font-size: 12px; color: var(--text-muted); margin-top: 2px; }

  /* Filter pills to a real tap target. */
  .filter-btn { min-height: 40px; padding: 8px 14px; }

  /* Detail header: Back + title share the first row (left-aligned — the desktop
     space-between would otherwise shove the title off-screen); the status
     control drops to its own full-width row below. */
  .detail-back { display: inline-flex; }
  .detail-header { flex-wrap: wrap; align-items: center; justify-content: flex-start; gap: 8px 12px; padding: 12px 14px; }
  .detail-header > div:first-of-type { flex: 1; min-width: 0; }
  .detail-sub { overflow-wrap: anywhere; }
  .status-edit { flex-basis: 100%; }
  .status-select { flex: 1; }
  .btn-primary { min-height: 40px; }
}
</style>
