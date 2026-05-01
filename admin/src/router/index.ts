import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '../stores/auth'

const router = createRouter({
  history: createWebHistory('/admin/'),
  routes: [
    {
      path: '/login',
      name: 'login',
      component: () => import('../pages/Login.vue'),
      meta: { public: true },
    },
    {
      path: '/',
      component: () => import('../layouts/AdminLayout.vue'),
      children: [
        { path: '', redirect: { name: 'users' } },
        { path: 'users',     name: 'users',     component: () => import('../pages/Users.vue') },
        { path: 'vehicles',  name: 'vehicles',  component: () => import('../pages/Vehicles.vue') },
        { path: 'chats',     name: 'chats',     component: () => import('../pages/Chats.vue') },
        { path: 'rents',     name: 'rents',     component: () => import('../pages/Rents.vue') },
        { path: 'support',   name: 'support',   component: () => import('../pages/Support.vue') },
        { path: 'accidents', name: 'accidents', component: () => import('../pages/Accidents.vue') },
        { path: 'car-sell',  name: 'car-sell',  component: () => import('../pages/CarSell.vue') },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: { name: 'users' } },
  ],
})

router.beforeEach((to) => {
  if (to.meta.public) return true
  const auth = useAuthStore()
  // bootstrap() runs in App.vue onMounted, but the very first navigation may
  // fire before that. Re-hydrate inline if needed.
  if (!auth.profile) auth.bootstrap()
  if (!auth.isAuthenticated) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  return true
})

export default router
