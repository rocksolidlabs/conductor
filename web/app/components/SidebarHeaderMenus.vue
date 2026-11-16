<script setup lang="ts">
import type { DropdownMenuItem } from '@nuxt/ui'
import { initials } from '~/utils/sessions'
import { accountItems } from '~/utils/sidebarActions'

/**
 * Beside the name in the sidebar's header (design 3b): your menu behind your initials (an amber chip while no workbench token is
 * set): Your name…, then the sidebar's own Alerts… (AlertsPanel) and Collapse to the rail (⌘B; not on the rail, where the foot's
 * Expand button is), then Keyboard shortcuts, Toggle theme, Workbench token…, Forget token. The foot then holds only the pages. On
 * the rail it sits in the foot, its tooltip and menu to the right. On the phone's list (`page`) the two sidebar items are not
 * there: its More shows the switches, and it has no rail.
 */
const props = withDefaults(defineProps<{ collapsed?: boolean; page?: boolean }>(), { collapsed: false, page: false })
/** Asks the layout for its workbench token dialog, and to fold the sidebar to the rail. */
const emit = defineEmits<{ token: []; collapse: [] }>()

const alerts = useAttentionSettings()
const alertsPanel = useAlertsPanel()
// The update the desktop app downloaded (#290): a button here until it installs, which asks for the restart.
const { ready: updateReady, restartFor } = useDesktopUpdate()
const identity = useIdentity()
const { hasToken, clear } = useWorkbenchToken()
const shortcuts = useShortcutsModal()
const colorMode = useColorMode()
const account = useTemplateRef<{ $el: HTMLElement }>('account')

const nameOpen = ref(false)
function openName() {
  nameOpen.value = true
}
function toggleTheme() {
  colorMode.preference = colorMode.value === 'dark' ? 'light' : 'dark'
}
/** The sidebar's own items, after Your name: never in `accountItems`, which the phone's More and the palette show too. */
const sidebarItems = computed<DropdownMenuItem[]>(() => {
  if (props.page) return []
  const out: DropdownMenuItem[] = [{ label: 'Alerts…', icon: alerts.settings.value.notifications ? 'i-lucide-bell-ring' : 'i-lucide-bell', onSelect: () => alertsPanel.show(account.value?.$el) }]
  if (!props.collapsed) out.push({ label: 'Collapse to the rail', icon: 'i-lucide-panel-left-close', kbds: ['meta', 'b'], onSelect: () => emit('collapse') })
  return out
})
const phone = useOpenOnPhone()
const items = computed(() => {
  const [name = [], ...rest] = accountItems(hasToken.value, { name: openName, shortcuts: () => shortcuts.show(), theme: toggleTheme, token: () => emit('token'), forget: () => clear(), phone: phone.show })
  return sidebarItems.value.length ? [name, sidebarItems.value, ...rest] : [name, ...rest]
})
const side = computed(() => (props.collapsed ? { side: 'right' as const } : undefined))
const who = computed(() => (identity.name.value ? `Your menu, ${identity.name.value}` : 'Your menu'))
</script>

<template>
  <div class="flex items-center gap-0.5" :class="collapsed && 'flex-col'" data-sidebar-menus>
    <UTooltip v-if="updateReady" :text="`Conductor ${updateReady} is ready: restart to update`" :content="side">
      <UButton
        icon="i-lucide-circle-arrow-up"
        color="primary"
        variant="ghost"
        :size="collapsed ? 'sm' : 'xs'"
        :aria-label="`Conductor ${updateReady} is ready to install`"
        data-header-update
        @click="restartFor = updateReady"
      />
    </UTooltip>
    <UDropdownMenu :items="items" :content="collapsed ? { side: 'right' } : { align: 'end' }">
      <UTooltip :text="identity.name.value ? `You are ${identity.name.value}` : 'Your menu'" :content="side">
        <UButton ref="account" color="neutral" variant="ghost" :size="collapsed ? 'sm' : 'xs'" :aria-label="who" data-header-account>
          <UChip :show="!hasToken" color="warning" inset>
            <span v-if="identity.name.value" class="grid size-5 place-items-center rounded-full bg-default text-[10px] font-semibold text-highlighted">{{ initials(identity.name.value) }}</span>
            <UIcon v-else name="i-lucide-user-round" class="size-4" />
          </UChip>
        </UButton>
      </UTooltip>
    </UDropdownMenu>
    <NameDialog v-model:open="nameOpen" />
  </div>
</template>
