<script setup lang="ts">
defineProps<{
  modelValue: boolean
  disabled?: boolean
  ariaLabel?: string
}>()
defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()
</script>

<template>
  <button
    type="button"
    class="toggle"
    :class="{ on: modelValue }"
    :disabled="disabled"
    :aria-pressed="modelValue"
    :aria-label="ariaLabel"
    @click="$emit('update:modelValue', !modelValue)"
  >
    <span class="thumb" />
  </button>
</template>

<style scoped>
.toggle {
  position: relative;
  width: 38px; height: 22px;
  border-radius: 999px;
  background: var(--border);
  border: none;
  padding: 0;
  cursor: pointer;
  transition: background 120ms ease;
}
.toggle.on { background: var(--accent); }
.toggle:disabled { opacity: 0.5; cursor: not-allowed; }
.thumb {
  position: absolute;
  top: 2px; left: 2px;
  width: 18px; height: 18px;
  border-radius: 50%;
  background: var(--surface);
  box-shadow: var(--shadow-sm);
  transition: transform 120ms ease;
}
.toggle.on .thumb { transform: translateX(16px); }
</style>
