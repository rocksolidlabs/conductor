<script setup lang="ts">
import { listLeavesFor, sessionWorthOpening } from '~/utils/sessions'

// The phone's home screen (design 3e): the list is the first screen, not a
// drawer. The filter on top, then the sessions and runs as the sidebar lists
// them, prompts answered in place with full-width buttons; a row opens its
// page, Back returns here. At lg and up the sidebar is that list already, so
// this route does what the home does there: the first session worth looking
// at (someone waiting, else the newest running one).
useHead({ title: 'Sessions' })

const attention = useAttention()
const admin = useWorkbenchToken()
const wide = useMedia('(min-width: 64rem)')

const target = computed(() => sessionWorthOpening(attention.sessions.value))
watch(
  [target, wide],
  ([t, w]) => {
    // At lg and up this page always leaves: the sidebar is the list, so staying would show it twice (#359). The home
    // decides what shows then: the session worth looking at, else its welcome.
    const to = listLeavesFor(w, t)
    if (to) navigateTo(to, { replace: true })
  },
  { immediate: true },
)

onMounted(() => {
  if (!admin.hasToken.value) admin.needsToken.value = true
  attention.start()
})
</script>

<template>
  <UDashboardPanel id="sessions" :ui="{ body: 'p-0 sm:p-0 flex flex-col min-h-0 gap-0' }">
    <template #header>
      <UDashboardNavbar title="Conductor" :toggle="false" :ui="{ root: 'h-14' }">
        <template #leading>
          <img src="/brand/conductor-mark.svg" alt="" class="size-6 dark:hidden" />
          <img src="/brand/conductor-mark-reversed.svg" alt="" class="size-6 hidden dark:block" />
        </template>
        <template #right>
          <SidebarHeaderMenus @token="admin.needsToken.value = true" />
        </template>
      </UDashboardNavbar>
    </template>
    <template #body>
      <UAlert v-if="attention.error.value" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="attention.error.value" class="m-3" :actions="[{ label: 'Set token', onClick: () => (admin.needsToken.value = true) }]" />
      <div class="flex min-h-0 flex-1 flex-col px-2 pt-2">
        <SessionSidebar page />
      </div>
    </template>
  </UDashboardPanel>
</template>
