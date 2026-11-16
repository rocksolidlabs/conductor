<script setup lang="ts">
import { initials } from '~/utils/sessions'
import { accountItems } from '~/utils/sidebarActions'

/**
 * Beside the name in the sidebar's header (design 3b): Alerts, and your menu
 * behind your initials (an amber chip while no workbench token is set):
 * Your name…, Keyboard shortcuts, Toggle theme, Workbench token…, Forget
 * token. The foot then holds only the pages. On the rail the same two stack
 * in the foot, their tooltips and menus to the right.
 */
const props = withDefaults(defineProps<{ collapsed?: boolean }>(), { collapsed: false })
/** Asks the layout for its workbench token dialog. */
const emit = defineEmits<{ token: [] }>()

const alerts = useAttentionSettings()
// The update the desktop app downloaded (#290): a button here until it installs, which asks for the restart.
const { ready: updateReady, restartFor } = useDesktopUpdate()
const identity = useIdentity()
const { hasToken, clear } = useWorkbenchToken()
const shortcuts = useShortcutsModal()
const colorMode = useColorMode()

const nameOpen = ref(false)
function openName() {
  nameOpen.value = true
}
function toggleTheme() {
  colorMode.preference = colorMode.value === 'dark' ? 'light' : 'dark'
}
const phone = useOpenOnPhone()
const items = computed(() => accountItems(hasToken.value, { name: openName, shortcuts: () => shortcuts.show(), theme: toggleTheme, token: () => emit('token'), forget: () => clear(), phone: phone.show }))
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
    <UPopover :content="side">
      <UTooltip text="Alerts" :content="side">
        <UButton :icon="alerts.settings.value.notifications ? 'i-lucide-bell-ring' : 'i-lucide-bell'" color="neutral" variant="ghost" :size="collapsed ? 'sm' : 'xs'" aria-label="Alerts" data-header-alerts />
      </UTooltip>
      <template #content>
        <div class="w-64 p-3"><AlertSwitches /></div>
      </template>
    </UPopover>
    <UDropdownMenu :items="items" :content="collapsed ? { side: 'right' } : { align: 'end' }">
      <UTooltip :text="identity.name.value ? `You are ${identity.name.value}` : 'Your menu'" :content="side">
        <UButton color="neutral" variant="ghost" :size="collapsed ? 'sm' : 'xs'" :aria-label="who" data-header-account>
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
