<script setup lang="ts">
import type { WebhookInfo } from '~/composables/useSessions'
import { EVENT_INFO, ROUTE_GROUPS, ROUTE_KEYS, type EventType, type RouteRow, webhookHosts } from '~/utils/events'

/**
 * Where each kind of event goes in this browser: the ten events grouped by
 * how loud they are, each with its destinations as pills (Badge, Browser,
 * Roundhouse jump, Feed) and the webhooks of conductor.json beside them, read-only.
 * `working` never sets a badge: it clears one. Below lg every pill is a 44 px touch target.
 */
const props = defineProps<{
  routes: Record<EventType, RouteRow>
  /** Webhooks configured in conductor.json, shown read-only: the server sends them the events they list. */
  webhooks?: WebhookInfo[]
}>()
const emit = defineEmits<{ update: [type: EventType, key: keyof RouteRow, value: boolean] }>()

const WEBHOOK_HINT = 'configure webhooks in conductor.json'

/** Cells that are not a choice, and why. */
function fixed(t: EventType, key: keyof RouteRow): string | null {
  return t === 'working' && key === 'badge' ? 'clears the badge' : null
}

function webhook(t: EventType): { on: boolean; hint: string } {
  const hosts = webhookHosts(props.webhooks, t)
  return { on: hosts.length > 0, hint: hosts.length ? `POSTed to ${hosts.join(', ')}` : WEBHOOK_HINT }
}

const on = 'bg-primary/8 text-primary ring-primary'
const off = 'text-muted ring-accented hover:text-default'
</script>

<template>
  <div class="flex flex-col gap-3.5" data-routing-matrix>
    <div class="flex flex-wrap items-center gap-x-4 gap-y-1.5 text-xs text-muted">
      <UPopover v-for="c in [...ROUTE_KEYS, { key: 'webhook', label: 'Webhook', icon: 'i-lucide-webhook', hint: 'What the server POSTs to the webhooks in conductor.json: set there, not here' }]" :key="c.key" :content="{ side: 'top' }" arrow>
        <button type="button" class="flex cursor-help items-center gap-1 max-lg:min-h-11 underline decoration-dotted underline-offset-4 hover:text-default" :data-hint="c.key">
          <UIcon :name="c.icon" class="size-3.5" />{{ c.label }}
        </button>
        <template #content>
          <p class="max-w-56 p-2.5 text-xs text-default">{{ c.hint }}</p>
        </template>
      </UPopover>
    </div>

    <section v-for="g in ROUTE_GROUPS" :key="g.key" class="overflow-hidden rounded-lg ring ring-default" :data-route-group="g.key">
      <div class="flex items-baseline gap-2 border-b border-default bg-muted px-4 py-2.5">
        <span class="text-[13px] font-semibold text-highlighted">{{ g.title }}</span>
        <span class="text-xs text-muted">{{ g.note }}</span>
      </div>
      <div v-for="t in g.types" :key="t" class="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-default px-4 py-2.5 last:border-b-0" :data-route="t">
        <div class="flex w-full min-w-0 flex-col gap-px sm:w-72 sm:flex-none">
          <span class="text-[13.5px] font-medium text-highlighted">{{ EVENT_INFO[t].title }}</span>
          <!-- Wrapped, not cut: what reports an event is read in full on every width, a touch screen's too (#206). -->
          <span class="text-xs leading-snug text-muted">{{ EVENT_INFO[t].source }} · <span class="font-mono text-[11px]">{{ t }}</span></span>
        </div>
        <div class="flex flex-wrap gap-1.5">
          <template v-for="c in ROUTE_KEYS" :key="c.key">
            <UTooltip v-if="fixed(t, c.key)" :text="fixed(t, c.key)!">
              <span class="flex h-[26px] items-center max-lg:h-11 max-lg:px-3.5 gap-1 rounded-full px-2.5 text-xs font-medium text-dimmed ring-1 ring-inset ring-default" :data-cell="`${t}.${c.key}`" data-fixed-cell aria-disabled="true">
                <UIcon :name="c.icon" class="size-3.5" />{{ c.label }}
              </span>
            </UTooltip>
            <button
              v-else
              type="button"
              role="switch"
              :aria-checked="routes[t][c.key]"
              :aria-label="`${EVENT_INFO[t].title}: ${c.label}`"
              class="flex h-[26px] items-center max-lg:h-11 max-lg:px-3.5 gap-1 rounded-full px-2.5 text-xs font-medium ring-1 ring-inset transition-colors outline-none focus-visible:ring-2 focus-visible:ring-primary"
              :class="routes[t][c.key] ? on : off"
              :data-cell="`${t}.${c.key}`"
              @click="emit('update', t, c.key, !routes[t][c.key])"
            >
              <UIcon :name="c.icon" class="size-3.5" />{{ c.label }}
            </button>
          </template>
          <UTooltip :text="webhook(t).hint">
            <span class="flex h-[26px] items-center max-lg:h-11 max-lg:px-3.5 gap-1 rounded-full px-2.5 text-xs font-medium ring-1 ring-inset" :class="webhook(t).on ? on : 'text-dimmed ring-default'" :aria-label="`${EVENT_INFO[t].title}: Webhook (${webhook(t).hint})`" data-webhook-cell>
              <UIcon name="i-lucide-webhook" class="size-3.5" />Webhook
            </span>
          </UTooltip>
        </div>
      </div>
    </section>
  </div>
</template>
