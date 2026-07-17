<script setup lang="ts">
// Star display (out of 5) for real review aggregates. rating == null or
// count == 0 renders "No ratings yet" — never a fake number.
const props = defineProps<{ rating?: number | null; count: number }>()
</script>

<template>
  <span
    v-if="props.count > 0 && props.rating != null"
    class="stars"
    :title="`${props.rating.toFixed(1)} out of 5 from ${props.count} review${props.count === 1 ? '' : 's'}`"
  >
    <span class="star-row" aria-hidden="true">
      <span v-for="i in 5" :key="i" :class="i <= Math.round(props.rating!) ? 'filled' : 'empty'">★</span>
    </span>
    <span class="value">{{ props.rating.toFixed(1) }}</span>
    <span class="count">({{ props.count }})</span>
  </span>
  <span v-else class="none">No ratings yet</span>
</template>

<style scoped>
.stars { display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }
.star-row { letter-spacing: 1px; font-size: 13px; }
.star-row .filled { color: #f5a623; }
.star-row .empty { color: var(--border, #ddd); }
.value { font-weight: 600; }
.count { color: var(--text-muted); font-size: 12.5px; }
.none { color: var(--text-muted); font-size: 12.5px; }
</style>
