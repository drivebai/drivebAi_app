// Pinia store for the support-ticket queue badge (open-count in the sidebar).
//
// It owns no socket of its own — the support store already holds the single
// admin WebSocket. AdminLayout seeds this count on mount and re-fetches it
// whenever a `ticket_submitted` frame arrives (surfaced by the support store),
// and the Tickets page refreshes it after every status change. The count is
// always the server's authoritative open total, so it can't drift.

import { defineStore } from 'pinia'
import { ref } from 'vue'
import { adminApi } from '../api/admin'

export const useTicketsStore = defineStore('tickets', () => {
  const openCount = ref(0)

  async function refreshOpenCount() {
    try {
      // limit:1 — we only need the total, not the rows.
      const res = await adminApi.listTickets({ status: 'open', limit: 1 })
      openCount.value = res.total
    } catch {
      // Non-fatal: a stale badge is better than a crash. Leave the last value.
    }
  }

  function setOpenCount(n: number) { openCount.value = Math.max(0, n) }

  return { openCount, refreshOpenCount, setOpenCount }
})
