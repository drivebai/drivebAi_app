<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  /** Free-form status string. Visual is picked by `tone`, not by parsing. */
  label: string
  /** Visual category. Default 'neutral'. */
  tone?: 'success' | 'danger' | 'warning' | 'info' | 'neutral'
}>()

const cls = computed(() => `badge tone-${props.tone || 'neutral'}`)
</script>

<template>
  <span :class="cls">{{ label }}</span>
</template>

<style scoped>
.badge {
  display: inline-block;
  padding: 2px 10px;
  border-radius: 999px;
  font-size: 12px;
  font-weight: 500;
  white-space: nowrap;
  border: 1px solid transparent;
}
.tone-success { background: #d1fae5; color: #047857; border-color: #a7f3d0; }
.tone-danger  { background: var(--danger-soft); color: var(--danger); border-color: #fecaca; }
.tone-warning { background: #fef3c7; color: #b45309; border-color: #fde68a; }
.tone-info    { background: var(--accent-soft); color: var(--accent-strong); border-color: var(--accent-soft); }
.tone-neutral { background: var(--bg); color: var(--text-muted); border-color: var(--border); }
</style>
