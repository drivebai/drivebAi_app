<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import { adminApi } from '../api/admin'
import type { AdminChat, AdminMessage } from '../api/types'
import { useToastStore } from '../stores/toast'
import { fmtDateTime, imgUrl } from '../utils/format'

const toast = useToastStore()

const chats = ref<AdminChat[]>([])
const total = ref(0)
const page = ref(1)
const limit = 50
const loadingChats = ref(false)
const query = ref('')

const selected = ref<AdminChat | null>(null)
const messages = ref<AdminMessage[]>([])
const loadingMsgs = ref(false)
const newMessage = ref('')
const sending = ref(false)

// Phone flag (≤640px) drives the single-view navigation, same as Support.
// Initialised synchronously so the load-time auto-select decision is right.
const narrowMq = window.matchMedia('(max-width: 640px)')
const isNarrow = ref(narrowMq.matches)
function syncNarrow() { isNarrow.value = narrowMq.matches }
onMounted(() => narrowMq.addEventListener('change', syncNarrow))
onUnmounted(() => narrowMq.removeEventListener('change', syncNarrow))

let timer: number | undefined
watch(query, () => {
  if (timer) clearTimeout(timer)
  timer = window.setTimeout(() => { page.value = 1; loadChats() }, 250)
})

async function loadChats() {
  loadingChats.value = true
  try {
    const res = await adminApi.listChats({ query: query.value, page: page.value, limit })
    chats.value = res.items
    total.value = res.total
    // Don't auto-open a chat on phones — the single-view model wants the admin
    // to land on the list and choose.
    if (!selected.value && res.items.length && !isNarrow.value) selectChat(res.items[0])
  } catch (e: any) {
    toast.error(e?.message || 'Failed to load chats')
  } finally {
    loadingChats.value = false
  }
}

async function selectChat(chat: AdminChat) {
  selected.value = chat
  loadingMsgs.value = true
  messages.value = []
  newMessage.value = ''
  try {
    const res = await adminApi.listChatMessages(chat.id)
    messages.value = res.messages
  } catch (e: any) {
    toast.error(e?.message || 'Failed to load messages')
  } finally {
    loadingMsgs.value = false
  }
}

async function sendMessage() {
  const text = newMessage.value.trim()
  if (!text || !selected.value || sending.value) return
  sending.value = true
  try {
    const msg = await adminApi.sendChatMessage(selected.value.id, text)
    messages.value.push(msg)
    newMessage.value = ''
  } catch (e: any) {
    toast.error(e?.message || 'Failed to send message')
  } finally {
    sending.value = false
  }
}

function onComposerKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    sendMessage()
  }
}

loadChats()
</script>

<template>
  <PageHeader title="Request Chats" />

  <div class="filters" :class="{ 'hide-when-detail': !!selected }">
    <input v-model="query" placeholder="Search by participant email/name, car title, or chat ID…" class="search" />
    <span class="count">{{ total }} chats</span>
  </div>

  <!-- show-detail drives the phone single-view: with a chat selected the list
       hides and the conversation goes full-screen (≤640px); desktop keeps both. -->
  <div class="split" :class="{ 'show-detail': !!selected }">
    <aside class="list">
      <div v-if="loadingChats" class="state">Loading…</div>
      <div v-else-if="!chats.length" class="state">No chats found.</div>
      <button
        v-for="c in chats" :key="c.id"
        class="chat-row"
        :class="{ active: selected?.id === c.id }"
        @click="selectChat(c)"
      >
        <img v-if="c.cover_photo_url" :src="imgUrl(c.cover_photo_url)" alt="" class="thumb" />
        <div v-else class="thumb thumb-placeholder" />
        <div class="meta">
          <div class="title">{{ c.car_title }} {{ c.car_year }}</div>
          <div class="sub">{{ c.driver_email }} · {{ c.owner_email }}</div>
          <div v-if="c.last_message_body" class="preview">{{ c.last_message_body }}</div>
        </div>
      </button>
    </aside>

    <section class="convo">
      <header v-if="selected" class="convo-header">
        <button
          v-if="isNarrow"
          type="button"
          class="convo-back"
          aria-label="Back to chats"
          @click="selected = null"
        >
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="22" height="22">
            <path d="M15 18l-6-6 6-6" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
        </button>
        <img v-if="selected.cover_photo_url" :src="imgUrl(selected.cover_photo_url)" alt="" class="thumb sm" />
        <div>
          <div class="convo-title">{{ selected.car_title }} {{ selected.car_year }}</div>
          <div class="convo-sub">
            Driver: <strong>{{ selected.driver_name || selected.driver_email }}</strong>
            · Owner: <strong>{{ selected.owner_name || selected.owner_email }}</strong>
          </div>
        </div>
      </header>

      <div v-if="!selected" class="state">Select a chat to view messages.</div>
      <div v-else-if="loadingMsgs" class="state">Loading messages…</div>
      <div v-else class="messages">
        <div v-if="!messages.length" class="state">No messages yet.</div>
        <div
          v-for="m in messages" :key="m.id"
          class="msg"
          :class="{
            system: m.type === 'system',
            driver: m.sender_id === selected.driver_id && m.sender_kind !== 'admin',
            admin: m.sender_kind === 'admin'
          }"
        >
          <div v-if="m.sender_kind === 'admin'" class="admin-badge">ADMIN</div>
          <div class="msg-head">
            <span class="sender">{{ m.sender_name || (m.sender_id === selected.driver_id ? 'Driver' : 'Owner') }}</span>
            <span class="when">{{ fmtDateTime(m.created_at) }}</span>
          </div>
          <div class="body">{{ m.body }}</div>
        </div>
      </div>

      <div v-if="selected" class="composer">
        <div class="composer-warning">⚠ Users will see this as an Admin message</div>
        <div class="composer-row">
          <textarea
            v-model="newMessage"
            placeholder="Send as Admin…"
            rows="2"
            class="composer-input"
            @keydown="onComposerKeydown"
          />
          <button class="composer-send" :disabled="!newMessage.trim() || sending" @click="sendMessage">
            {{ sending ? '…' : 'Send' }}
          </button>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.filters {
  display: flex; align-items: center; gap: 16px;
  margin-bottom: 16px;
}
.search { flex: 1; max-width: 480px; }
.count { color: var(--text-muted); font-size: 13px; }

.split {
  display: grid;
  grid-template-columns: 360px 1fr;
  gap: 16px;
  height: calc(100vh - 200px);
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  overflow: hidden;
}

.list {
  border-right: 1px solid var(--border);
  overflow-y: auto;
  padding: 8px;
}
.chat-row {
  display: flex; gap: 12px; align-items: flex-start;
  width: 100%; text-align: left; padding: 10px;
  background: transparent; border: none; border-radius: var(--radius);
  cursor: pointer;
}
.chat-row:hover { background: var(--bg); }
.chat-row.active { background: var(--accent-soft); }
.thumb { width: 48px; height: 48px; border-radius: 8px; object-fit: cover; border: 1px solid var(--border); flex-shrink: 0; }
.thumb.sm { width: 36px; height: 36px; border-radius: 6px; }
.thumb-placeholder { background: var(--bg); }
.meta { min-width: 0; flex: 1; }
.title { font-weight: 500; }
.sub { color: var(--text-muted); font-size: 12px; }
.preview { color: var(--text-muted); font-size: 13px; margin-top: 4px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.convo {
  display: flex; flex-direction: column;
  min-width: 0;
}
.convo-header {
  display: flex; align-items: center; gap: 12px;
  padding: 16px 20px;
  border-bottom: 1px solid var(--border);
}
.convo-title { font-weight: 600; }
.convo-sub { color: var(--text-muted); font-size: 13px; }

.messages {
  flex: 1; overflow-y: auto;
  padding: 16px 20px;
  display: flex; flex-direction: column; gap: 12px;
}
.msg {
  background: var(--bg);
  padding: 10px 14px;
  border-radius: 10px;
  max-width: 70%;
  align-self: flex-start;
}
.msg.driver { background: var(--accent-soft); align-self: flex-end; }
.msg.system { align-self: center; background: transparent; color: var(--text-muted); font-style: italic; }
.msg.admin { background: #f3e8ff; border: 1px solid #c084fc; align-self: flex-start; }
.admin-badge {
  display: inline-block;
  font-size: 10px; font-weight: 700; letter-spacing: 0.05em;
  color: #fff; background: #9333ea;
  border-radius: 999px; padding: 2px 8px;
  margin-bottom: 4px;
}
.msg-head { display: flex; gap: 8px; font-size: 11px; color: var(--text-muted); margin-bottom: 4px; }
.sender { font-weight: 500; }
.body { white-space: pre-wrap; }

.composer {
  border-top: 1px solid var(--border);
  padding: 10px 16px 12px;
  display: flex; flex-direction: column; gap: 6px;
}
.composer-warning {
  font-size: 11px; color: #92400e;
  background: #fef3c7; border: 1px solid #fcd34d;
  border-radius: 6px; padding: 4px 10px;
}
.composer-row { display: flex; gap: 8px; align-items: flex-end; }
.composer-input {
  flex: 1; resize: none;
  border: 1px solid var(--border); border-radius: var(--radius);
  padding: 8px 10px; font-size: 14px;
  background: var(--bg); color: var(--text);
}
.composer-send {
  padding: 8px 18px; font-weight: 600;
  background: #9333ea; color: #fff;
  border: none; border-radius: var(--radius); cursor: pointer;
  white-space: nowrap;
}
.composer-send:disabled { opacity: 0.5; cursor: not-allowed; }

.state { padding: 32px; color: var(--text-muted); text-align: center; }

/* Back arrow in the conversation header — only rendered on phones (v-if). */
.convo-back {
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

/* ── Phone: single-view (≤640px) ───────────────────────────────
   Same model as Support: the 360px+1fr split (which collapsed the message pane
   to ~0 on a phone) becomes one view at a time — list full-screen, tap → chat
   full-screen with a Back arrow. Shared list rhythm (:root --m-row-*) so it
   matches Support/Tickets/Accidents. Desktop (>640px) is untouched. */
@media (max-width: 640px) {
  .filters.hide-when-detail { display: none; }

  .split {
    /* minmax(0,1fr) (not 1fr) so the column shrinks to the viewport instead of
       blowing out to a nowrap preview's min-content width. */
    grid-template-columns: minmax(0, 1fr);
    /* list view still shows the filters row, so a bit more offset */
    height: calc(100vh - 188px);
    height: calc(100dvh - 188px);
  }
  .split.show-detail {
    /* conversation view hides the filters → reclaim that height */
    height: calc(100vh - 132px);
    height: calc(100dvh - 132px);
  }
  .split:not(.show-detail) .convo { display: none; }
  .split.show-detail .list { display: none; }

  /* List: each chat is a self-contained bordered CARD (matching Support + the
     other list pages) so the eye separates rows at a glance. The list view
     scrolls the page; the conversation view keeps its own full-height card. */
  .split:not(.show-detail) {
    background: transparent;
    border: none;
    border-radius: 0;
    height: auto;
    overflow: visible;
  }
  .split:not(.show-detail) .list { border-right: none; overflow: visible; padding: 0; }
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
  /* Three distinct levels with breathing room: car title, participants, preview. */
  .meta { display: flex; flex-direction: column; gap: 4px; }
  .title { font-size: 15px; font-weight: 600; }
  .sub { font-size: 13px; }
  .preview { font-size: 13px; margin-top: 0; }

  /* Conversation header keeps naming car + driver + owner; let it wrap. */
  .convo-header { padding: 12px 12px; gap: 10px; }
  .convo-sub { font-size: 13px; }

  .msg { max-width: 85%; }
  /* Keep the ADMIN badge legible at phone width. */
  .admin-badge { font-size: 11px; padding: 3px 9px; }

  .composer {
    padding: 10px 12px;
    padding-bottom: max(10px, env(safe-area-inset-bottom));
  }
  /* Full-width strip that reads as a sentence, not a vertical ribbon. */
  .composer-warning { font-size: 12px; padding: 6px 12px; }
  .composer-send { min-height: 44px; }
}
</style>
