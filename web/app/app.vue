<script setup lang="ts">
import { shellBridge } from '~/utils/shell'

// Inside the phone shell the workbench's stream and its token are never set up (docs/plans/phone-shell.md, OSS-1b).
const inShell = shellBridge() !== null
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
  if (!inShell && admin.hasToken.value) begin()
})
watch(
  () => admin.hasToken.value,
  (has) => {
    if (has && !inShell) begin()
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
