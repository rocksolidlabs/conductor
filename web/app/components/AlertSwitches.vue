<script setup lang="ts">
/**
 * The alert settings: a notification on this device and a chime when a session needs input, and for events routed to This device. Drawn alone
 * (the phone's More, `rules`), it ends with "All alert rules", which asks for the Alerts panel (AlertsPanel); inside the panel the
 * rules are under it, so it has none. While Brakeman runs in the background a third switch says whether its background briefs that
 * hold a decision alert on this device (on unless switched off); the server decides which do, and the page alerts on its event alone.
 */
import { BRIEF_SWITCH_DESCRIPTION, BRIEF_SWITCH_LABEL } from '~/utils/brakemanAlert'

withDefaults(defineProps<{ rules?: boolean }>(), { rules: false })
const emit = defineEmits<{ rules: [from: HTMLElement | null] }>()
const alerts = useAttentionSettings()
const brakeman = useBrakeman()
const background = computed(() => {
  const c = brakeman.state.value?.config
  return !!c?.enabled && c.wake.mode === 'background'
})
</script>

<template>
  <div class="flex flex-col gap-3" data-alert-switches>
    <p class="text-xs text-muted">When a session needs input, and for the events routed to This device:</p>
    <USwitch
      :model-value="alerts.settings.value.notifications"
      label="Notification on this device"
      :description="alerts.permission.value === 'denied' ? 'Blocked on this device' : undefined"
      :disabled="alerts.permission.value === 'denied' || alerts.permission.value === 'unsupported'"
      @update:model-value="alerts.setNotifications"
    />
    <USwitch :model-value="alerts.settings.value.chime" label="Chime" @update:model-value="alerts.setChime" />
    <div v-if="background" class="max-sm:min-h-11" data-alert-briefs>
      <USwitch :model-value="alerts.settings.value.briefs !== false" :label="BRIEF_SWITCH_LABEL" :description="BRIEF_SWITCH_DESCRIPTION" @update:model-value="alerts.setBriefs" />
    </div>
    <p class="text-xs text-muted">The tab title and favicon always show the count.</p>
    <UButton
      v-if="rules"
      label="All alert rules"
      icon="i-lucide-route"
      color="neutral"
      variant="link"
      class="min-h-11 self-start px-0"
      data-alert-rules
      @click="(e: MouseEvent) => emit('rules', e.currentTarget as HTMLElement | null)"
    />
  </div>
</template>
