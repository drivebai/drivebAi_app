<script setup lang="ts">
import { ref, watch } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import DataTable from '../components/DataTable.vue'
import StatusBadge from '../components/StatusBadge.vue'
import Drawer from '../components/Drawer.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import { adminApi } from '../api/admin'
import type { AdminUser } from '../api/types'
import { useToastStore } from '../stores/toast'
import { fmtDate } from '../utils/format'

const toast = useToastStore()

const rows = ref<AdminUser[]>([])
const total = ref(0)
const page = ref(1)
const limit = ref(50)
const loading = ref(false)

const query = ref('')
const role = ref('')
const status = ref('active') // matches the prototype's "Show Blocked Drivers" toggle (default = active only)

// Search debouncer — avoid hammering the API on every keystroke.
let searchTimer: number | undefined
watch(query, () => {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => { page.value = 1; load() }, 250)
})
watch([role, status], () => { page.value = 1; load() })

async function load() {
  loading.value = true
  try {
    const res = await adminApi.listUsers({
      query: query.value, role: role.value, status: status.value,
      page: page.value, limit: limit.value,
    })
    rows.value = res.items
    total.value = res.total
  } catch (e: any) {
    toast.error(e?.message || 'Failed to load users')
  } finally {
    loading.value = false
  }
}
load()

// --- detail drawer ---
const drawerUser = ref<AdminUser | null>(null)
function openDetails(u: AdminUser) { drawerUser.value = u }

// --- block confirm ---
const pendingBlock = ref<AdminUser | null>(null)
function askBlock(u: AdminUser) { pendingBlock.value = u }
async function confirmBlock() {
  const u = pendingBlock.value
  if (!u) return
  try {
    await adminApi.blockUser(u.id, !u.is_blocked)
    u.is_blocked = !u.is_blocked
    if (drawerUser.value?.id === u.id) drawerUser.value = { ...u }
    toast.success(u.is_blocked ? 'User blocked' : 'User unblocked')
  } catch (e: any) {
    toast.error(e?.message || 'Action failed')
  } finally {
    pendingBlock.value = null
  }
}

function roleLabel(r: string) {
  if (r === 'driver') return 'Driver'
  if (r === 'car_owner') return 'Owner'
  return r
}
</script>

<template>
  <PageHeader title="Users" />

  <div class="filters">
    <input v-model="query" placeholder="Search by email or name…" class="search" />
    <select v-model="role">
      <option value="">All roles</option>
      <option value="driver">Driver</option>
      <option value="car_owner">Owner</option>
    </select>
    <select v-model="status">
      <option value="active">Active only</option>
      <option value="blocked">Blocked only</option>
      <option value="">All</option>
    </select>
  </div>

  <DataTable
    :rows :loading :total :page :limit
    :on-row-click="openDetails"
    @page="(p: number) => { page = p; load() }"
  >
    <template #header>
      <th>Email</th>
      <th>Full Name</th>
      <th>Role</th>
      <th>Status</th>
      <th>Created</th>
      <th></th>
    </template>
    <template #row="{ row }">
      <td>{{ row.email }}</td>
      <td>{{ row.first_name }} {{ row.last_name }}</td>
      <td>{{ roleLabel(row.role) }}</td>
      <td>
        <StatusBadge
          :label="row.is_blocked ? 'Blocked' : 'Active'"
          :tone="row.is_blocked ? 'danger' : 'success'"
        />
      </td>
      <td>{{ fmtDate(row.created_at) }}</td>
      <td class="actions" @click.stop>
        <button class="ghost" @click="openDetails(row)">Details</button>
        <button :class="row.is_blocked ? 'primary' : 'danger'" @click="askBlock(row)">
          {{ row.is_blocked ? 'Unblock' : 'Block' }}
        </button>
      </td>
    </template>
    <template #empty>No users match these filters.</template>
  </DataTable>

  <Drawer v-if="drawerUser" :title="`${drawerUser.first_name} ${drawerUser.last_name}`" @close="drawerUser = null">
    <dl class="kv">
      <dt>Email</dt><dd>{{ drawerUser.email }}</dd>
      <dt>Phone</dt><dd>{{ drawerUser.phone || '—' }}</dd>
      <dt>Role</dt><dd>{{ roleLabel(drawerUser.role) }}</dd>
      <dt>Email verified</dt><dd>{{ drawerUser.is_email_verified ? 'Yes' : 'No' }}</dd>
      <dt>Onboarding</dt><dd>{{ drawerUser.onboarding_status }}</dd>
      <dt>Status</dt>
      <dd>
        <StatusBadge
          :label="drawerUser.is_blocked ? 'Blocked' : 'Active'"
          :tone="drawerUser.is_blocked ? 'danger' : 'success'"
        />
      </dd>
      <dt>Created</dt><dd>{{ fmtDate(drawerUser.created_at) }}</dd>
      <template v-if="drawerUser.role === 'driver'">
        <dt>Driver's license</dt>
        <dd>
          <StatusBadge
            :label="drawerUser.has_license ? 'Uploaded' : 'Missing'"
            :tone="drawerUser.has_license ? 'success' : 'warning'"
          />
        </dd>
        <dt>Registration</dt>
        <dd>
          <StatusBadge
            :label="drawerUser.has_registration ? 'Uploaded' : 'Missing'"
            :tone="drawerUser.has_registration ? 'success' : 'warning'"
          />
        </dd>
      </template>
    </dl>

    <div class="drawer-actions">
      <button :class="drawerUser.is_blocked ? 'primary' : 'danger'" @click="askBlock(drawerUser)">
        {{ drawerUser.is_blocked ? 'Unblock user' : 'Block user' }}
      </button>
    </div>
  </Drawer>

  <ConfirmDialog
    :open="!!pendingBlock"
    :title="pendingBlock?.is_blocked ? 'Unblock user?' : 'Block user?'"
    :message="pendingBlock?.is_blocked
      ? `${pendingBlock?.email} will be able to log in and use the app again.`
      : `${pendingBlock?.email} will be signed out and unable to log in.`"
    :confirm-label="pendingBlock?.is_blocked ? 'Unblock' : 'Block'"
    :destructive="!pendingBlock?.is_blocked"
    @confirm="confirmBlock"
    @cancel="pendingBlock = null"
  />
</template>

<style scoped>
.filters {
  display: flex; gap: 12px;
  margin-bottom: 16px;
  align-items: center;
}
.search { flex: 1; max-width: 360px; }
select { width: 160px; }
.actions { display: flex; gap: 8px; justify-content: flex-end; }

.kv { display: grid; grid-template-columns: 140px 1fr; gap: 12px 16px; margin: 0; }
.kv dt { color: var(--text-muted); }
.kv dd { margin: 0; }
.drawer-actions { margin-top: 24px; padding-top: 16px; border-top: 1px solid var(--border); display: flex; justify-content: flex-end; }
</style>
