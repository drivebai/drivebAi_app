<script setup lang="ts">
defineProps<{
  open: boolean
  title: string
  message: string
  confirmLabel?: string
  cancelLabel?: string
  destructive?: boolean
}>()
defineEmits<{ (e: 'confirm'): void; (e: 'cancel'): void }>()
</script>

<template>
  <div v-if="open" class="overlay" @click.self="$emit('cancel')">
    <div class="dialog" role="alertdialog" aria-modal="true">
      <h3>{{ title }}</h3>
      <p>{{ message }}</p>
      <div class="actions">
        <button @click="$emit('cancel')">{{ cancelLabel || 'Cancel' }}</button>
        <button :class="destructive ? 'danger' : 'primary'" @click="$emit('confirm')">
          {{ confirmLabel || 'Confirm' }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.overlay {
  position: fixed; inset: 0;
  background: rgba(17,24,39,0.4);
  display: flex; align-items: center; justify-content: center;
  z-index: 200;
}
.dialog {
  background: var(--surface);
  border-radius: 12px;
  padding: 24px;
  width: min(420px, 92vw);
  box-shadow: 0 12px 40px rgba(0,0,0,0.18);
}
h3 { margin: 0 0 8px; font-size: 18px; }
p  { margin: 0 0 20px; color: var(--text-muted); line-height: 1.5; }
.actions { display: flex; justify-content: flex-end; gap: 8px; }
</style>
