<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue'

const props = defineProps<{ title: string }>()
const emit = defineEmits<{ (e: 'close'): void }>()

function onKey(e: KeyboardEvent) { if (e.key === 'Escape') emit('close') }
onMounted(() => window.addEventListener('keydown', onKey))
onUnmounted(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <div class="overlay" @click.self="emit('close')">
    <aside class="drawer" role="dialog" :aria-label="props.title">
      <header>
        <h2>{{ props.title }}</h2>
        <button class="ghost close" @click="emit('close')" aria-label="Close">×</button>
      </header>
      <div class="body"><slot /></div>
    </aside>
  </div>
</template>

<style scoped>
.overlay {
  position: fixed; inset: 0;
  background: rgba(17, 24, 39, 0.35);
  display: flex; justify-content: flex-end;
  z-index: 100;
}
.drawer {
  width: min(560px, 92vw);
  background: var(--surface);
  height: 100%;
  display: flex; flex-direction: column;
  box-shadow: -8px 0 24px rgba(0,0,0,0.08);
  animation: slideIn 160ms ease;
}
@keyframes slideIn { from { transform: translateX(20px); opacity: 0 } to { transform: none; opacity: 1 } }

header {
  display: flex; align-items: center; justify-content: space-between;
  padding: 16px 20px;
  border-bottom: 1px solid var(--border);
}
h2 { margin: 0; font-size: 18px; }
.close { font-size: 22px; line-height: 1; padding: 0 8px; }

.body {
  padding: 20px;
  overflow-y: auto;
  flex: 1;
  -webkit-overflow-scrolling: touch;
}

/* Phones: take the full viewport so wide content (tables, doc grids, photo
   galleries) has room, and give the close control a comfortable tap target. */
@media (max-width: 640px) {
  .drawer {
    width: 100vw;
    box-shadow: none;
  }
  header {
    padding: 12px 14px;
    position: sticky;
    top: 0;
    background: var(--surface);
    z-index: 1;
  }
  h2 { font-size: 16px; }
  .close {
    min-width: 44px;
    min-height: 44px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 0;
  }
  .body { padding: 14px; }
}
</style>
