<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { RouterView, RouterLink, useRouter, useRoute } from 'vue-router'
import { useAuthStore } from '../stores/auth'
import { useSupportStore } from '../stores/support'

const auth = useAuthStore()
const router = useRouter()
const route = useRoute()
const support = useSupportStore()

const items = [
  { to: '/users',     label: 'Users',     icon: 'user' },
  { to: '/vehicles',  label: 'Vehicles',  icon: 'car' },
  { to: '/chats',     label: 'Chats',     icon: 'chat' },
  { to: '/rents',     label: 'Rents',     icon: 'rent' },
  { to: '/support',   label: 'Support',   icon: 'support' },
  { to: '/accidents', label: 'Accidents', icon: 'accident' },
  { to: '/car-sell',  label: 'Car Sell',  icon: 'sell' },
  { to: '/purchases', label: 'Purchases', icon: 'purchase' },
]

// Mobile drawer state. Sidebar is hidden by default on small viewports and
// opened on demand via the hamburger button in the top bar.
const drawerOpen = ref(false)
const hamburgerRef = ref<HTMLButtonElement | null>(null)
const closeBtnRef  = ref<HTMLButtonElement | null>(null)

function openDrawer() {
  drawerOpen.value = true
  // Move focus into the drawer so keyboard / screen-reader users land there
  // after activating the hamburger.
  nextTick(() => closeBtnRef.value?.focus())
}
function closeDrawer() {
  drawerOpen.value = false
  // Return focus to the trigger so keyboard users don't lose their place.
  nextTick(() => hamburgerRef.value?.focus())
}

// Close the drawer on navigation so the new page is visible.
watch(() => route.fullPath, () => { drawerOpen.value = false })

// Escape closes the drawer (only while it's open, to avoid stealing Esc on
// the desktop layout where there's no drawer at all).
function onKeydown(e: KeyboardEvent) {
  if (drawerOpen.value && e.key === 'Escape') {
    e.preventDefault()
    closeDrawer()
  }
}

// Current page title for the mobile top bar — driven off the route name.
function pageTitle(): string {
  const name = String(route.name || '')
  if (!name) return 'Admin'
  if (name === 'car-sell') return 'Car Sell'
  if (name === 'purchases') return 'Purchases'
  return name.charAt(0).toUpperCase() + name.slice(1)
}

onMounted(() => {
  support.connect()
  document.addEventListener('keydown', onKeydown)
})
onUnmounted(() => {
  support.disconnect()
  document.removeEventListener('keydown', onKeydown)
})

function logout() {
  support.disconnect()
  auth.logout()
  router.replace({ name: 'login' })
}
</script>

<template>
  <div class="layout" :class="{ 'drawer-open': drawerOpen }">
    <!-- Mobile-only top bar: hamburger + page title. Hidden on desktop. -->
    <header class="topbar">
      <button
        ref="hamburgerRef"
        class="hamburger"
        type="button"
        aria-label="Open menu"
        aria-controls="admin-sidebar"
        :aria-expanded="drawerOpen"
        @click="openDrawer"
      >
        <span /><span /><span />
      </button>
      <div class="topbar-title">{{ pageTitle() }}</div>
      <div class="topbar-spacer" />
    </header>

    <!-- Backdrop dims the page when the mobile drawer is open. -->
    <div
      v-if="drawerOpen"
      class="backdrop"
      aria-hidden="true"
      @click="closeDrawer"
    />

    <aside
      id="admin-sidebar"
      class="sidebar"
      :class="{ open: drawerOpen }"
      role="navigation"
      aria-label="Primary"
    >
      <button
        ref="closeBtnRef"
        class="ghost close-drawer"
        type="button"
        aria-label="Close menu"
        @click="closeDrawer"
      >×</button>

      <div class="brand">
        <img v-if="auth.profile?.profile_photo_url" :src="auth.profile.profile_photo_url" alt="" />
        <div v-else class="avatar-placeholder" />
        <div class="brand-text">
          <div class="brand-title">Admin</div>
          <div class="brand-sub">{{ auth.profile?.email }}</div>
        </div>
      </div>

      <nav>
        <RouterLink v-for="i in items" :key="i.to" :to="i.to" class="nav-item" active-class="active">
          <span class="nav-icon" :data-icon="i.icon" />
          <span>{{ i.label }}</span>
          <span v-if="i.icon === 'support' && support.totalUnread > 0" class="badge">
            {{ support.totalUnread > 99 ? '99+' : support.totalUnread }}
          </span>
        </RouterLink>
      </nav>

      <button class="logout ghost" @click="logout">Sign out</button>
    </aside>

    <main
      class="main"
      :aria-hidden="drawerOpen ? 'true' : undefined"
      :inert="drawerOpen ? true : undefined"
    >
      <RouterView />
    </main>
  </div>
</template>

<style scoped>
.layout {
  display: flex;
  min-height: 100vh;
  background: var(--surface);
}

/* ---- Mobile top bar (hidden on desktop) ---- */
.topbar {
  display: none;
  position: sticky;
  top: 0;
  z-index: 50;
  background: var(--surface);
  border-bottom: 1px solid var(--border);
  padding: 8px 12px;
  align-items: center;
  gap: 8px;
  height: 52px;
}
.topbar-title {
  flex: 1;
  text-align: center;
  font-weight: 600;
  font-size: 16px;
  color: var(--text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.topbar-spacer { width: 44px; }
.hamburger {
  width: 44px; height: 44px;
  display: inline-flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 4px;
  background: transparent;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 0;
  flex-shrink: 0;
}
.hamburger span {
  display: block;
  width: 18px; height: 2px;
  background: var(--text);
  border-radius: 2px;
}
.close-drawer {
  display: none;
  position: absolute;
  top: 8px; right: 8px;
  font-size: 24px;
  line-height: 1;
  padding: 4px 10px;
}

/* ---- Backdrop ---- */
.backdrop {
  display: none;
  position: fixed;
  inset: 0;
  background: rgba(17, 24, 39, 0.45);
  z-index: 60;
}

.sidebar {
  width: 260px;
  flex-shrink: 0;
  background: var(--surface);
  border-right: 1px solid var(--border);
  padding: 24px 16px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.brand {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 8px 12px 16px;
  border-bottom: 1px solid var(--border);
  margin-bottom: 16px;
}
.brand img, .avatar-placeholder {
  width: 36px; height: 36px;
  border-radius: 50%;
  background: var(--bg);
  border: 1px solid var(--border);
  object-fit: cover;
}
.brand-title { font-weight: 600; }
.brand-sub { font-size: 12px; color: var(--text-muted); overflow: hidden; text-overflow: ellipsis; max-width: 170px; white-space: nowrap; }

nav { display: flex; flex-direction: column; gap: 2px; }
.nav-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 12px;
  border-radius: var(--radius);
  color: var(--text-muted);
  font-weight: 500;
}
.nav-item:hover { background: var(--bg); color: var(--text); text-decoration: none; }
.nav-item.active { color: var(--accent-strong); background: var(--accent-soft); }

.nav-icon {
  width: 18px; height: 18px;
  background-color: currentColor;
  -webkit-mask-position: center; mask-position: center;
  -webkit-mask-repeat: no-repeat; mask-repeat: no-repeat;
  -webkit-mask-size: contain; mask-size: contain;
}
.nav-icon[data-icon="user"]     { -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2'%3E%3Ccircle cx='12' cy='8' r='4'/%3E%3Cpath d='M4 21v-1a8 8 0 0116 0v1'/%3E%3C/svg%3E"); mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2'%3E%3Ccircle cx='12' cy='8' r='4'/%3E%3Cpath d='M4 21v-1a8 8 0 0116 0v1'/%3E%3C/svg%3E"); }
.nav-icon[data-icon="car"]      { -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2'%3E%3Cpath d='M3 13l2-5a2 2 0 012-2h10a2 2 0 012 2l2 5'/%3E%3Crect x='3' y='13' width='18' height='6' rx='2'/%3E%3Ccircle cx='7' cy='19' r='1'/%3E%3Ccircle cx='17' cy='19' r='1'/%3E%3C/svg%3E"); mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2'%3E%3Cpath d='M3 13l2-5a2 2 0 012-2h10a2 2 0 012 2l2 5'/%3E%3Crect x='3' y='13' width='18' height='6' rx='2'/%3E%3Ccircle cx='7' cy='19' r='1'/%3E%3Ccircle cx='17' cy='19' r='1'/%3E%3C/svg%3E"); }
.nav-icon[data-icon="chat"]     { -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2'%3E%3Cpath d='M21 12a8 8 0 11-3-6L21 4l-1 5a8 8 0 011 3z'/%3E%3C/svg%3E"); mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2'%3E%3Cpath d='M21 12a8 8 0 11-3-6L21 4l-1 5a8 8 0 011 3z'/%3E%3C/svg%3E"); }
.nav-icon[data-icon="rent"]     { -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2'%3E%3Crect x='3' y='5' width='18' height='14' rx='2'/%3E%3Cpath d='M3 9h18'/%3E%3C/svg%3E"); mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2'%3E%3Crect x='3' y='5' width='18' height='14' rx='2'/%3E%3Cpath d='M3 9h18'/%3E%3C/svg%3E"); }
.nav-icon[data-icon="support"]  { -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2'%3E%3Ccircle cx='12' cy='12' r='9'/%3E%3Cpath d='M9.5 9a2.5 2.5 0 015 0c0 1.5-2.5 2-2.5 4'/%3E%3Ccircle cx='12' cy='17' r='0.5' fill='currentColor'/%3E%3C/svg%3E"); mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2'%3E%3Ccircle cx='12' cy='12' r='9'/%3E%3Cpath d='M9.5 9a2.5 2.5 0 015 0c0 1.5-2.5 2-2.5 4'/%3E%3Ccircle cx='12' cy='17' r='0.5' fill='currentColor'/%3E%3C/svg%3E"); }
.nav-icon[data-icon="accident"] { -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2'%3E%3Cpath d='M12 3l10 18H2L12 3z'/%3E%3Cpath d='M12 10v4'/%3E%3Ccircle cx='12' cy='17' r='0.5' fill='currentColor'/%3E%3C/svg%3E"); mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2'%3E%3Cpath d='M12 3l10 18H2L12 3z'/%3E%3Cpath d='M12 10v4'/%3E%3Ccircle cx='12' cy='17' r='0.5' fill='currentColor'/%3E%3C/svg%3E"); }
.nav-icon[data-icon="sell"]     { -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2'%3E%3Cpath d='M12 1v22M17 5H9.5a3.5 3.5 0 000 7h5a3.5 3.5 0 010 7H6'/%3E%3C/svg%3E"); mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2'%3E%3Cpath d='M12 1v22M17 5H9.5a3.5 3.5 0 000 7h5a3.5 3.5 0 010 7H6'/%3E%3C/svg%3E"); }
.nav-icon[data-icon="purchase"] { -webkit-mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2'%3E%3Cpath d='M6 6h15l-1.5 9h-12z'/%3E%3Cpath d='M6 6L4 3H2'/%3E%3Ccircle cx='9' cy='20' r='1.5'/%3E%3Ccircle cx='18' cy='20' r='1.5'/%3E%3C/svg%3E"); mask-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2'%3E%3Cpath d='M6 6h15l-1.5 9h-12z'/%3E%3Cpath d='M6 6L4 3H2'/%3E%3Ccircle cx='9' cy='20' r='1.5'/%3E%3Ccircle cx='18' cy='20' r='1.5'/%3E%3C/svg%3E"); }

.badge {
  margin-left: auto;
  background: #e53e3e;
  color: #fff;
  font-size: 11px;
  font-weight: 700;
  line-height: 1;
  padding: 3px 6px;
  border-radius: 999px;
  min-width: 18px;
  text-align: center;
}

.logout { margin-top: auto; text-align: left; }

.main {
  flex: 1;
  padding: 32px 40px;
  overflow-x: auto;
  min-width: 0;
}

/* ---- Mobile breakpoint: collapse sidebar into a slide-in drawer ---- */
@media (max-width: 768px) {
  .layout {
    flex-direction: column;
  }
  .topbar { display: flex; }
  .close-drawer { display: block; }

  .sidebar {
    position: fixed;
    top: 0; left: 0;
    height: 100vh;
    width: min(280px, 82vw);
    z-index: 70;
    transform: translateX(-100%);
    transition: transform 200ms ease;
    box-shadow: 4px 0 24px rgba(0, 0, 0, 0.08);
    padding-top: 56px;
  }
  .sidebar.open { transform: translateX(0); }

  .layout.drawer-open .backdrop { display: block; }

  .main {
    padding: 16px 14px 24px;
    width: 100%;
  }
}

@media (max-width: 380px) {
  .topbar { padding: 6px 8px; gap: 6px; }
  .topbar-title { font-size: 15px; }
  .main { padding: 14px 12px 24px; }
}
</style>
