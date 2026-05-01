<script setup lang="ts">
import { ref, watch } from 'vue'
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
    if (!selected.value && res.items.length) selectChat(res.items[0])
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
  try {
    const res = await adminApi.listChatMessages(chat.id)
    messages.value = res.messages
  } catch (e: any) {
    toast.error(e?.message || 'Failed to load messages')
  } finally {
    loadingMsgs.value = false
  }
}

loadChats()
</script>

<template>
  <PageHeader title="Request Chats" />

  <div class="filters">
    <input v-model="query" placeholder="Search by participant email/name, car title, or chat ID…" class="search" />
    <span class="count">{{ total }} chats</span>
  </div>

  <div class="split">
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
          :class="{ system: m.type === 'system', driver: m.sender_id === selected.driver_id }"
        >
          <div class="msg-head">
            <span class="sender">{{ m.sender_name || (m.sender_id === selected.driver_id ? 'Driver' : 'Owner') }}</span>
            <span class="when">{{ fmtDateTime(m.created_at) }}</span>
          </div>
          <div class="body">{{ m.body }}</div>
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
.msg-head { display: flex; gap: 8px; font-size: 11px; color: var(--text-muted); margin-bottom: 4px; }
.sender { font-weight: 500; }
.body { white-space: pre-wrap; }

.state { padding: 32px; color: var(--text-muted); text-align: center; }
</style>
