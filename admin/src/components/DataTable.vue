<script setup lang="ts" generic="T extends { id: string }">
/**
 * Minimal data table used by every admin list page.
 * Server does the searching + pagination; this is just the view layer.
 *
 * Usage:
 *   <DataTable :rows :loading :total :page :limit @page="onPage">
 *     <template #header>...<th>columns...</th></template>
 *     <template #row="{ row }">...<td>...</td></template>
 *     <template #empty>No users yet.</template>
 *   </DataTable>
 */

defineProps<{
  rows: T[]
  loading?: boolean
  total: number
  page: number
  limit: number
  /** Optional override of the row click handler — by default rows are unclickable. */
  onRowClick?: (row: T) => void
}>()

const emit = defineEmits<{ (e: 'page', p: number): void }>()

function pageCount(total: number, limit: number) {
  return Math.max(1, Math.ceil(total / limit))
}
</script>

<template>
  <div class="table-wrap">
    <table>
      <thead>
        <tr><slot name="header" /></tr>
      </thead>
      <tbody v-if="!loading && rows.length">
        <tr
          v-for="row in rows" :key="row.id"
          :class="{ clickable: !!onRowClick }"
          @click="onRowClick?.(row)"
        >
          <slot name="row" :row="row" />
        </tr>
      </tbody>
      <tbody v-else-if="loading">
        <tr><td class="state" colspan="99">Loading…</td></tr>
      </tbody>
      <tbody v-else>
        <tr><td class="state" colspan="99"><slot name="empty">No data.</slot></td></tr>
      </tbody>
    </table>

    <footer v-if="total > 0">
      <span class="muted">
        {{ ((page - 1) * limit) + 1 }}–{{ Math.min(page * limit, total) }} of {{ total }}
      </span>
      <div class="pager">
        <button :disabled="page <= 1" @click="emit('page', page - 1)">‹ Prev</button>
        <span class="page-num">Page {{ page }} / {{ pageCount(total, limit) }}</span>
        <button :disabled="page >= pageCount(total, limit)" @click="emit('page', page + 1)">Next ›</button>
      </div>
    </footer>
  </div>
</template>

<style scoped>
.table-wrap {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  overflow: hidden;
}
.state {
  text-align: center;
  padding: 32px;
  color: var(--text-muted);
}
.clickable { cursor: pointer; }
footer {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 12px 16px;
  border-top: 1px solid var(--border);
  background: var(--surface);
  color: var(--text-muted);
  font-size: 13px;
}
.muted { color: var(--text-muted); }
.pager { display: flex; align-items: center; gap: 12px; }
.page-num { color: var(--text-muted); }
</style>
