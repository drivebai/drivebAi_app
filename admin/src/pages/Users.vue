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

// --- password reset ---
// Passwords are stored as one-way bcrypt hashes: there is nothing to view or
// hand out, by design. The only admin remedy for "can't log in" is emailing
// the user a reset link. Confirm first — this invalidates any previously
// issued reset tokens for the account.
const pendingReset = ref<AdminUser | null>(null)
const sendingReset = ref(false)
function askReset(u: AdminUser) { pendingReset.value = u }
async function confirmReset() {
  const u = pendingReset.value
  if (!u || sendingReset.value) return
  sendingReset.value = true
  try {
    await adminApi.resetUserPassword(u.id)
    toast.success(`Reset email sent to ${u.email}`)
  } catch (e: any) {
    toast.error(e?.message || 'Failed to send reset email')
  } finally {
    sendingReset.value = false
    pendingReset.value = null
  }
}

// --- edit profile ---
// Modal-style form bound to a snapshot of the drawer user. Backend only
// accepts first_name / last_name / phone — see adminApi.updateUserProfile.
const editing = ref<AdminUser | null>(null)
const editForm = ref({ first_name: '', last_name: '', phone: '' })
const savingEdit = ref(false)
const editError = ref<string | null>(null)
function startEdit(u: AdminUser) {
  editing.value = u
  editForm.value = {
    first_name: u.first_name ?? '',
    last_name: u.last_name ?? '',
    phone: u.phone ?? '',
  }
  editError.value = null
}
function cancelEdit() {
  editing.value = null
  editError.value = null
}
async function saveEdit() {
  const u = editing.value
  if (!u) return
  const fn = editForm.value.first_name.trim()
  const ln = editForm.value.last_name.trim()
  const ph = editForm.value.phone.trim()
  if (!fn || !ln) {
    editError.value = 'First and last name are required.'
    return
  }
  // Only send what actually changed.
  const body: { first_name?: string; last_name?: string; phone?: string } = {}
  if (fn !== (u.first_name ?? '')) body.first_name = fn
  if (ln !== (u.last_name ?? '')) body.last_name = ln
  if (ph !== (u.phone ?? '')) body.phone = ph
  if (Object.keys(body).length === 0) { cancelEdit(); return }

  savingEdit.value = true
  editError.value = null
  try {
    const updated = await adminApi.updateUserProfile(u.id, body)
    // Patch the row in-place so the table reflects the change without a refetch.
    const idx = rows.value.findIndex(x => x.id === u.id)
    if (idx >= 0) rows.value[idx] = updated
    if (drawerUser.value?.id === u.id) drawerUser.value = updated
    toast.success('Profile updated')
    editing.value = null
  } catch (e: any) {
    editError.value = e?.message || 'Failed to update profile'
  } finally {
    savingEdit.value = false
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
        <button class="ghost" @click="askReset(row)">Reset password</button>
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

    <p class="password-note">
      Passwords are stored as one-way hashes and can never be viewed — by
      anyone, including admins. If a user can't log in, send them a password
      reset email instead.
    </p>

    <div class="drawer-actions">
      <button class="secondary" @click="startEdit(drawerUser)">Edit profile</button>
      <button class="secondary" @click="askReset(drawerUser)">Send password reset</button>
      <button :class="drawerUser.is_blocked ? 'primary' : 'danger'" @click="askBlock(drawerUser)">
        {{ drawerUser.is_blocked ? 'Unblock user' : 'Block user' }}
      </button>
    </div>
  </Drawer>

  <!-- Edit profile modal. Backend allow-lists first_name / last_name / phone;
       email + role intentionally not editable here. -->
  <div v-if="editing" class="modal-overlay" @click.self="cancelEdit">
    <div class="modal" role="dialog" aria-labelledby="editProfileTitle">
      <h2 id="editProfileTitle">Edit profile</h2>
      <p class="modal-sub">{{ editing.email }}</p>

      <label>
        First name
        <input v-model="editForm.first_name" maxlength="100" :disabled="savingEdit" autocomplete="off" />
      </label>
      <label>
        Last name
        <input v-model="editForm.last_name" maxlength="100" :disabled="savingEdit" autocomplete="off" />
      </label>
      <label>
        Phone
        <input v-model="editForm.phone" maxlength="20" :disabled="savingEdit" autocomplete="off" />
      </label>

      <p v-if="editError" class="error">{{ editError }}</p>

      <div class="modal-actions">
        <button class="secondary" :disabled="savingEdit" @click="cancelEdit">Cancel</button>
        <button class="primary" :disabled="savingEdit" @click="saveEdit">
          {{ savingEdit ? 'Saving…' : 'Save' }}
        </button>
      </div>
    </div>
  </div>

  <ConfirmDialog
    :open="!!pendingReset"
    title="Send password reset email?"
    :message="`${pendingReset?.email} will receive an email with a link to choose a new password. Any previously sent reset links stop working. Their current password stays valid until they complete the reset.`"
    :confirm-label="sendingReset ? 'Sending…' : 'Send reset email'"
    @confirm="confirmReset"
    @cancel="pendingReset = null"
  />

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
.password-note {
  margin: 20px 0 0;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--bg);
  color: var(--text-muted);
  font-size: 12.5px;
  line-height: 1.5;
}
.drawer-actions { margin-top: 24px; padding-top: 16px; border-top: 1px solid var(--border); display: flex; gap: 8px; justify-content: flex-end; flex-wrap: wrap; }

.modal-overlay {
  position: fixed; inset: 0; background: rgba(0, 0, 0, 0.4);
  display: flex; align-items: center; justify-content: center;
  z-index: 1000; padding: 16px;
}
.modal {
  background: var(--surface, #fff); border-radius: 12px; padding: 24px;
  width: 100%; max-width: 420px; display: flex; flex-direction: column; gap: 14px;
  box-shadow: 0 12px 32px rgba(0, 0, 0, 0.15);
}
.modal h2 { margin: 0; font-size: 18px; }
.modal-sub { margin: -8px 0 4px; color: var(--text-muted); font-size: 13px; }
.modal label { display: flex; flex-direction: column; gap: 4px; font-size: 13px; color: var(--text-muted); }
.modal input {
  padding: 10px 12px; border: 1px solid var(--border); border-radius: 8px;
  font-size: 14px; background: var(--bg, #fff); color: var(--text, #111);
}
.modal input:focus { outline: 2px solid var(--accent, #2bd1c4); outline-offset: -1px; }
.modal .error { color: var(--danger, #d33); font-size: 13px; margin: 0; }
.modal-actions { display: flex; gap: 8px; justify-content: flex-end; margin-top: 8px; }
</style>
