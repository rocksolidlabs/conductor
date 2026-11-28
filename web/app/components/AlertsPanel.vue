<script setup lang="ts">
import type { WebhookInfo } from '~/composables/useSessions'

/**
 * Alerts: both halves of how a person is alerted, in one slideover at the right of the window, mounted once in the layout and
 * opened through useAlertsPanel (your menu's "Alerts…", the palette's row, the phone's "All alert rules", `?alerts`, the feed's
 * Edit routing). On top the switches for a notification on this device and a chime (AlertSwitches); under them where each kind of event
 * goes (RoutingRows), kept on this device, with the webhooks of conductor.json read from the server while it is open. Closed, by
 * Escape or its close button, it gives the focus back to the control it was opened from.
 */
const panel = useAlertsPanel()
const events = useEvents()
const api = useSessions()
const admin = useWorkbenchToken()

const webhooks = ref<WebhookInfo[]>([])
/** The webhooks are the server's to say; without a token the rules still show, the webhooks' column says where they are set. */
async function readWebhooks() {
  if (!admin.hasToken.value) return
  try {
    webhooks.value = (await api.integrations()).webhooks
  } catch {
    // The column then reads as not configured: the rules themselves are this device's and still work.
  }
}
watch(
  () => panel.open.value,
  (open) => {
    if (open) void readWebhooks()
  },
  { immediate: true },
)

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
    description="How Conductor tells you a session needs you, and where each kind of event goes."
    :content="{ onCloseAutoFocus: giveBack }"
    :ui="{ content: 'max-w-[44rem]', close: 'max-lg:min-h-11 max-lg:min-w-11 max-lg:justify-center' }"
    data-alerts-panel
  >
    <template #body>
      <div class="flex flex-col gap-6" data-alerts-panel-body>
        <AlertSwitches />
        <section class="flex flex-col gap-3 border-t border-default pt-5" aria-labelledby="alerts-rules-title" data-alerts-rules>
          <h3 id="alerts-rules-title" class="text-sm font-semibold text-highlighted">Where each event goes</h3>
          <p class="text-[13px] text-muted">Where each kind of event goes on this device. Webhooks are set in <code>conductor.json</code>.</p>
          <RoutingRows :routes="events.routes.value" :webhooks="webhooks" @update="events.setRoute" />
        </section>
      </div>
    </template>
  </USlideover>
</template>
