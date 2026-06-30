<script setup lang="ts">
defineProps<{ title: string }>()
</script>

<template>
  <header class="page-header">
    <h1>{{ title }}</h1>
    <div class="actions"><slot /></div>
  </header>
</template>

<style scoped>
.page-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 24px;
  flex-wrap: wrap;
}
h1 { margin: 0; font-size: 28px; font-weight: 600; }
.actions { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }

@media (max-width: 640px) {
  .page-header { margin-bottom: 16px; gap: 8px; }
  h1 { font-size: 22px; }
}
</style>
