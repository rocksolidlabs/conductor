<script setup lang="ts">
/**
 * Alerts: the switches for a browser notification and a chime (AlertSwitches), in a small slideover opened from your menu's
 * "Alerts…" (useAlertsPanel), mounted once in the layout. Closed, by Escape or its close button, it gives the focus back to the
 * button it was opened from.
 */
const panel = useAlertsPanel()

function giveBack(e: Event) {
  const to = panel.returnTo.value
  if (!to?.isConnected) return
  e.preventDefault()
  to.focus()
}
</script>

<template>
  <USlideover
    v-model:open="panel.open.value"
    title="Alerts"
    description="How Conductor tells you a session needs you."
    :content="{ onCloseAutoFocus: giveBack }"
    :ui="{ content: 'max-w-sm' }"
    data-alerts-panel
  >
    <template #body>
      <div data-alerts-panel-body><AlertSwitches /></div>
    </template>
  </USlideover>
</template>
