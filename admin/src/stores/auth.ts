// Auth store. The admin login reuses the existing /api/v1/auth/login endpoint.
// The backend rejects non-admins server-side via RequireRole; we ALSO check the
// returned profile here so a wrong-role user gets a clear "not an admin" message
// instead of bouncing 401s on every subsequent call.

import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api, getToken, setToken, ApiError } from '../api/client'

interface UserProfile {
  id: string
  email: string
  role: string
  first_name: string
  last_name: string
  profile_photo_url?: string | null
}

interface LoginResponse {
  access_token: string
  refresh_token?: string
  user: UserProfile
}

const PROFILE_KEY = 'drivebai.admin.profile'

export const useAuthStore = defineStore('auth', () => {
  const profile = ref<UserProfile | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)

  const isAuthenticated = computed(() => !!getToken() && profile.value?.role === 'admin')

  async function login(email: string, password: string) {
    loading.value = true
    error.value = null
    try {
      const res = await api.post<LoginResponse>('/api/v1/auth/login', { email, password })
      if (res.user.role !== 'admin') {
        // Don't keep a token we can't use anywhere.
        setToken(null)
        throw new ApiError('This account is not an admin.', 403, 'NOT_ADMIN')
      }
      setToken(res.access_token)
      profile.value = res.user
      localStorage.setItem(PROFILE_KEY, JSON.stringify(res.user))
    } catch (e) {
      error.value = e instanceof ApiError ? e.message : 'Login failed'
      throw e
    } finally {
      loading.value = false
    }
  }

  function logout() {
    setToken(null)
    profile.value = null
    localStorage.removeItem(PROFILE_KEY)
  }

  /** Re-hydrate from localStorage on app boot so refreshes don't kick admins out. */
  function bootstrap() {
    const raw = localStorage.getItem(PROFILE_KEY)
    if (raw) {
      try { profile.value = JSON.parse(raw) } catch { /* ignore */ }
    }
  }

  return { profile, loading, error, isAuthenticated, login, logout, bootstrap }
})
