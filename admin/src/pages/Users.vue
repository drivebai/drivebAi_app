<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import PageHeader from '../components/PageHeader.vue'
import DataTable from '../components/DataTable.vue'
import StatusBadge from '../components/StatusBadge.vue'
import Drawer from '../components/Drawer.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import StarRating from '../components/StarRating.vue'
import { adminApi } from '../api/admin'
import type { AdminUser, AdminUserDocument } from '../api/types'
import { useToastStore } from '../stores/toast'
import { fmtDate, imgUrl } from '../utils/format'

const toast = useToastStore()
const router = useRouter()

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
function openDetails(u: AdminUser) {
  drawerUser.value = u
  loadDocs(u)
}

// --- documents (license review) ---
const drawerDocs = ref<AdminUserDocument[]>([])
const docsLoading = ref(false)
const docSaving = ref<string | null>(null) // id of the doc being saved

async function loadDocs(u: AdminUser) {
  drawerDocs.value = []
  docsLoading.value = true
  try {
    drawerDocs.value = await adminApi.listUserDocuments(u.id)
  } catch (e: any) {
    toast.error(e?.message || 'Failed to load documents')
  } finally {
    docsLoading.value = false
  }
}

async function approveDoc(doc: AdminUserDocument) {
  const u = drawerUser.value
  if (!u || docSaving.value) return
  docSaving.value = doc.id
  try {
    await adminApi.setUserDocumentStatus(u.id, doc.id, { status: 'verified' })
    doc.status = 'verified'
    toast.success(`${docLabel(doc.type)} approved — the user has been notified`)
  } catch (e: any) {
    toast.error(e?.message || 'Failed to approve document')
  } finally {
    docSaving.value = null
  }
}

// Slot list (client point 4): always render the driver's-license slot,
// plus TLC and commercial when absent, plus anything else the user
// actually uploaded — otherwise "Upload" is unreachable for a document
// the driver has never provided. Registration stays deliberately
// unsolicited (car-owner document).
const DRIVER_DOC_SLOTS = ['drivers_license', 'tlc_license', 'commercial_license']

const docSlots = computed(() => {
  const byType = new Map(drawerDocs.value.map(d => [d.type, d]))
  const slots: { type: string; doc: AdminUserDocument | null }[] =
    DRIVER_DOC_SLOTS.map(t => ({ type: t, doc: byType.get(t) ?? null }))
  for (const d of drawerDocs.value) {
    if (!DRIVER_DOC_SLOTS.includes(d.type)) slots.push({ type: d.type, doc: d })
  }
  return slots
})

// Upload / replace on the user's behalf (client point 4). The endpoint
// resets the slot to pending review — an approval attests to specific
// bytes, and the bytes just changed.
const uploadingType = ref<string | null>(null)
const docFileInput = ref<HTMLInputElement | null>(null)
const pendingUploadType = ref<string | null>(null)

function pickDocFile(docType: string) {
  if (uploadingType.value) return
  pendingUploadType.value = docType
  docFileInput.value?.click()
}

async function onDocFilePicked(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  const docType = pendingUploadType.value
  pendingUploadType.value = null
  const u = drawerUser.value
  if (!file || !docType || !u) return
  if (file.size > 10 * 1024 * 1024) {
    toast.error('File is too large — the limit is 10 MB')
    return
  }
  uploadingType.value = docType
  try {
    const replaced = await adminApi.replaceUserDocument(u.id, docType, file)
    // Swap the card from the response — but only if the drawer still shows
    // the user this upload was for: nothing stops the admin closing the
    // drawer and opening someone else mid-flight, and writing into the
    // CURRENT drawerDocs would graft this card onto the wrong user's list.
    if (drawerUser.value?.id === u.id) {
      const idx = drawerDocs.value.findIndex(d => d.type === docType)
      if (idx >= 0) drawerDocs.value[idx] = replaced
      else drawerDocs.value.push(replaced)
    }
    toast.success(`${docLabel(docType)} uploaded for ${u.email} — pending review`)
  } catch (e: any) {
    toast.error(e?.message || 'Failed to upload document')
  } finally {
    uploadingType.value = null
  }
}

// Decline needs a reason (the user sees it), so it gets a small modal
// instead of a bare ConfirmDialog.
const declining = ref<AdminUserDocument | null>(null)
const declineReason = ref('')
const savingDecline = ref(false)
const declineError = ref<string | null>(null)
function startDecline(doc: AdminUserDocument) {
  declining.value = doc
  declineReason.value = ''
  declineError.value = null
}
async function confirmDecline() {
  const doc = declining.value
  const u = drawerUser.value
  if (!doc || !u || savingDecline.value) return
  const reason = declineReason.value.trim()
  if (!reason) {
    declineError.value = 'A reason is required — the user will see it.'
    return
  }
  savingDecline.value = true
  declineError.value = null
  try {
    await adminApi.setUserDocumentStatus(u.id, doc.id, { status: 'rejected', reason })
    doc.status = 'rejected'
    toast.success(`${docLabel(doc.type)} declined — the user has been notified`)
    declining.value = null
  } catch (e: any) {
    declineError.value = e?.message || 'Failed to decline document'
  } finally {
    savingDecline.value = false
  }
}

function docLabel(t: string) {
  switch (t) {
    case 'drivers_license': return "Driver's license"
    case 'registration': return 'Vehicle registration'
    case 'commercial_license': return 'Commercial license'
    case 'tlc_license': return 'TLC license'
    default: return 'Document'
  }
}
function docStatusLabel(s: string) {
  return s === 'verified' ? 'Approved' : s === 'rejected' ? 'Declined' : 'Pending review'
}
function docStatusTone(s: string): 'success' | 'danger' | 'warning' {
  return s === 'verified' ? 'success' : s === 'rejected' ? 'danger' : 'warning'
}
function docIsImage(doc: AdminUserDocument) {
  if (doc.mime_type?.startsWith('image/')) return true
  return /\.(jpe?g|png|webp|heic)$/i.test(doc.file_url.split('?')[0] ?? '')
}

// In-console document preview (same pattern as Support.vue's attachment
// modal): { url, mime } while open, null while closed. url is absolute via
// imgUrl() so it hits the API host, not the SPA.
const docPreview = ref<{ url: string; mime: string } | null>(null)
function openDocPreview(doc: AdminUserDocument) {
  docPreview.value = {
    url: imgUrl(doc.file_url) || '',
    mime: doc.mime_type || (docIsImage(doc) ? 'image/jpeg' : 'application/pdf'),
  }
}

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

// --- account deletion (batch item 3 — soft delete / anonymize) ---
// The server tombstones the row: email renamed, phone NULLed (both freed
// for re-registration), name anonymized, account blocked, deleted_at
// stamped. Transaction history survives attributed to "Deleted User".
const pendingDelete = ref<AdminUser | null>(null)
const deleting = ref(false)
function askDelete(u: AdminUser) { pendingDelete.value = u }
async function confirmDelete() {
  const u = pendingDelete.value
  if (!u || deleting.value) return
  deleting.value = true
  try {
    await adminApi.deleteUser(u.id)
    toast.success('Account deleted — the email and phone are free to register again')
    pendingDelete.value = null
    drawerUser.value = null
    load()
  } catch (e: any) {
    toast.error(e?.message || 'Failed to delete account')
  } finally {
    deleting.value = false
  }
}

// --- live chat (admin-initiated support conversation) ---
// Opens (or creates) the user's ONE support chat — the same thread their
// in-app Ask-for-Help button uses — and jumps to Support with it selected.
const openingChatFor = ref<string | null>(null)
async function openLiveChat(u: AdminUser) {
  if (openingChatFor.value) return
  openingChatFor.value = u.id
  try {
    const res = await adminApi.openSupportChat(u.id)
    router.push({ name: 'support', query: { chat: res.chat_id } })
  } catch (e: any) {
    toast.error(e?.message || 'Failed to open live chat')
  } finally {
    openingChatFor.value = null
  }
}

// --- mode switch (sign-up role vs current mode) ---
// The user's CURRENT mode can be changed by admin — e.g. when someone is
// stuck unable to switch to Owner mode themselves. The target profile is
// created server-side if missing; the user's device updates via WS + push.
const pendingSwitch = ref<AdminUser | null>(null)
const switchTarget = ref<'driver' | 'car_owner'>('driver')
const switching = ref(false)
function currentMode(u: AdminUser): string { return u.active_role ?? u.role }
function otherMode(u: AdminUser): 'driver' | 'car_owner' {
  return currentMode(u) === 'driver' ? 'car_owner' : 'driver'
}
// A user "has both profiles" when they hold a driver AND an owner profile
// (profile_roles ships on every admin user row; second profile is created
// lazily on first mode switch).
function hasBothProfiles(u: AdminUser): boolean {
  return (u.profile_roles ?? []).includes('driver') && (u.profile_roles ?? []).includes('car_owner')
}
function askSwitch(u: AdminUser) {
  switchTarget.value = otherMode(u)
  pendingSwitch.value = u
}
async function confirmSwitch() {
  const u = pendingSwitch.value
  if (!u || switching.value) return
  switching.value = true
  try {
    await adminApi.setUserActiveProfile(u.id, switchTarget.value)
    u.active_role = switchTarget.value
    u.role = switchTarget.value // users.role mirrors the active profile
    if (!u.profile_roles?.includes(switchTarget.value)) u.profile_roles?.push(switchTarget.value)
    if (drawerUser.value?.id === u.id) drawerUser.value = { ...u }
    toast.success(`Switched to ${roleLabel(switchTarget.value)} mode — the user's app updates automatically`)
  } catch (e: any) {
    toast.error(e?.message || 'Mode switch failed')
  } finally {
    switching.value = false
    pendingSwitch.value = null
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

// Human-readable onboarding step instead of the raw enum string.
// Mirrors backend/internal/models/user.go OnboardingStatus values.
function onboardingLabel(s: string) {
  switch (s) {
    case 'created': return 'Account created — no role chosen yet'
    case 'role_selected': return 'Role chosen — profile photo pending'
    case 'photo_uploaded': return 'Photo added — documents pending'
    case 'documents_uploaded': return 'Documents uploaded — finishing up'
    case 'complete': return 'Complete'
    default: return s
  }
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

  <!-- Desktop: 8-column table -->
  <div class="desktop-only">
  <DataTable
    :rows :loading :total :page :limit
    :on-row-click="openDetails"
    @page="(p: number) => { page = p; load() }"
  >
    <template #header>
      <th>Email</th>
      <th>Full Name</th>
      <th>Signed up as</th>
      <th>Current mode</th>
      <th>Rating</th>
      <th>Status</th>
      <th>Created</th>
      <th></th>
    </template>
    <template #row="{ row }">
      <td>{{ row.email }}</td>
      <td>{{ row.first_name }} {{ row.last_name }}</td>
      <td>{{ roleLabel(row.signup_role ?? row.role) }}</td>
      <td>
        {{ roleLabel(currentMode(row)) }}
        <span v-if="hasBothProfiles(row)" class="both-badge" title="Holds both driver and owner profiles">Driver + Owner</span>
      </td>
      <td><StarRating :rating="row.rating" :count="row.rating_count" /></td>
      <td>
        <StatusBadge
          :label="row.deleted_at ? 'Deleted' : row.is_blocked ? 'Blocked' : 'Active'"
          :tone="row.deleted_at ? 'neutral' : row.is_blocked ? 'danger' : 'success'"
        />
      </td>
      <td>{{ fmtDate(row.created_at) }}</td>
      <td class="actions" @click.stop>
        <button
          class="ghost"
          :disabled="openingChatFor === row.id"
          title="Open a live support chat with this user"
          @click="openLiveChat(row)"
        >💬 Chat</button>
        <button class="ghost" @click="openDetails(row)">Details</button>
        <button class="ghost" @click="askReset(row)">Reset password</button>
        <button :class="row.is_blocked ? 'primary' : 'danger'" @click="askBlock(row)">
          {{ row.is_blocked ? 'Unblock' : 'Block' }}
        </button>
      </td>
    </template>
    <template #empty>No users match these filters.</template>
  </DataTable>
  </div>

  <!-- Phone: stacked cards → tap opens the full-screen Drawer (which holds every
       action: Live chat, Edit, Reset password, Block). No sideways table scroll. -->
  <div class="mobile-only">
    <div v-if="loading" class="card-state">Loading…</div>
    <div v-else-if="!rows.length" class="card-state">No users match these filters.</div>
    <template v-else>
      <button v-for="row in rows" :key="row.id" class="list-card" @click="openDetails(row)">
        <div class="lc-top">
          <span class="lc-title">{{ row.first_name }} {{ row.last_name }}</span>
          <StatusBadge
            :label="row.is_blocked ? 'Blocked' : 'Active'"
            :tone="row.is_blocked ? 'danger' : 'success'"
          />
        </div>
        <div class="lc-name">{{ row.email }}</div>
        <div class="lc-sub">
          {{ roleLabel(row.signup_role ?? row.role) }} · {{ fmtDate(row.created_at) }}
          <span v-if="hasBothProfiles(row)" class="both-badge">Driver + Owner</span>
        </div>
      </button>
      <footer v-if="total > 0" class="mobile-pager">
        <span class="muted">{{ ((page - 1) * limit) + 1 }}–{{ Math.min(page * limit, total) }} of {{ total }}</span>
        <div class="pager">
          <button :disabled="page <= 1" @click="page = page - 1; load()">‹ Prev</button>
          <span class="page-num">{{ page }} / {{ Math.max(1, Math.ceil(total / limit)) }}</span>
          <button :disabled="page >= Math.ceil(total / limit)" @click="page = page + 1; load()">Next ›</button>
        </div>
      </footer>
    </template>
  </div>

  <Drawer v-if="drawerUser" :title="`${drawerUser.first_name} ${drawerUser.last_name}`" @close="drawerUser = null">
    <dl class="kv">
      <dt>Email</dt><dd>{{ drawerUser.email }}</dd>
      <dt>Phone</dt><dd>{{ drawerUser.phone || '—' }}</dd>
      <dt>Signed up as</dt><dd>{{ roleLabel(drawerUser.signup_role ?? drawerUser.role) }}</dd>
      <dt>Current mode</dt>
      <dd class="mode-cell">
        <span>{{ roleLabel(currentMode(drawerUser)) }}</span>
        <button class="ghost" :disabled="switching" @click="askSwitch(drawerUser)">
          Switch to {{ roleLabel(otherMode(drawerUser)) }}
        </button>
      </dd>
      <dt>Profiles</dt>
      <dd>
        <span v-if="hasBothProfiles(drawerUser)" class="both-badge">Driver + Owner</span>
        <span v-else>{{ roleLabel(currentMode(drawerUser)) }} only</span>
      </dd>
      <dt>Rating</dt><dd><StarRating :rating="drawerUser.rating" :count="drawerUser.rating_count" /></dd>
      <dt>Email verified</dt><dd>{{ drawerUser.is_email_verified ? 'Yes' : 'No' }}</dd>
      <dt>Onboarding</dt><dd>{{ onboardingLabel(drawerUser.onboarding_status) }}</dd>
      <dt>Status</dt>
      <dd>
        <StatusBadge
          :label="drawerUser.is_blocked ? 'Blocked' : 'Active'"
          :tone="drawerUser.is_blocked ? 'danger' : 'success'"
        />
      </dd>
      <dt>Created</dt><dd>{{ fmtDate(drawerUser.created_at) }}</dd>
    </dl>

    <!-- Personal documents: slot list, not raw uploads (client point 4) —
         the driver's-license slot always renders, TLC/commercial render
         even when absent, so Upload is reachable for a document the driver
         never provided. Registration stays deliberately unsolicited — it's
         a car-owner document. -->
    <section class="docs-section">
      <h3>Documents</h3>
      <input
        ref="docFileInput"
        type="file"
        accept="image/jpeg,image/png,application/pdf"
        style="display: none"
        @change="onDocFilePicked"
      />
      <p v-if="docsLoading" class="muted">Loading documents…</p>
      <template v-else>
        <div v-for="slot in docSlots" :key="slot.type" class="doc-card">
          <div class="doc-head">
            <strong>{{ docLabel(slot.type) }}</strong>
            <StatusBadge
              v-if="slot.doc"
              :label="docStatusLabel(slot.doc.status)"
              :tone="docStatusTone(slot.doc.status)"
            />
            <span v-else class="muted">Not uploaded</span>
          </div>
          <template v-if="slot.doc">
            <!-- In-console preview. The raw file_url is a RELATIVE signed path;
                 opened in a new tab it resolved against the admin origin, whose
                 SPA fallback served index.html → the router bounced to /users
                 (the client's "it just shows the user list again"). imgUrl()
                 prefixes the API base and preserves the ?sig=&exp= query. -->
            <button type="button" class="doc-link" @click="openDocPreview(slot.doc)">
              <img
                v-if="docIsImage(slot.doc)"
                :src="imgUrl(slot.doc.file_url)"
                :alt="docLabel(slot.doc.type)"
                class="doc-image"
                loading="lazy"
              />
              <span v-else class="doc-open-file">Open {{ slot.doc.file_name }}</span>
            </button>
            <div class="doc-actions">
              <button
                class="primary"
                :disabled="docSaving === slot.doc.id || slot.doc.status === 'verified' || uploadingType === slot.type"
                @click="approveDoc(slot.doc)"
              >
                {{ slot.doc.status === 'verified' ? 'Approved' : 'Approve' }}
              </button>
              <button
                class="danger"
                :disabled="docSaving === slot.doc.id || slot.doc.status === 'rejected' || uploadingType === slot.type"
                @click="startDecline(slot.doc)"
              >
                {{ slot.doc.status === 'rejected' ? 'Declined' : 'Decline' }}
              </button>
              <button
                class="secondary"
                :disabled="!!uploadingType || docSaving === slot.doc.id"
                @click="pickDocFile(slot.type)"
              >
                {{ uploadingType === slot.type ? 'Uploading…' : 'Replace…' }}
              </button>
            </div>
          </template>
          <template v-else>
            <p class="muted doc-empty-note">
              {{ slot.type === 'drivers_license' && drawerUser.role === 'driver'
                ? 'Required for driving — not uploaded yet.'
                : 'Nothing on file.' }}
            </p>
            <div class="doc-actions">
              <button
                class="secondary"
                :disabled="!!uploadingType"
                @click="pickDocFile(slot.type)"
              >
                {{ uploadingType === slot.type ? 'Uploading…' : 'Upload…' }}
              </button>
            </div>
          </template>
        </div>
      </template>
    </section>

    <p class="password-note">
      Passwords are stored as one-way hashes and can never be viewed — by
      anyone, including admins. If a user can't log in, send them a password
      reset email instead.
    </p>

    <div class="drawer-actions">
      <button
        class="secondary"
        :disabled="openingChatFor === drawerUser.id"
        @click="openLiveChat(drawerUser)"
      >💬 Live chat</button>
      <button class="secondary" @click="startEdit(drawerUser)">Edit profile</button>
      <button class="secondary" @click="askReset(drawerUser)">Send password reset</button>
      <button :class="drawerUser.is_blocked ? 'primary' : 'danger'" @click="askBlock(drawerUser)">
        {{ drawerUser.is_blocked ? 'Unblock user' : 'Block user' }}
      </button>
      <button
        v-if="!drawerUser.deleted_at"
        class="danger"
        @click="askDelete(drawerUser)"
      >Delete account</button>
      <span v-else class="deleted-note">Account deleted</span>
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

  <!-- Decline-document modal: a reason is mandatory because it is sent to
       the user in the decline notification. -->
  <div v-if="declining" class="modal-overlay" @click.self="declining = null">
    <div class="modal" role="dialog" aria-labelledby="declineDocTitle">
      <h2 id="declineDocTitle">Decline {{ docLabel(declining.type) }}?</h2>
      <p class="modal-sub">
        The user is notified with your reason and must upload a new copy
        before they can continue as a driver.
      </p>

      <label>
        Reason (visible to the user)
        <textarea
          v-model="declineReason"
          rows="3"
          maxlength="300"
          :disabled="savingDecline"
          placeholder="e.g. The photo is blurry — please retake it with all four corners visible."
        ></textarea>
      </label>

      <p v-if="declineError" class="error">{{ declineError }}</p>

      <div class="modal-actions">
        <button class="secondary" :disabled="savingDecline" @click="declining = null">Cancel</button>
        <button class="danger" :disabled="savingDecline" @click="confirmDecline">
          {{ savingDecline ? 'Declining…' : 'Decline document' }}
        </button>
      </div>
    </div>
  </div>

  <ConfirmDialog
    :open="!!pendingDelete"
    title="Delete this account?"
    :message="`${pendingDelete?.email} will be permanently signed out and anonymized: the name becomes 'Deleted User', the email and phone are FREED so the person can register a fresh account, and their rentals, purchases and chats stay on record attributed to the deleted account. This cannot be undone from the console.`"
    :confirm-label="deleting ? 'Deleting…' : 'Delete account'"
    @confirm="confirmDelete"
    @cancel="pendingDelete = null"
  />

  <ConfirmDialog
    :open="!!pendingReset"
    title="Send password reset email?"
    :message="`${pendingReset?.email} will receive an email with a link to choose a new password. Any previously sent reset links stop working. Their current password stays valid until they complete the reset.`"
    :confirm-label="sendingReset ? 'Sending…' : 'Send reset email'"
    @confirm="confirmReset"
    @cancel="pendingReset = null"
  />

  <ConfirmDialog
    :open="!!pendingSwitch"
    :title="`Switch to ${roleLabel(switchTarget)} mode?`"
    :message="`${pendingSwitch?.email} will be switched to ${roleLabel(switchTarget)} mode immediately — their app updates via live connection and push. If they don't have a ${roleLabel(switchTarget)} profile yet, one is created. Switching to Driver requires a valid driver's license on file.`"
    :confirm-label="switching ? 'Switching…' : `Switch to ${roleLabel(switchTarget)}`"
    @confirm="confirmSwitch"
    @cancel="pendingSwitch = null"
  />

  <ConfirmDialog
    :open="!!pendingBlock"
    :title="pendingBlock?.is_blocked ? 'Unblock user?' : 'Block user?'"
    :message="pendingBlock?.is_blocked
      ? `${pendingBlock?.email} will be able to log in and use the app again.`
      : `${pendingBlock?.email} will be signed out within seconds — active sessions are cut, refresh tokens revoked — and they will be unable to log back in until unblocked.`"
    :confirm-label="pendingBlock?.is_blocked ? 'Unblock' : 'Block'"
    :destructive="!pendingBlock?.is_blocked"
    @confirm="confirmBlock"
    @cancel="pendingBlock = null"
  />

  <!-- In-console document preview (licence etc.) — images inline, PDFs in an
       embedded frame; "Open in new tab" (now absolute-URL'd) as a fallback. -->
  <div v-if="docPreview" class="preview-overlay" @click.self="docPreview = null">
    <div class="preview-modal">
      <div class="preview-bar">
        <a :href="docPreview.url" target="_blank" rel="noopener" class="preview-link">Open in new tab ↗</a>
        <button type="button" class="preview-close" aria-label="Close preview" @click="docPreview = null">×</button>
      </div>
      <img v-if="docPreview.mime.startsWith('image/')" :src="docPreview.url" class="preview-img" />
      <iframe v-else :src="docPreview.url" class="preview-frame" title="Document preview" />
    </div>
  </div>
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
.deleted-note { align-self: center; font-size: 13px; color: var(--text-muted); font-style: italic; }

/* Mobile card list hidden on desktop. */
.mobile-only { display: none; }

.mode-cell { display: flex; align-items: center; gap: 10px; }
.docs-section { margin-top: 20px; padding-top: 16px; border-top: 1px solid var(--border); }
.docs-section h3 { margin: 0 0 12px; font-size: 14px; }
.muted { color: var(--text-muted); font-size: 13px; }
.doc-card {
  border: 1px solid var(--border); border-radius: 10px;
  padding: 12px; margin-bottom: 12px;
  display: flex; flex-direction: column; gap: 10px;
}
.doc-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.doc-link {
  display: block;
  width: 100%;
  padding: 0;
  border: none;
  background: none;
  cursor: pointer;
  text-align: left;
}
.doc-open-file { color: var(--accent-strong); font-size: 13px; font-weight: 600; }
.doc-image {
  display: block; width: 100%; max-height: 260px; object-fit: contain;
  border-radius: 8px; background: var(--bg); border: 1px solid var(--border);
}

/* ── In-console document preview modal (same pattern as Support.vue) ── */
.preview-overlay {
  position: fixed;
  inset: 0;
  z-index: 300;
  background: rgba(17, 24, 39, 0.6);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
}
.preview-modal {
  background: var(--surface);
  border-radius: var(--radius);
  width: min(900px, 92vw);
  height: min(90vh, 1000px);
  display: flex;
  flex-direction: column;
  overflow: hidden;
  box-shadow: 0 20px 60px rgba(0, 0, 0, 0.35);
}
.preview-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 14px;
  border-bottom: 1px solid var(--border);
  flex-shrink: 0;
}
.preview-link { font-size: 13px; color: var(--accent-strong); text-decoration: none; }
.preview-link:hover { text-decoration: underline; }
.preview-close {
  width: 32px; height: 32px;
  border: none;
  background: transparent;
  font-size: 22px;
  line-height: 1;
  color: var(--text-muted);
  cursor: pointer;
  border-radius: 6px;
}
.preview-close:hover { background: var(--bg); color: var(--text); }
.preview-frame { flex: 1; width: 100%; border: none; }
.preview-img { flex: 1; width: 100%; object-fit: contain; background: var(--bg); min-height: 0; }
.doc-actions { display: flex; gap: 8px; justify-content: flex-end; }
.doc-empty-note { margin: 4px 0 8px; font-size: 13px; }
.modal textarea {
  padding: 10px 12px; border: 1px solid var(--border); border-radius: 8px;
  font-size: 14px; background: var(--bg, #fff); color: var(--text, #111);
  resize: vertical; font-family: inherit;
}
.modal textarea:focus { outline: 2px solid var(--accent, #2bd1c4); outline-offset: -1px; }

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

/* ── Phone: 8-column table → stacked cards (≤640px) ───────────
   Shared :root --m-row-* rhythm, matching the other mobile lists. Each card
   opens the full-screen Drawer, where every action lives — no sideways table
   scroll to reach Block/Chat/Reset. Desktop keeps the table. */
@media (max-width: 640px) {
  .desktop-only { display: none; }
  .mobile-only {
    display: block;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .filters { flex-wrap: wrap; }
  .search { flex-basis: 100%; max-width: none; }

  .list-card {
    display: block;
    width: 100%;
    text-align: left;
    background: transparent;
    border: none;
    border-bottom: 1px solid var(--border);
    padding: var(--m-row-py) var(--m-row-px);
    cursor: pointer;
  }
  .list-card:last-of-type { border-bottom: none; }
  .lc-top { display: flex; justify-content: space-between; align-items: center; gap: 8px; margin-bottom: 5px; }
  .lc-title { font-weight: 600; font-size: 15px; }
  .lc-name { font-size: 14px; color: var(--text); word-break: break-all; }
  .lc-sub { font-size: 12px; color: var(--text-muted); margin-top: 2px; }
  .card-state { padding: 32px; text-align: center; color: var(--text-muted); }

  .mobile-pager {
    display: flex; flex-direction: column; align-items: center; gap: 8px;
    padding: 12px; border-top: 1px solid var(--border);
    color: var(--text-muted); font-size: 13px;
  }
  .mobile-pager .pager { display: flex; align-items: center; gap: 12px; }
  .mobile-pager .pager button { min-height: 44px; min-width: 72px; }
}

/* User holds both driver and owner profiles (7/31 batch item 1). */
.both-badge {
  display: inline-block;
  margin-left: 6px;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 600;
  background: #4ecdc422;
  color: #0d9488;
  white-space: nowrap;
}
</style>
