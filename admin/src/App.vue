<script setup lang="ts">
import { onMounted } from 'vue'
import { useAuthStore } from './stores/auth'
import Toasts from './components/Toasts.vue'

const auth = useAuthStore()
onMounted(() => auth.bootstrap())
</script>

<template>
  <RouterView />
  <Toasts />
</template>
