// Pinia store for support chat real-time state.
// Owns the WebSocket connection so it persists across navigation and the unread
// badge in AdminLayout stays up-to-date even while the Support page is not open.

import { defineStore } from 'pinia'
import { ref, shallowRef } from 'vue'
import { getToken } from '../api/client'
import type { AdminSupportMessage } from '../api/types'

export const useSupportStore = defineStore('support', () => {
  const totalUnread = ref(0)
  // Reactive pointer to the last received WS message — Support.vue watches this.
  const lastMessage = shallowRef<AdminSupportMessage | null>(null)

  let socket: WebSocket | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let reconnectDelay = 3_000
  const MAX_RECONNECT_DELAY = 30_000

  function connect() {
    if (socket?.readyState === WebSocket.OPEN || socket?.readyState === WebSocket.CONNECTING) return
    const token = getToken()
    if (!token) return

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    socket = new WebSocket(`${protocol}//${window.location.host}/api/v1/ws?token=${token}`)

    socket.onopen = () => {
      reconnectDelay = 3_000  // reset backoff on successful connection
    }

    socket.onmessage = (e: MessageEvent) => {
      try {
        const event = JSON.parse(e.data as string)
        if (event.type === 'support_message_created') {
          const msg = event.payload as AdminSupportMessage
          lastMessage.value = msg
          if (msg.sender_kind === 'user') totalUnread.value++
        }
      } catch { /* ignore malformed frames */ }
    }

    socket.onclose = () => {
      socket = null
      reconnectTimer = setTimeout(() => connect(), reconnectDelay)
      reconnectDelay = Math.min(reconnectDelay * 2, MAX_RECONNECT_DELAY)
    }

    socket.onerror = () => socket?.close()
  }

  function disconnect() {
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null }
    socket?.close()
    socket = null
  }

  function setTotalUnread(n: number) { totalUnread.value = n }
  function decrementUnread(n: number) { totalUnread.value = Math.max(0, totalUnread.value - n) }

  return { totalUnread, lastMessage, connect, disconnect, setTotalUnread, decrementUnread }
})
