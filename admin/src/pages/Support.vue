<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import PageHeader from '../components/PageHeader.vue'
import { adminApi } from '../api/admin'
import type { AdminSupportChat, AdminSupportMessage } from '../api/types'
import { useSupportStore } from '../stores/support'
import { useToastStore } from '../stores/toast'
import { fmtDateTime, imgUrl } from '../utils/format'

const toast = useToastStore()
const support = useSupportStore()
const route = useRoute()

// ─── State ───────────────────────────────────────────────────────────────────

const chats = ref<AdminSupportChat[]>([])
const loadingChats = ref(false)
const search = ref('')

const selected = ref<AdminSupportChat | null>(null)
const messages = ref<AdminSupportMessage[]>([])
const loadingMsgs = ref(false)
const draft = ref('')
const sending = ref(false)
const messagesEl = ref<HTMLDivElement | null>(null)

// Attachment preview modal: { url, mime } while open, null while closed.
const preview = ref<{ url: string; mime: string } | null>(null)

// Phone flag (≤640px) drives the single-view chat navigation and the shortened
// composer placeholder. Initialised synchronously so the load-time auto-select
// decision below is right on the first render; kept reactive for rotate/resize.
const narrowMq = window.matchMedia('(max-width: 640px)')
const isNarrow = ref(narrowMq.matches)
function syncNarrow() { isNarrow.value = narrowMq.matches }
onMounted(() => narrowMq.addEventListener('change', syncNarrow))
onUnmounted(() => narrowMq.removeEventListener('change', syncNarrow))

// ─── Derived ─────────────────────────────────────────────────────────────────

const filteredChats = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return chats.value
  return chats.value.filter(c =>
    c.user_name.toLowerCase().includes(q) ||
    c.user_email.toLowerCase().includes(q)
  )
})

const totalUnreadInList = computed(() =>
  chats.value.reduce((acc, c) => acc + c.unread_count, 0)
)

// ─── Load chats ───────────────────────────────────────────────────────────────

async function loadChats() {
  loadingChats.value = true
  try {
    const res = await adminApi.listSupportChats()
    chats.value = res.chats
    support.setTotalUnread(totalUnreadInList.value)
    // Deep-link from the Users page 💬 action: ?chat=<id> preselects that
    // user's chat (GetOrCreate ran first, so the row is in the list even
    // when the conversation is brand new and empty).
    const wanted = typeof route.query.chat === 'string' ? route.query.chat : null
    const target = wanted ? res.chats.find(c => c.id === wanted) : null
    if (target) {
      await selectChat(target)
    } else if (!selected.value && res.chats.length && !isNarrow.value) {
      // Auto-select first chat if none selected — but NOT on a phone, where the
      // single-view model wants the admin to land on the conversation list and
      // choose. A ?chat= deep-link (above) still opens directly on phones.
      await selectChat(res.chats[0])
    }
  } catch (e: any) {
    toast.error(e?.message || 'Failed to load support chats')
  } finally {
    loadingChats.value = false
  }
}

async function selectChat(c: AdminSupportChat) {
  selected.value = c
  loadingMsgs.value = true
  messages.value = []
  try {
    const res = await adminApi.listSupportMessages(c.id)
    messages.value = res.messages
    await scrollToBottom()

    // Mark as read and decrement badges
    if (c.unread_count > 0) {
      const prev = c.unread_count
      const idx = chats.value.findIndex(x => x.id === c.id)
      if (idx !== -1) chats.value[idx] = { ...chats.value[idx], unread_count: 0 }
      selected.value = { ...c, unread_count: 0 }
      support.decrementUnread(prev)
      try { await adminApi.markSupportRead(c.id) } catch { /* silent */ }
    }
  } catch (e: any) {
    toast.error(e?.message || 'Failed to load messages')
  } finally {
    loadingMsgs.value = false
  }
}

// ─── Send ─────────────────────────────────────────────────────────────────────

async function send() {
  const body = draft.value.trim()
  if (!body || !selected.value || sending.value) return
  sending.value = true
  const optimistic: AdminSupportMessage = {
    id: crypto.randomUUID(),
    support_chat_id: selected.value.id,
    sender_id: 'admin',
    sender_kind: 'admin',
    body,
    created_at: new Date().toISOString(),
  }
  messages.value.push(optimistic)
  draft.value = ''
  await scrollToBottom()

  try {
    const saved = await adminApi.sendSupportMessage(selected.value.id, body)
    const idx = messages.value.findIndex(m => m.id === optimistic.id)
    if (idx !== -1) messages.value[idx] = saved
    // Update last_message in list
    updateChatPreview(selected.value.id, saved.body, saved.created_at)
  } catch (e: any) {
    messages.value = messages.value.filter(m => m.id !== optimistic.id)
    toast.error(e?.message || 'Failed to send')
  } finally {
    sending.value = false
  }
}

// ─── Attachments (batch item 2 — the deferred v1.1 composer tail) ────────────
// The upload rides the shared support endpoint (owner-or-admin server-side);
// the response is the saved message, appended directly — the WS fan-out goes
// to the user only, so there is no echo to dedupe.

const ACCEPT_ATTACH = 'image/jpeg,image/png,image/heic,image/heif,application/pdf,video/mp4,video/quicktime'
const MAX_ATTACH_BYTES = 50 * 1024 * 1024 // mirrors the server's MaxBytesReader

const attaching = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)

function pickFile() {
  if (!attaching.value) fileInput.value?.click()
}

async function onFilePicked(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = '' // allow re-picking the same file later
  if (!file || !selected.value || attaching.value) return
  if (file.size > MAX_ATTACH_BYTES) {
    toast.error('File is too large — the limit is 50 MB')
    return
  }
  attaching.value = true
  try {
    const caption = draft.value.trim()
    const saved = await adminApi.uploadSupportAttachment(selected.value.id, file, caption || undefined)
    messages.value.push(saved)
    if (caption) draft.value = ''
    updateChatPreview(selected.value.id, saved.body || '📎 Attachment', saved.created_at)
    await scrollToBottom()
  } catch (err: any) {
    toast.error(err?.message || 'Failed to send attachment')
  } finally {
    attaching.value = false
  }
}

function isVideo(mime: string): boolean {
  return mime.startsWith('video/')
}

// ─── Apply a chat attachment as a vehicle document (batch item 1) ────────────
// The owner sent the corrected paperwork in this chat; the admin grafts it
// onto the right car slot. The server copies the file, replaces older docs
// of that type, verifies the attachment came from the car owner's own chat,
// and notifies the owner.

const DOC_TYPES: { value: string; label: string }[] = [
  { value: 'registration', label: 'Registration' },
  { value: 'inspection', label: 'Vehicle Inspection' },
  { value: 'insurance', label: 'Insurance Certificate' },
  { value: 'title', label: 'Title' },
  { value: 'permit', label: 'Parking Permit' },
]

const applyTarget = ref<{ attachmentId: string } | null>(null)
const applyCars = ref<{ id: string; label: string }[]>([])
const applyCarsLoading = ref(false)
const applyCarId = ref('')
const applyDocType = ref('registration')
const applying = ref(false)

function canApplyAsDocument(mime: string): boolean {
  return mime === 'image/jpeg' || mime === 'image/jpg' || mime === 'image/png' || mime === 'application/pdf'
}

async function openApplyDialog(attachmentId: string) {
  if (!selected.value) return
  applyTarget.value = { attachmentId }
  applyCarId.value = ''
  applyCarsLoading.value = true
  applyCars.value = []
  try {
    // The cars list searches owner email; the server still enforces that
    // the attachment sender owns the chosen car.
    const res = await adminApi.listCars({ query: selected.value.user_email, limit: 50 })
    applyCars.value = (res.items || []).map((c: any) => ({
      id: c.id,
      label: c.title || `${c.year ?? ''} ${c.make ?? ''} ${c.model ?? ''}`.trim() || c.id,
    }))
    if (applyCars.value.length === 1) applyCarId.value = applyCars.value[0].id
  } catch (e: any) {
    toast.error(e?.message || 'Failed to load this user\'s vehicles')
  } finally {
    applyCarsLoading.value = false
  }
}

async function confirmApply() {
  if (!applyTarget.value || !applyCarId.value || applying.value) return
  applying.value = true
  try {
    await adminApi.applyChatAttachmentToCar(applyCarId.value, applyTarget.value.attachmentId, applyDocType.value)
    const label = DOC_TYPES.find(d => d.value === applyDocType.value)?.label || applyDocType.value
    toast.success(`${label} set on the vehicle — the owner was notified`)
    applyTarget.value = null
  } catch (e: any) {
    toast.error(e?.message || 'Failed to set the document')
  } finally {
    applying.value = false
  }
}

// ─── Apply a chat attachment as a DRIVER document (client point 3) ───────────
// Simpler than the vehicle flow: the target user IS the chat owner, so
// there's nothing to select but the slot. The server verifies the
// attachment came from this user's own chat, resets the slot to pending
// review, and notifies them.

const USER_DOC_TYPES: { value: string; label: string }[] = [
  { value: 'drivers_license', label: "Driver's license" },
  { value: 'tlc_license', label: 'TLC license' },
  { value: 'commercial_license', label: 'Commercial license' },
  { value: 'other', label: 'Other document' },
]

// The target user is SNAPSHOTTED at modal-open alongside the attachment —
// `selected` is mutable (the WS deep-link path can switch chats under an
// open modal), and pairing one chat's attachment with another chat's user
// would 403 on the server's WRONG_SENDER guard for an action that looked
// valid when opened. Same snapshot discipline as the vehicle flow's ids.
const applyUserTarget = ref<{ attachmentId: string; userId: string; userName: string } | null>(null)
const applyUserDocType = ref('drivers_license')
const applyingUser = ref(false)

function openApplyUserDialog(attachmentId: string) {
  if (!selected.value) return
  applyUserTarget.value = {
    attachmentId,
    userId: selected.value.user_id,
    userName: selected.value.user_name,
  }
}

async function confirmApplyUser() {
  if (!applyUserTarget.value || applyingUser.value) return
  applyingUser.value = true
  try {
    await adminApi.applyChatAttachmentToUser(applyUserTarget.value.userId, applyUserTarget.value.attachmentId, applyUserDocType.value)
    const label = USER_DOC_TYPES.find(d => d.value === applyUserDocType.value)?.label || applyUserDocType.value
    toast.success(`${label} set — pending review; the user was notified`)
    applyUserTarget.value = null
  } catch (e: any) {
    toast.error(e?.message || 'Failed to set the document')
  } finally {
    applyingUser.value = false
  }
}

// ─── WebSocket ────────────────────────────────────────────────────────────────

const unsubscribe = watch(() => support.lastMessage, (msg) => {
  if (!msg) return
  // Update conversation if it's the open chat
  if (selected.value?.id === msg.support_chat_id) {
    const exists = messages.value.some(m => m.id === msg.id)
    if (!exists) {
      messages.value.push(msg)
      scrollToBottom()
      // Immediately mark read since admin has chat open
      adminApi.markSupportRead(msg.support_chat_id).catch(() => {})
      // Undo the store's increment — admin is actively viewing this chat
      if (msg.sender_kind === 'user') support.decrementUnread(1)
    }
  } else {
    // Bump unread badge on the relevant chat row
    const idx = chats.value.findIndex(c => c.id === msg.support_chat_id)
    if (idx !== -1 && msg.sender_kind === 'user') {
      chats.value[idx] = { ...chats.value[idx], unread_count: chats.value[idx].unread_count + 1 }
    } else if (idx === -1 && msg.sender_kind === 'user') {
      // New chat we haven't seen — reload list to pick it up
      loadChats()
      return
    }
  }
  // Always update the preview in the list
  updateChatPreview(msg.support_chat_id, msg.body, msg.created_at)
})

onUnmounted(() => unsubscribe())

// ─── Helpers ─────────────────────────────────────────────────────────────────

function updateChatPreview(chatId: string, body: string, createdAt: string) {
  const idx = chats.value.findIndex(c => c.id === chatId)
  if (idx === -1) return
  const updated = { ...chats.value[idx], last_message_body: body, last_message_at: createdAt }
  chats.value.splice(idx, 1)
  chats.value.unshift(updated)
  if (selected.value?.id === chatId) selected.value = { ...selected.value, last_message_body: body }
}

async function scrollToBottom() {
  await nextTick()
  if (messagesEl.value) messagesEl.value.scrollTop = messagesEl.value.scrollHeight
}

function fmtRelativeTime(iso?: string | null): string {
  if (!iso) return ''
  const d = new Date(iso)
  const now = new Date()
  const diffMs = now.getTime() - d.getTime()
  const diffMins = Math.floor(diffMs / 60_000)
  if (diffMins < 1) return 'just now'
  if (diffMins < 60) return `${diffMins}m ago`
  const diffH = Math.floor(diffMins / 60)
  if (diffH < 24) return `${diffH}h ago`
  return fmtDateTime(iso)
}

function roleBadge(role: string) {
  return role === 'car_owner' ? 'Owner' : role === 'driver' ? 'Driver' : role
}

function isImage(mime: string) {
  return mime.startsWith('image/')
}

function fmtBytes(n: number): string {
  if (!n) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  const i = Math.min(units.length - 1, Math.floor(Math.log(n) / Math.log(1024)))
  return `${(n / Math.pow(1024, i)).toFixed(i ? 1 : 0)} ${units[i]}`
}

function handleDraftKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    send()
  }
}

// ─── Init ─────────────────────────────────────────────────────────────────────

loadChats()
</script>

<template>
  <PageHeader title="Support chats" />

  <!-- show-detail drives the phone single-view model: with a conversation
       selected the list hides and the conversation goes full-screen (≤640px);
       on desktop both panes always show. -->
  <div class="support-layout" :class="{ 'show-detail': !!selected }">
    <!-- ── Left: Chat list ── -->
    <aside class="chat-list">
      <div class="list-toolbar">
        <div class="search-wrap">
          <svg class="search-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8">
            <circle cx="9" cy="9" r="6" /><path d="M15 15l-3-3" stroke-linecap="round"/>
          </svg>
          <input
            v-model="search"
            class="search-input"
            placeholder="Search users…"
            autocomplete="off"
          />
        </div>
      </div>

      <div v-if="loadingChats" class="state-msg">Loading…</div>
      <div v-else-if="!chats.length" class="state-msg">No support requests yet.</div>
      <div v-else-if="!filteredChats.length" class="state-msg">No results for "{{ search }}"</div>

      <button
        v-for="c in filteredChats"
        :key="c.id"
        class="chat-row"
        :class="{ active: selected?.id === c.id, unread: c.unread_count > 0 }"
        @click="selectChat(c)"
      >
        <div class="chat-avatar">
          <img v-if="c.user_photo_url" :src="imgUrl(c.user_photo_url)" alt="" />
          <div v-else class="avatar-placeholder">
            {{ (c.user_name || c.user_email || '?').charAt(0).toUpperCase() }}
          </div>
          <span v-if="c.unread_count > 0" class="unread-dot">{{ c.unread_count }}</span>
        </div>
        <div class="chat-meta">
          <div class="chat-top">
            <span class="chat-name">{{ c.user_name || c.user_email }}</span>
            <span class="chat-time">{{ fmtRelativeTime(c.last_message_at) }}</span>
          </div>
          <div class="chat-bottom">
            <span class="role-badge">{{ roleBadge(c.user_role) }}</span>
            <span class="chat-preview">{{ c.last_message_body || 'No messages yet' }}</span>
          </div>
        </div>
      </button>
    </aside>

    <!-- ── Right: Conversation ── -->
    <section class="conversation">
      <!-- Empty state -->
      <div v-if="!selected" class="conv-empty">
        <svg viewBox="0 0 48 48" fill="none" stroke="currentColor" stroke-width="1.5" width="56" height="56">
          <path d="M8 12h32a4 4 0 014 4v20a4 4 0 01-4 4H14l-8 6V16a4 4 0 014-4z"/>
        </svg>
        <p>Select a conversation to view messages</p>
      </div>

      <template v-else>
        <!-- Header -->
        <header class="conv-header">
          <button
            v-if="isNarrow"
            type="button"
            class="conv-back"
            aria-label="Back to conversations"
            @click="selected = null"
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="22" height="22">
              <path d="M15 18l-6-6 6-6" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
          </button>
          <div class="conv-avatar">
            <img v-if="selected.user_photo_url" :src="imgUrl(selected.user_photo_url)" alt="" />
            <div v-else class="avatar-placeholder sm">
              {{ (selected.user_name || selected.user_email || '?').charAt(0).toUpperCase() }}
            </div>
          </div>
          <div class="conv-user-info">
            <div class="conv-name">{{ selected.user_name || selected.user_email }}</div>
            <div class="conv-sub">
              <span class="role-badge">{{ roleBadge(selected.user_role) }}</span>
              {{ selected.user_email }}
            </div>
          </div>
        </header>

        <!-- Messages -->
        <div v-if="loadingMsgs" class="state-msg" style="flex:1">Loading messages…</div>
        <div v-else ref="messagesEl" class="messages">
          <div v-if="!messages.length" class="state-msg">No messages yet. Say hello!</div>
          <div
            v-for="m in messages"
            :key="m.id"
            class="msg-row"
            :class="m.sender_kind"
          >
            <div class="msg-bubble">
              <div v-if="m.attachments?.length" class="msg-attachments">
                <template v-for="att in m.attachments" :key="att.id">
                  <button
                    type="button"
                    class="msg-attach"
                    @click="preview = { url: imgUrl(att.file_url) || '', mime: att.mime_type }"
                  >
                    <img v-if="isImage(att.mime_type)" :src="imgUrl(att.file_url)" class="msg-attach-img" />
                    <span v-else class="msg-attach-file">
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="18" height="18">
                        <path d="M14 3H7a2 2 0 00-2 2v14a2 2 0 002 2h10a2 2 0 002-2V8z"/><path d="M14 3v5h5"/>
                      </svg>
                      {{ att.mime_type === 'application/pdf' ? 'PDF' : isVideo(att.mime_type) ? 'Video' : 'File' }} · {{ fmtBytes(att.file_size) }}
                    </span>
                  </button>
                  <!-- Only user-sent images/PDFs can become vehicle documents -->
                  <button
                    v-if="m.sender_kind === 'user' && canApplyAsDocument(att.mime_type)"
                    type="button"
                    class="msg-attach-apply"
                    @click="openApplyDialog(att.id)"
                  >
                    Use as vehicle document…
                  </button>
                  <button
                    v-if="m.sender_kind === 'user' && canApplyAsDocument(att.mime_type)"
                    type="button"
                    class="msg-attach-apply"
                    @click="openApplyUserDialog(att.id)"
                  >
                    Use as driver document…
                  </button>
                </template>
              </div>
              <p v-if="m.body" class="msg-body">{{ m.body }}</p>
              <span class="msg-time">
                {{ m.sender_kind === 'admin' ? 'Support · ' : '' }}{{ fmtDateTime(m.created_at) }}
              </span>
            </div>
          </div>
        </div>

        <!-- Composer -->
        <form class="composer" @submit.prevent="send">
          <input
            ref="fileInput"
            type="file"
            :accept="ACCEPT_ATTACH"
            class="attach-input"
            @change="onFilePicked"
          />
          <button
            type="button"
            class="attach-btn"
            :disabled="attaching || sending"
            :title="attaching ? 'Uploading…' : 'Attach a photo, video or file'"
            aria-label="Attach a file"
            @click="pickFile"
          >
            <svg v-if="!attaching" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" width="20" height="20">
              <path d="M21.44 11.05l-8.49 8.49a5.5 5.5 0 01-7.78-7.78l8.49-8.49a3.5 3.5 0 014.95 4.95l-8.49 8.49a1.5 1.5 0 01-2.12-2.12l7.78-7.78" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
            <span v-else class="attach-spinner" />
          </button>
          <textarea
            v-model="draft"
            rows="1"
            :placeholder="isNarrow ? 'Type a reply…' : 'Type a reply… (Enter to send, Shift+Enter for new line)'"
            :disabled="sending"
            @keydown="handleDraftKeydown"
          />
          <button class="send-btn" type="submit" :disabled="!draft.trim() || sending">
            <svg viewBox="0 0 24 24" fill="currentColor" width="20" height="20">
              <path d="M3.478 2.405a.75.75 0 00-.926.94l2.432 7.905H13.5a.75.75 0 010 1.5H4.984l-2.432 7.905a.75.75 0 00.926.94 60.519 60.519 0 0018.445-8.986.75.75 0 000-1.218A60.517 60.517 0 003.478 2.405z"/>
            </svg>
          </button>
        </form>
      </template>
    </section>

    <!-- Apply a chat attachment as a driver document (client point 3) -->
    <div v-if="applyUserTarget" class="preview-overlay" @click.self="applyUserTarget = null">
      <div class="apply-modal">
        <h3 class="apply-title">Use as driver document</h3>
        <p class="apply-sub">
          Copies this file onto {{ applyUserTarget?.userName || 'the user' }}'s
          account, replacing the current document of that type. It lands as
          <strong>pending review</strong> — approve it from the Users page.
        </p>
        <label class="apply-label">Document slot</label>
        <select v-model="applyUserDocType" class="apply-select">
          <option v-for="d in USER_DOC_TYPES" :key="d.value" :value="d.value">{{ d.label }}</option>
        </select>
        <div class="apply-actions">
          <button type="button" class="ghost" @click="applyUserTarget = null">Cancel</button>
          <button
            type="button"
            class="apply-confirm"
            :disabled="applyingUser"
            @click="confirmApplyUser"
          >
            {{ applyingUser ? 'Setting…' : 'Set document' }}
          </button>
        </div>
      </div>
    </div>

    <!-- Apply a chat attachment as a vehicle document (batch item 1) -->
    <div v-if="applyTarget" class="preview-overlay" @click.self="applyTarget = null">
      <div class="apply-modal">
        <h3 class="apply-title">Use as vehicle document</h3>
        <p class="apply-sub">
          Copies this file onto the owner's car, replacing the current document
          of that type. The owner is notified.
        </p>
        <label class="apply-label">Vehicle</label>
        <select v-model="applyCarId" class="apply-select" :disabled="applyCarsLoading">
          <option value="" disabled>{{ applyCarsLoading ? 'Loading vehicles…' : (applyCars.length ? 'Choose a vehicle' : 'No vehicles found for this user') }}</option>
          <option v-for="c in applyCars" :key="c.id" :value="c.id">{{ c.label }}</option>
        </select>
        <label class="apply-label">Document slot</label>
        <select v-model="applyDocType" class="apply-select">
          <option v-for="d in DOC_TYPES" :key="d.value" :value="d.value">{{ d.label }}</option>
        </select>
        <div class="apply-actions">
          <button type="button" class="ghost" @click="applyTarget = null">Cancel</button>
          <button
            type="button"
            class="apply-confirm"
            :disabled="!applyCarId || applying"
            @click="confirmApply"
          >
            {{ applying ? 'Setting…' : 'Set document' }}
          </button>
        </div>
      </div>
    </div>

    <!-- In-console attachment preview. PDFs render in an embedded frame and
         images inline, so opening one never leaves the console (was a bare
         target=_blank link). "Open in new tab" stays as a fallback. -->
    <div v-if="preview" class="preview-overlay" @click.self="preview = null">
      <div class="preview-modal">
        <div class="preview-bar">
          <a :href="preview.url" target="_blank" rel="noopener" class="preview-link">Open in new tab ↗</a>
          <button type="button" class="preview-close" aria-label="Close preview" @click="preview = null">×</button>
        </div>
        <img v-if="preview.mime.startsWith('image/')" :src="preview.url" class="preview-img" />
        <video v-else-if="preview.mime.startsWith('video/')" :src="preview.url" class="preview-img" controls autoplay />
        <iframe v-else :src="preview.url" class="preview-frame" title="Attachment preview" />
      </div>
    </div>
  </div>
</template>

<style scoped>
/* ── Layout ───────────────────────────────────────────────── */
.support-layout {
  display: grid;
  grid-template-columns: 300px 1fr;
  height: calc(100vh - 180px);
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  overflow: hidden;
}

/* ── Chat list ─────────────────────────────────────────────── */
.chat-list {
  border-right: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  min-height: 0;
}

.list-toolbar {
  padding: 12px;
  border-bottom: 1px solid var(--border);
}

.search-wrap {
  position: relative;
  display: flex;
  align-items: center;
}
.search-icon {
  position: absolute;
  left: 10px;
  width: 16px;
  height: 16px;
  color: var(--text-muted);
  pointer-events: none;
}
.search-input {
  width: 100%;
  padding: 8px 12px 8px 34px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--bg);
  font-size: 13px;
}
.search-input:focus { outline: none; border-color: var(--accent-strong); }

.chat-row {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 12px;
  text-align: left;
  background: transparent;
  border: none;
  border-bottom: 1px solid var(--border);
  cursor: pointer;
  transition: background 120ms;
  width: 100%;
}
.chat-row:hover { background: var(--bg); }
.chat-row.active { background: var(--accent-soft); }
.chat-row.unread .chat-name { font-weight: 700; color: var(--text); }

.chat-avatar { position: relative; flex-shrink: 0; }
.chat-avatar img, .avatar-placeholder {
  width: 40px;
  height: 40px;
  border-radius: 50%;
  object-fit: cover;
}
.avatar-placeholder {
  background: var(--accent-soft);
  color: var(--accent-strong);
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 700;
  font-size: 15px;
}
.avatar-placeholder.sm { width: 36px; height: 36px; font-size: 13px; }

.unread-dot {
  position: absolute;
  top: -3px;
  right: -3px;
  background: #e53e3e;
  color: #fff;
  font-size: 10px;
  font-weight: 700;
  line-height: 1;
  padding: 2px 5px;
  border-radius: 999px;
  min-width: 16px;
  text-align: center;
}

.chat-meta { flex: 1; min-width: 0; }
.chat-top {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  gap: 8px;
  margin-bottom: 3px;
}
.chat-name {
  font-size: 13px;
  font-weight: 500;
  color: var(--text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.chat-time { font-size: 11px; color: var(--text-muted); flex-shrink: 0; }
.chat-bottom { display: flex; align-items: center; gap: 6px; }
.chat-preview {
  font-size: 12px;
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
}

.role-badge {
  display: inline-block;
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 10px;
  font-weight: 600;
  background: var(--accent-soft);
  color: var(--accent-strong);
  white-space: nowrap;
  flex-shrink: 0;
}

/* ── Conversation ──────────────────────────────────────────── */
.conversation {
  display: flex;
  flex-direction: column;
  min-height: 0;
}

.conv-empty {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  color: var(--text-muted);
  gap: 14px;
}
.conv-empty svg { opacity: 0.25; }
.conv-empty p { font-size: 14px; }

.conv-header {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 14px 20px;
  border-bottom: 1px solid var(--border);
  background: var(--surface);
}
.conv-avatar img, .conv-avatar .avatar-placeholder {
  width: 36px;
  height: 36px;
  border-radius: 50%;
  object-fit: cover;
}
.conv-name { font-weight: 600; font-size: 15px; }
.conv-sub {
  font-size: 12px;
  color: var(--text-muted);
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 2px;
}

.messages {
  flex: 1;
  overflow-y: auto;
  padding: 16px 20px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  scroll-behavior: smooth;
}

.msg-row {
  display: flex;
}
.msg-row.user { justify-content: flex-start; }
.msg-row.admin { justify-content: flex-end; }

.msg-bubble {
  max-width: 68%;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.msg-row.user .msg-bubble { align-items: flex-start; }
.msg-row.admin .msg-bubble { align-items: flex-end; }

.msg-body {
  margin: 0;
  padding: 10px 14px;
  border-radius: 16px;
  font-size: 14px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
}
.msg-row.user .msg-body {
  background: var(--bg);
  border: 1px solid var(--border);
  border-bottom-left-radius: 4px;
  color: var(--text);
}
.msg-row.admin .msg-body {
  background: var(--accent-strong);
  color: #fff;
  border-bottom-right-radius: 4px;
}

.msg-time {
  font-size: 11px;
  color: var(--text-muted);
  padding: 0 2px;
}

/* ── In-chat attachments ── */
.msg-attachments {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.msg-attach {
  display: block;
  padding: 0;
  border: none;
  background: none;
  cursor: pointer;
  text-align: left;
}
.msg-attach-img {
  max-width: 220px;
  max-height: 220px;
  border-radius: 12px;
  border: 1px solid var(--border);
  display: block;
}
.msg-attach-file {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 10px 14px;
  border-radius: 12px;
  background: var(--bg);
  border: 1px solid var(--border);
  color: var(--accent-strong);
  font-size: 13px;
  font-weight: 600;
}
.msg-attach-file:hover { border-color: var(--accent-strong); }

/* ── In-console attachment preview modal ── */
.preview-overlay {
  position: fixed;
  inset: 0;
  z-index: 100;
  background: rgba(17, 24, 39, 0.6);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
}
.preview-modal {
  background: var(--surface);
  border-radius: var(--radius);
  width: min(900px, 92vw);
  height: min(90vh, 1000px);
  display: flex;
  flex-direction: column;
  overflow: hidden;
  box-shadow: 0 20px 60px rgba(0, 0, 0, 0.35);
}
.preview-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 14px;
  border-bottom: 1px solid var(--border);
  flex-shrink: 0;
}
.preview-link { font-size: 13px; color: var(--accent-strong); text-decoration: none; }
.preview-link:hover { text-decoration: underline; }
.preview-close {
  width: 32px; height: 32px;
  border: none;
  background: transparent;
  font-size: 22px;
  line-height: 1;
  color: var(--text-muted);
  cursor: pointer;
  border-radius: 6px;
}
.preview-close:hover { background: var(--bg); color: var(--text); }
.preview-frame { flex: 1; width: 100%; border: none; }
.preview-img { flex: 1; width: 100%; object-fit: contain; background: var(--bg); min-height: 0; }

/* ── Composer ─────────────────────────────────────────────── */
.composer {
  display: flex;
  align-items: flex-end;
  gap: 8px;
  padding: 12px 16px;
  border-top: 1px solid var(--border);
  background: var(--surface);
}

.composer textarea {
  flex: 1;
  resize: none;
  min-height: 40px;
  max-height: 120px;
  padding: 10px 14px;
  border: 1px solid var(--border);
  border-radius: 20px;
  background: var(--bg);
  font-size: 14px;
  line-height: 1.4;
  font-family: inherit;
  overflow-y: auto;
  field-sizing: content;
}
.composer textarea:focus { outline: none; border-color: var(--accent-strong); }
.composer textarea:disabled { opacity: 0.5; }

.send-btn {
  width: 40px;
  height: 40px;
  border-radius: 50%;
  background: var(--accent-strong);
  color: #fff;
  border: none;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  transition: opacity 150ms;
}
.send-btn:disabled { opacity: 0.4; cursor: default; }

/* Apply-as-document (batch item 1). */
.msg-attach-apply {
  display: block;
  margin-top: 2px;
  font-size: 12px;
  color: var(--accent-strong);
  background: transparent;
  border: none;
  cursor: pointer;
  padding: 2px 0;
  text-align: left;
}
.msg-attach-apply:hover { text-decoration: underline; }
.apply-modal {
  background: var(--surface, #fff);
  border-radius: 12px;
  padding: 20px;
  width: min(420px, 92vw);
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.apply-title { margin: 0; font-size: 16px; }
.apply-sub { margin: 0 0 8px; font-size: 13px; color: var(--text-muted); }
.apply-label { font-size: 11px; text-transform: uppercase; letter-spacing: 0.04em; color: var(--text-muted); }
.apply-select {
  padding: 8px 10px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: transparent;
  color: var(--text);
  font-size: 14px;
}
.apply-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 12px; }
.apply-confirm {
  padding: 8px 16px;
  border-radius: 8px;
  border: none;
  background: var(--accent-strong);
  color: #fff;
  cursor: pointer;
}
.apply-confirm:disabled { opacity: 0.5; cursor: default; }

/* Attach control (batch item 2): ghost twin of the send button. */
.attach-input { display: none; }
.attach-btn {
  width: 40px;
  height: 40px;
  border-radius: 50%;
  background: transparent;
  color: var(--text-muted);
  border: 1px solid var(--border);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  transition: opacity 150ms, color 150ms;
}
.attach-btn:hover:not(:disabled) { color: var(--accent-strong); }
.attach-btn:disabled { opacity: 0.4; cursor: default; }
.attach-spinner {
  width: 16px;
  height: 16px;
  border: 2px solid var(--border);
  border-top-color: var(--accent-strong);
  border-radius: 50%;
  animation: attach-spin 0.8s linear infinite;
}
@keyframes attach-spin { to { transform: rotate(360deg); } }
.send-btn:not(:disabled):hover { opacity: 0.85; }

/* ── Misc ─────────────────────────────────────────────────── */
.state-msg {
  padding: 32px;
  color: var(--text-muted);
  text-align: center;
  font-size: 14px;
}

/* Back arrow in the conversation header — only rendered on phones (v-if). */
.conv-back {
  display: inline-flex;
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

/* ── Phone: single-view chat (≤640px) ──────────────────────────
   The desktop split (300px list + conversation pane) collapses to one view at
   a time: with nothing selected the list fills the screen; selecting a chat
   hides the list and shows the conversation full-screen (Back arrow returns).
   Desktop (>640px) is untouched. */
@media (max-width: 640px) {
  .support-layout {
    /* minmax(0,1fr) (not 1fr) so the column shrinks to the viewport instead of
       blowing out to a nowrap preview's min-content width. */
    grid-template-columns: minmax(0, 1fr);
    /* dvh accounts for mobile browser chrome; the vh line is the fallback.
       Offset ≈ topbar(52) + main padding(16+24) + page header(~38). */
    height: calc(100vh - 130px);
    height: calc(100dvh - 130px);
  }
  .support-layout:not(.show-detail) .conversation { display: none; }
  .support-layout.show-detail .chat-list { display: none; }

  /* Phone list: each chat is a self-contained bordered CARD (the Users/Rents/
     Vehicles idiom) so the eye separates rows at a glance — a bare divided list
     read as one column. The list view scrolls the page (no fixed height); the
     conversation view keeps its own full-height card + pinned composer. */
  .support-layout:not(.show-detail) {
    background: transparent;
    border: none;
    border-radius: 0;
    height: auto;
    overflow: visible;
  }
  .support-layout:not(.show-detail) .chat-list { border-right: none; overflow: visible; }
  .list-toolbar { padding: 0 0 12px; border-bottom: none; }

  .chat-row {
    align-items: center;
    gap: 12px;
    padding: 14px;
    margin-bottom: 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }
  .chat-row:last-child { margin-bottom: 0; }
  .chat-row:hover { background: var(--surface); }
  .chat-row.active { border-color: var(--accent-strong); background: var(--accent-soft); }

  /* Normalise the avatar so the left edge is clean and scannable. */
  .chat-avatar img, .chat-avatar .avatar-placeholder { width: 44px; height: 44px; }

  /* Three distinct levels: name + time, the badge on its own line, then the
     preview — so the badge no longer touches the preview text. */
  .chat-meta { display: flex; flex-direction: column; gap: 5px; }
  .chat-top { margin-bottom: 0; }
  .chat-name { font-size: 15px; }
  .chat-time { font-size: 12px; color: var(--text-subtle); }
  .chat-bottom { flex-direction: column; align-items: flex-start; gap: 5px; }
  .chat-preview { font-size: 13px; }

  .conv-header { padding: 12px 12px; }
  .msg-bubble { max-width: 85%; }

  .composer {
    padding: 10px 12px;
    /* Clear the home-indicator safe area so the pinned composer sits above it. */
    padding-bottom: max(10px, env(safe-area-inset-bottom));
  }
  .send-btn { width: 44px; height: 44px; }
}
</style>
