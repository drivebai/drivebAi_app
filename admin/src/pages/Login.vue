<script setup lang="ts">
import { ref } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const router = useRouter()
const route = useRoute()

const email = ref('')
const password = ref('')

async function submit() {
  try {
    await auth.login(email.value.trim(), password.value)
    const redirect = (route.query.redirect as string) || '/users'
    router.replace(redirect)
  } catch {
    /* error already on auth.error */
  }
}
</script>

<template>
  <div class="page">
    <form class="card" @submit.prevent="submit">
      <h1>DriveBai Admin</h1>
      <p class="sub">Sign in with an admin account.</p>

      <label for="email">Email</label>
      <input id="email" v-model="email" type="email" autocomplete="username" required />

      <label for="password">Password</label>
      <input id="password" v-model="password" type="password" autocomplete="current-password" required />

      <p v-if="auth.error" class="error">{{ auth.error }}</p>

      <button class="primary" type="submit" :disabled="auth.loading">
        {{ auth.loading ? 'Signing in…' : 'Sign in' }}
      </button>
    </form>
  </div>
</template>

<style scoped>
.page {
  min-height: 100vh;
  background: var(--bg);
  display: flex; align-items: center; justify-content: center;
  padding: 24px;
}
.card {
  width: 100%;
  max-width: 380px;
  background: var(--surface);
  padding: 32px;
  border-radius: 12px;
  box-shadow: 0 8px 24px rgba(0,0,0,0.06);
}
h1 { margin: 0 0 4px; font-size: 22px; }
.sub { margin: 0 0 24px; color: var(--text-muted); }
label { margin-top: 14px; }
button { margin-top: 20px; width: 100%; padding: 10px; }
.error {
  margin-top: 14px;
  background: var(--danger-soft);
  color: var(--danger);
  padding: 8px 12px;
  border-radius: var(--radius);
  font-size: 13px;
}
</style>
