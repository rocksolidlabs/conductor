<script setup lang="ts">
import { accountItems } from '~/utils/sidebarActions'
import { pageActive, phoneMore, phoneTabs, type PageId } from '~/utils/pages'
import { needsCount } from '~/utils/needsYou'
import { isActive } from '~/utils/attention'

/**
 * The phone's bar at the foot (design 3e): Needs you first (with the one
 * number of what waits), Sessions, Yard (the asking sessions on its grid,
 * in neutral ink), Crews; Dispatch, Brakeman (while the server has it),
 * Agents, History, Settings, Alerts and your menu under More, lit
 * while one of its pages is shown, Brakeman's row with the briefs not
 * yet opened; the alerts end with "All alert rules", the Alerts panel. Every target is at
 * least 44 px tall; the padding reads the safe
 * area under the home indicator. Below lg only: the one root hides itself
 * at lg and up (a class from the layout would not reach several roots).
 */
const emit = defineEmits<{ token: [] }>()
const attention = useAttention()
const desktop = useDesktop()
const brakeman = useBrakeman()
const route = useRoute()
const { hasToken, clear } = useWorkbenchToken()
const shortcuts = useShortcutsModal()
const colorMode = useColorMode()

// The bar's tabs and More's pages, from the one table (utils/pages.ts); the hook a test finds Decide's tab by stays `decide`.
const TAB_HOOKS: Partial<Record<PageId, string>> = { decisions: 'decide' }
// Needs you's tab carries the one number in amber (useNeedsYou; nothing while it is not known); the Yard's counts the asking sessions
// on its grid, as the Yard's chip does, in neutral ink: the phone's one amber number is Needs you's.
const needs = useNeedsYou()
const gridAsks = computed(() => attention.needsInput.value.filter(isActive).length)
const tabs = computed(() =>
  phoneTabs().map((p) => ({
    key: TAB_HOOKS[p.id] ?? p.id,
    id: p.id,
    label: p.label,
    to: p.to,
    icon: p.icon,
    count: p.id === 'decisions' ? (needs.number.value ? needsCount(needs.list.value) : '') : p.id === 'yard' && gridAsks.value ? String(gridAsks.value) : '',
    amber: p.id === 'decisions',
  })),
)
function active(id: PageId): boolean {
  return pageActive(id, route.path)
}

const more = ref(false)
const nameOpen = ref(false)
const pages = computed(() => phoneMore({ desktop: desktop.isDesktop.value, brakeman: brakeman.available.value === true }))
/** More is lit while the page shown is one of its own, as a tab is while its page is shown. */
const moreActive = computed(() => pages.value.some((p) => active(p.id)))
/** The briefs not yet opened, on Brakeman's row in neutral ink: the phone's one amber number is Needs you's. */
const brakemanCount = computed(() => brakeman.unopened.value.length)
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
const alertsPanel = useAlertsPanel()
const moreButton = useTemplateRef<HTMLButtonElement>('moreButton')
/** All alert rules: the sheet closes, then the layout's Alerts panel opens; closed, it gives the focus to More. */
async function openRules() {
  more.value = false
  await nextTick()
  alertsPanel.show(moreButton.value)
}
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
      :class="active(t.id) ? 'text-primary' : 'text-muted'"
      :aria-current="active(t.id) ? 'page' : undefined"
      :data-bottom-tab="t.key"
    >
      <span class="relative">
        <UIcon :name="t.icon" class="size-5" />
        <span
          v-if="t.count"
          class="absolute -right-2.5 -top-1.5 rounded-full px-1 text-[9px] font-semibold leading-3.5"
          :class="t.amber ? 'bg-warning text-(--ui-color-neutral-900)' : 'bg-elevated text-default ring ring-default'"
          :data-count-of="t.amber ? 'needs' : undefined"
          data-bottom-count
          >{{ t.count }}</span
        >
      </span>
      {{ t.label }}
    </NuxtLink>
    <button
      ref="moreButton"
      type="button"
      class="flex min-h-11 flex-1 flex-col items-center justify-center gap-0.5 text-[10px] font-medium"
      :class="more || moreActive ? 'text-primary' : 'text-muted'"
      :aria-current="moreActive ? 'page' : undefined"
      data-bottom-tab="more"
      @click="more = true"
    >
      <UIcon name="i-lucide-ellipsis" class="size-5" />
      More
    </button>
  </nav>
  <UDrawer v-model:open="more" direction="bottom" title="More" close>
    <template #body>
      <div class="flex flex-col gap-4" data-bottom-more>
        <div class="flex flex-col">
          <UButton v-for="p in pages" :key="p.to" :label="p.label" :icon="p.icon" :to="p.to" color="neutral" variant="ghost" size="lg" class="min-h-11 justify-start" :data-more-page="p.id" @click="more = false">
            <template v-if="p.id === 'brakeman' && brakemanCount" #trailing>
              <UBadge :label="String(brakemanCount)" color="neutral" variant="subtle" size="sm" :aria-label="`${brakemanCount} new ${brakemanCount === 1 ? 'brief' : 'briefs'}`" data-more-count />
            </template>
          </UButton>
        </div>
        <AlertSwitches rules @rules="openRules" />
        <div class="flex flex-col border-t border-default pt-2">
          <UButton v-for="i in account" :key="i.label" :label="i.label" :icon="i.icon" :color="i.color ?? 'neutral'" variant="ghost" size="lg" class="min-h-11 justify-start" @click="act(i.onSelect)" />
        </div>
      </div>
    </template>
  </UDrawer>
  <NameDialog v-model:open="nameOpen" />
  </div>
</template>
