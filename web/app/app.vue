<script setup lang="ts">
const attention = useAttention()
const admin = useWorkbenchToken()
const identity = useIdentity()
const api = useSessions()
useAttentionHead()

function begin() {
  attention.start()
  identity.ensureDefault(api.whoami)
}

onMounted(() => {
  if (admin.hasToken.value) begin()
})
watch(
  () => admin.hasToken.value,
  (has) => {
    if (has) begin()
  },
)
</script>

<template>
  <UApp>
    <NuxtLayout>
      <NuxtPage />
    </NuxtLayout>
    <ZoomIndicator />
  </UApp>
</template>
