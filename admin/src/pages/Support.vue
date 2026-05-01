<script setup lang="ts">
import { nextTick, ref } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import { adminApi } from '../api/admin'
import type { AdminSupportChat, AdminSupportMessage } from '../api/types'
import { useToastStore } from '../stores/toast'
import { fmtDateTime, imgUrl } from '../utils/format'

const toast = useToastStore()

const chats = ref<AdminSupportChat[]>([])
const loadingChats = ref(false)

const selected = ref<AdminSupportChat | null>(null)
const messages = ref<AdminSupportMessage[]>([])
const loadingMsgs = ref(false)
const draft = ref('')
const sending = ref(false)
const messagesEl = ref<HTMLDivElement | null>(null)

async function loadChats() {
  loadingChats.value = true
  try {
    const res = await adminApi.listSupportChats()
    chats.value = res.chats
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
    await nextTick()
    if (messagesEl.value) messagesEl.value.scrollTop = messagesEl.value.scrollHeight
  } catch (e: any) {
    toast.error(e?.message || 'Failed to load messages')
  } finally {
    loadingMsgs.value = false
  }
}

async function send() {
  const body = draft.value.trim()
  if (!body || !selected.value) return
  sending.value = true
  try {
    const msg = await adminApi.sendSupportMessage(selected.value.id, body)
    messages.value.push(msg)
    draft.value = ''
    await nextTick()
    if (messagesEl.value) messagesEl.value.scrollTop = messagesEl.value.scrollHeight
  } catch (e: any) {
    toast.error(e?.message || 'Failed to send')
  } finally {
    sending.value = false
  }
}

loadChats()
</script>

<template>
  <PageHeader title="Support chats" />

  <div class="split">
    <aside class="list">
      <div v-if="loadingChats" class="state">Loading…</div>
      <div v-else-if="!chats.length" class="state">No support requests.</div>
      <button
        v-for="c in chats" :key="c.id"
        class="chat-row"
        :class="{ active: selected?.id === c.id }"
        @click="selectChat(c)"
      >
        <img v-if="c.user_photo_url" :src="imgUrl(c.user_photo_url)" alt="" class="avatar" />
        <div v-else class="avatar avatar-placeholder" />
        <div class="meta">
          <div class="title">{{ c.user_name || c.user_email }}</div>
          <div class="sub">{{ c.user_email }}</div>
          <div v-if="c.last_message_body" class="preview">{{ c.last_message_body }}</div>
        </div>
      </button>
    </aside>

    <section class="convo">
      <header v-if="selected" class="convo-header">
        <img v-if="selected.user_photo_url" :src="imgUrl(selected.user_photo_url)" class="avatar sm" alt="" />
        <div v-else class="avatar avatar-placeholder sm" />
        <div>
          <div class="convo-title">{{ selected.user_name || selected.user_email }}</div>
          <div class="convo-sub">Role: {{ selected.user_role }} · {{ selected.user_email }}</div>
        </div>
      </header>

      <div v-if="!selected" class="state">Select a chat to view messages.</div>
      <div v-else-if="loadingMsgs" class="state">Loading messages…</div>
      <div v-else ref="messagesEl" class="messages">
        <div v-if="!messages.length" class="state">No messages yet.</div>
        <div
          v-for="m in messages" :key="m.id"
          class="msg"
          :class="{ admin: m.sender_kind === 'admin' }"
        >
          <div class="msg-head">
            <span class="sender">{{ m.sender_kind === 'admin' ? 'Support' : (selected.user_name || 'User') }}</span>
            <span class="when">{{ fmtDateTime(m.created_at) }}</span>
          </div>
          <div class="body">{{ m.body }}</div>
        </div>
      </div>

      <form v-if="selected" class="composer" @submit.prevent="send">
        <input
          v-model="draft"
          placeholder="Type here…"
          :disabled="sending"
          autocomplete="off"
        />
        <button class="primary" type="submit" :disabled="!draft.trim() || sending">
          {{ sending ? '…' : 'Send' }}
        </button>
      </form>
    </section>
  </div>
</template>

<style scoped>
.split {
  display: grid;
  grid-template-columns: 320px 1fr;
  height: calc(100vh - 180px);
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
.avatar { width: 40px; height: 40px; border-radius: 50%; object-fit: cover; flex-shrink: 0; }
.avatar.sm { width: 32px; height: 32px; }
.avatar-placeholder { background: var(--bg); border: 1px solid var(--border); }
.meta { min-width: 0; flex: 1; }
.title { font-weight: 500; }
.sub { color: var(--text-muted); font-size: 12px; }
.preview { color: var(--text-muted); font-size: 13px; margin-top: 4px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.convo { display: flex; flex-direction: column; min-width: 0; }
.convo-header { display: flex; align-items: center; gap: 12px; padding: 16px 20px; border-bottom: 1px solid var(--border); }
.convo-title { font-weight: 600; }
.convo-sub { color: var(--text-muted); font-size: 13px; }

.messages { flex: 1; overflow-y: auto; padding: 16px 20px; display: flex; flex-direction: column; gap: 12px; }
.msg { background: var(--bg); padding: 10px 14px; border-radius: 10px; max-width: 70%; align-self: flex-start; }
.msg.admin { background: var(--accent-soft); align-self: flex-end; }
.msg-head { display: flex; gap: 8px; font-size: 11px; color: var(--text-muted); margin-bottom: 4px; }
.body { white-space: pre-wrap; }

.composer {
  display: flex; gap: 8px;
  padding: 12px 16px;
  border-top: 1px solid var(--border);
}
.composer input { flex: 1; }

.state { padding: 32px; color: var(--text-muted); text-align: center; }
</style>
