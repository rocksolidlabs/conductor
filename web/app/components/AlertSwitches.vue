<script setup lang="ts">
/**
 * The alert settings: a browser notification and a chime when a session needs input, and for events routed to Browser. Drawn alone
 * (the phone's More, `rules`), it ends with "All alert rules", which asks for the Alerts panel (AlertsPanel); inside the panel the
 * rules are under it, so it has none.
 */
withDefaults(defineProps<{ rules?: boolean }>(), { rules: false })
const emit = defineEmits<{ rules: [from: HTMLElement | null] }>()
const alerts = useAttentionSettings()
</script>

<template>
  <div class="flex flex-col gap-3" data-alert-switches>
    <p class="text-xs text-muted">When a session needs input, and for the events routed to Browser:</p>
    <USwitch
      :model-value="alerts.settings.value.notifications"
      label="Browser notification"
      :description="alerts.permission.value === 'denied' ? 'Blocked by the browser' : undefined"
      :disabled="alerts.permission.value === 'denied' || alerts.permission.value === 'unsupported'"
      @update:model-value="alerts.setNotifications"
    />
    <USwitch :model-value="alerts.settings.value.chime" label="Chime" @update:model-value="alerts.setChime" />
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
