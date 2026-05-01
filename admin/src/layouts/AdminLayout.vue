<script setup lang="ts">
import { RouterView, RouterLink, useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const router = useRouter()

const items = [
  { to: '/users',     label: 'Users',     icon: 'user' },
  { to: '/vehicles',  label: 'Vehicles',  icon: 'car' },
  { to: '/chats',     label: 'Chats',     icon: 'chat' },
  { to: '/rents',     label: 'Rents',     icon: 'rent' },
  { to: '/support',   label: 'Support',   icon: 'support' },
  { to: '/accidents', label: 'Accidents', icon: 'accident' },
  { to: '/car-sell',  label: 'Car Sell',  icon: 'sell' },
]

function logout() {
  auth.logout()
  router.replace({ name: 'login' })
}
</script>

<template>
  <div class="layout">
    <aside class="sidebar">
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
        </RouterLink>
      </nav>

      <button class="logout ghost" @click="logout">Sign out</button>
    </aside>

    <main class="main">
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

.logout { margin-top: auto; text-align: left; }

.main {
  flex: 1;
  padding: 32px 40px;
  overflow-x: auto;
  min-width: 0;
}
</style>
