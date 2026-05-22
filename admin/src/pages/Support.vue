<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import { adminApi } from '../api/admin'
import type { AdminSupportChat, AdminSupportMessage } from '../api/types'
import { useSupportStore } from '../stores/support'
import { useToastStore } from '../stores/toast'
import { fmtDateTime, imgUrl } from '../utils/format'

const toast = useToastStore()
const support = useSupportStore()

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
    // Auto-select first chat if none selected
    if (!selected.value && res.chats.length) await selectChat(res.chats[0])
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

  <div class="support-layout">
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
              <p class="msg-body">{{ m.body }}</p>
              <span class="msg-time">
                {{ m.sender_kind === 'admin' ? 'Support · ' : '' }}{{ fmtDateTime(m.created_at) }}
              </span>
            </div>
          </div>
        </div>

        <!-- Composer -->
        <form class="composer" @submit.prevent="send">
          <textarea
            v-model="draft"
            rows="1"
            placeholder="Type a reply… (Enter to send, Shift+Enter for new line)"
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
.send-btn:not(:disabled):hover { opacity: 0.85; }

/* ── Misc ─────────────────────────────────────────────────── */
.state-msg {
  padding: 32px;
  color: var(--text-muted);
  text-align: center;
  font-size: 14px;
}
</style>
