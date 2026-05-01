import { defineStore } from 'pinia'
import { ref } from 'vue'

export type ToastKind = 'info' | 'success' | 'error'
export interface Toast { id: number; kind: ToastKind; text: string }

let nextId = 1

export const useToastStore = defineStore('toast', () => {
  const toasts = ref<Toast[]>([])

  function push(kind: ToastKind, text: string, ttlMs = 4000) {
    const t: Toast = { id: nextId++, kind, text }
    toasts.value.push(t)
    setTimeout(() => dismiss(t.id), ttlMs)
  }
  function dismiss(id: number) {
    toasts.value = toasts.value.filter(t => t.id !== id)
  }

  return {
    toasts,
    info:    (s: string) => push('info', s),
    success: (s: string) => push('success', s),
    error:   (s: string) => push('error', s, 6000),
    dismiss,
  }
})
