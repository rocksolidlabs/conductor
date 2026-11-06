<script setup lang="ts">
import { accountItems } from '~/utils/sidebarActions'

/**
 * The phone's bar at the foot (design 3e): Decide first (Needs your
 * decision, with the number of items waiting: Owner decisions 10 of
 * docs/plans/decision-inbox.md), Sessions, Yard (with its count), Crews;
 * Roundhouse, Agents, Dispatch, Events, Settings, Alerts and your menu under
 * More. Every target is at least 44 px tall; the padding reads the safe
 * area under the home indicator. Below lg only: the one root hides itself
 * at lg and up (a class from the layout would not reach several roots).
 */
const emit = defineEmits<{ token: [] }>()
const attention = useAttention()
const inbox = useInbox()
const desktop = useDesktop()
const route = useRoute()
const { hasToken, clear } = useWorkbenchToken()
const shortcuts = useShortcutsModal()
const colorMode = useColorMode()

const tabs = computed(() => [
  { key: 'decide', label: 'Decide', to: '/decisions', icon: 'i-lucide-gavel', count: inbox.count.value },
  { key: 'sessions', label: 'Sessions', to: '/sessions', icon: 'i-lucide-list', count: 0 },
  { key: 'yard', label: 'Yard', to: '/yard', icon: 'i-lucide-layout-grid', count: attention.count.value },
  { key: 'crews', label: 'Crews', to: '/crews', icon: 'i-lucide-users', count: 0 },
])
function active(to: string): boolean {
  if (to === '/sessions') return route.path === '/sessions' || route.path.startsWith('/sessions/') || route.path === '/'
  if (to === '/crews') return route.path.startsWith('/crews') || route.path.startsWith('/runs/')
  return route.path.startsWith(to)
}

const more = ref(false)
const nameOpen = ref(false)
const pages = computed(() => [
  { label: 'Roundhouse', icon: 'i-lucide-gallery-horizontal', to: '/roundhouse' },
  { label: 'Agents', icon: 'i-lucide-bot', to: '/agents' },
  { label: 'Dispatch', icon: 'i-lucide-ticket', to: '/dispatch' },
  { label: 'Events', icon: 'i-lucide-radio-tower', to: '/events' },
  ...(desktop.isDesktop.value ? [{ label: 'Settings', icon: 'i-lucide-settings', to: '/settings' }] : []),
])
/** Your menu's items as buttons, the sheet closed before each acts. */
const account = computed(() =>
  accountItems(hasToken.value, {
    name: () => (nameOpen.value = true),
    shortcuts: () => shortcuts.show(),
    theme: () => (colorMode.preference = colorMode.value === 'dark' ? 'light' : 'dark'),
    token: () => emit('token'),
    forget: () => clear(),
  }).flat(),
)
function act(fn?: (e: Event) => void) {
  more.value = false
  fn?.(new Event('select'))
}
</script>

<template>
  <div class="lg:hidden">
  <nav class="fixed inset-x-0 bottom-0 z-40 flex border-t border-default bg-default pb-[env(safe-area-inset-bottom)]" aria-label="Pages" data-bottom-bar>
    <NuxtLink
      v-for="t in tabs"
      :key="t.key"
      :to="t.to"
      class="flex min-h-11 flex-1 flex-col items-center justify-center gap-0.5 text-[10px] font-medium"
      :class="active(t.to) ? 'text-primary' : 'text-muted'"
      :aria-current="active(t.to) ? 'page' : undefined"
      :data-bottom-tab="t.key"
    >
      <span class="relative">
        <UIcon :name="t.icon" class="size-5" />
        <span v-if="t.count" class="absolute -right-2.5 -top-1.5 rounded-full bg-warning px-1 text-[9px] font-semibold leading-3.5 text-inverted" data-bottom-count>{{ t.count }}</span>
      </span>
      {{ t.label }}
    </NuxtLink>
    <button type="button" class="flex min-h-11 flex-1 flex-col items-center justify-center gap-0.5 text-[10px] font-medium text-muted" :class="more && 'text-primary'" data-bottom-tab="more" @click="more = true">
      <UIcon name="i-lucide-ellipsis" class="size-5" />
      More
    </button>
  </nav>
  <UDrawer v-model:open="more" direction="bottom" title="More" close>
    <template #body>
      <div class="flex flex-col gap-4" data-bottom-more>
        <div class="flex flex-col">
          <UButton v-for="p in pages" :key="p.to" :label="p.label" :icon="p.icon" :to="p.to" color="neutral" variant="ghost" size="lg" class="min-h-11 justify-start" @click="more = false" />
        </div>
        <AlertSwitches />
        <div class="flex flex-col border-t border-default pt-2">
          <UButton v-for="i in account" :key="i.label" :label="i.label" :icon="i.icon" :color="i.color ?? 'neutral'" variant="ghost" size="lg" class="min-h-11 justify-start" @click="act(i.onSelect)" />
        </div>
      </div>
    </template>
  </UDrawer>
  <NameDialog v-model:open="nameOpen" />
  </div>
</template>
