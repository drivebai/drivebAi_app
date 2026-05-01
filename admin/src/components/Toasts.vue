<script setup lang="ts">
import { useToastStore } from '../stores/toast'
const toast = useToastStore()
</script>

<template>
  <div class="stack" aria-live="polite">
    <div v-for="t in toast.toasts" :key="t.id" class="toast" :data-kind="t.kind">
      <span>{{ t.text }}</span>
      <button class="ghost dismiss" @click="toast.dismiss(t.id)" aria-label="Dismiss">×</button>
    </div>
  </div>
</template>

<style scoped>
.stack {
  position: fixed;
  bottom: 24px;
  right: 24px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  z-index: 1000;
}
.toast {
  display: flex;
  align-items: center;
  gap: 12px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-left: 4px solid var(--text-muted);
  padding: 10px 14px;
  border-radius: var(--radius);
  box-shadow: 0 4px 14px rgba(0,0,0,0.08);
  min-width: 240px;
  max-width: 400px;
}
.toast[data-kind="success"] { border-left-color: var(--success); }
.toast[data-kind="error"]   { border-left-color: var(--danger); }
.toast[data-kind="info"]    { border-left-color: var(--accent-strong); }

.dismiss { font-size: 18px; padding: 0 4px; }
</style>
