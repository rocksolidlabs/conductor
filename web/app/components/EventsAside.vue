<script setup lang="ts">
import type { Integration } from '~/composables/useSessions'
import { enough } from '~/utils/charts'
import { reportStatus } from '~/utils/integrations'
import { routeSummary, sparkBuckets, type EventType, type FeedEntry, type RouteRow, type SparkKind } from '~/utils/events'
import { tickLabel } from '~/utils/timeline'

/**
 * Beside the feed: the last hour as bars per minute (what needs you,
 * handoffs, errors and the rest, as this tab saw them), where each kind of
 * event goes now (Edit routing opens the Alerts panel, where the rules are),
 * and whether each agent reports.
 */
const props = defineProps<{ entries: FeedEntry[]; now: number; routes: Record<EventType, RouteRow>; integrations: Integration[] }>()
const alertsPanel = useAlertsPanel()
const reportingOpen = ref(false)

const spark = computed(() => sparkBuckets(props.entries, props.now, 60))
const total = computed(() => Object.values(spark.value.totals).reduce((a, b) => a + b, 0))
const max = computed(() => Math.max(1, ...spark.value.buckets.map((b) => b.needs + b.handoff + b.error + b.other)))
/** Bottom to top: the loudest first, so a minute that needed you is never hidden under tool calls. */
const kinds: Array<{ key: SparkKind; label: string; cls: string }> = [
  { key: 'error', label: 'errors', cls: 'bg-error' },
  { key: 'needs', label: 'needs you', cls: 'bg-warning' },
  { key: 'handoff', label: 'handoffs', cls: 'bg-info' },
  { key: 'other', label: 'everything else', cls: 'bg-accented' },
]
/** The legend reads as the design has it: needs you, handoffs, errors, everything else. */
const legend = ['needs', 'handoff', 'error', 'other'].map((k) => kinds.find((x) => x.key === k)!)
const axis = computed(() => {
  const b = spark.value.buckets
  return [tickLabel(b[0]!.minute, 3_600_000), tickLabel(b[30]!.minute, 3_600_000), 'now']
})
const where = computed(() => routeSummary(props.routes))

/** Whether the agent reports, by reportStatus (the one rule), in this list's own words; installed and injected neutral: green is for a live agent. */
function reporting(it: Integration): { dot: string; text: string; tone: string } {
  switch (reportStatus(it).key) {
    case 'reports':
      return { dot: 'bg-neutral-400', text: it.installed ? `${it.events.length} hooks installed` : 'injected at launch', tone: 'text-muted' }
    case 'experimental':
      return { dot: 'bg-neutral-400', text: 'experimental', tone: 'text-muted' }
    default:
      // A standing fact that asks nothing: a neutral dot and muted words (amber is for what someone must act on).
      return { dot: 'bg-neutral-400', text: 'not configured', tone: 'text-muted' }
  }
}
</script>

<template>
  <aside class="flex flex-col gap-6" data-events-aside>
    <section class="flex flex-col gap-2.5" aria-labelledby="activity-chart-title" data-activity-chart :data-total="total">
      <div class="flex flex-wrap items-baseline gap-x-2">
        <h3 id="activity-chart-title" class="text-[11px] font-semibold uppercase tracking-wider text-muted">Last hour</h3>
        <span class="text-xs text-muted">per minute, as this tab saw them</span>
      </div>
      <p v-if="!enough(total)" class="py-3 text-sm text-muted" data-activity-chart-empty>Activity per minute appears once two events arrived in the last hour.</p>
      <template v-else>
        <div class="flex h-[72px] items-end gap-[2px] border-b border-default">
          <div v-for="b in spark.buckets" :key="b.minute" class="flex h-full flex-1 flex-col-reverse" :title="`${tickLabel(b.minute, 3_600_000)} · ${b.needs + b.handoff + b.error + b.other}`" data-activity-bar>
            <div v-for="k in kinds" :key="k.key" :class="k.cls" :style="{ height: `${(b[k.key] / max) * 100}%` }" />
          </div>
        </div>
        <div class="flex justify-between font-mono text-[10.5px] text-muted">
          <span v-for="a in axis" :key="a">{{ a }}</span>
        </div>
        <div class="flex flex-wrap gap-x-2.5 gap-y-1 text-[11.5px] text-muted">
          <span v-for="k in legend" :key="k.key" class="flex items-center gap-1">
            <span class="size-2 rounded-xs" :class="k.cls" />{{ k.label }} {{ spark.totals[k.key] }}
          </span>
        </div>
      </template>
    </section>

    <section class="flex flex-col gap-2" data-routing-readout>
      <div class="flex items-baseline">
        <h3 class="text-[11px] font-semibold uppercase tracking-wider text-muted">Where they go</h3>
        <UButton label="Edit routing" size="xs" color="secondary" variant="link" class="ml-auto px-0" @click="(e: MouseEvent) => alertsPanel.show(e.currentTarget as HTMLElement | null)" />
      </div>
      <p v-if="!where.length" class="text-[12.5px] text-muted">Nothing interrupts: every event goes to the feed only.</p>
      <div v-for="r in where" :key="r.key" class="flex flex-col gap-0.5 text-[12.5px]">
        <span class="flex items-center gap-1.5 font-medium text-highlighted"><UIcon :name="r.icon" class="size-3.5 text-muted" />{{ r.title }}</span>
        <span class="pl-5 text-muted">{{ r.types }}</span>
      </div>
    </section>

    <!-- Which agents report is set-up, not history (#1024): folded until asked for. -->
    <section v-if="integrations.length" class="flex flex-col gap-2" data-reporting>
      <div class="flex items-center">
        <UButton
          :label="reportingOpen ? 'Hide which agents report' : 'Which agents report'"
          :trailing-icon="reportingOpen ? 'i-lucide-chevron-up' : 'i-lucide-chevron-down'"
          size="xs"
          color="neutral"
          variant="ghost"
          class="-ml-2 max-sm:min-h-11"
          :aria-expanded="reportingOpen"
          aria-controls="history-reporting-list"
          data-reporting-toggle
          @click="reportingOpen = !reportingOpen"
        />
        <UButton label="Set up on Agents" to="/agents#reporting" size="xs" color="secondary" variant="link" class="ml-auto px-0" data-reporting-link />
      </div>
      <div v-show="reportingOpen" id="history-reporting-list" class="flex flex-col gap-2">
        <div v-for="it in integrations" :key="it.id" class="flex items-center gap-2 text-[12.5px]" :data-reporting-agent="it.id">
          <span class="size-[7px] flex-none rounded-full" :class="reporting(it).dot" />
          <span class="truncate font-medium text-highlighted">{{ it.name }}</span>
          <span class="ml-auto flex-none" :class="reporting(it).tone">{{ reporting(it).text }}</span>
        </div>
      </div>
    </section>
  </aside>
</template>
