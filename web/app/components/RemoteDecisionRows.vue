<script setup lang="ts">
import { answerPage, remoteDecisions } from '~/utils/remote'

/**
 * Other machines' decisions (docs/plans/sync-seams.md, OS-4), beside this machine's: of the ticket `ticketKey` names, or all of them.
 * Read only: no action of this machine's is offered on them, each marked "on <device>"; an open one whose request left its machine
 * offers its answer page (the sync store's, in a new tab) when the store names one. Nothing is drawn with no sync store.
 */
const props = defineProps<{ ticketKey?: string; title?: string }>()
const remote = useRemote('decision', () => props.ticketKey ?? '')
const rows = computed(() => remoteDecisions(remote.records.value))
</script>

<template>
  <section v-if="rows.length" class="flex flex-col gap-2" data-remote-decisions>
    <h3 v-if="title" class="text-xs font-semibold uppercase tracking-wide text-muted">
      {{ title }}
    </h3>
    <ul class="flex flex-col gap-1.5">
      <li
        v-for="d in rows"
        :key="d.key"
        class="flex min-w-0 items-start gap-2 rounded-md px-3 py-2 text-sm opacity-80 ring ring-default"
        :data-remote-decision="d.id"
        :data-remote-device="d.device"
      >
        <UIcon name="i-lucide-monitor-smartphone" class="mt-0.5 size-4 flex-none text-muted" />
        <div class="flex min-w-0 flex-1 flex-col gap-0.5">
          <div class="flex min-w-0 flex-wrap items-baseline gap-x-2">
            <span class="font-mono text-xs text-muted" data-mono="id">{{ d.id }}</span>
            <span class="min-w-0 break-words text-default" data-remote-question>{{ d.question ?? `Its request is kept ${d.where}` }}</span>
          </div>
          <p class="text-xs text-dimmed">
            <UBadge :label="d.where" color="neutral" variant="soft" size="sm" data-remote-where />
            <span class="ml-1.5" data-remote-state>{{ d.state }}</span>
            <template v-if="answerPage(remote.report.value, d)">
              ·
              <ULink :to="answerPage(remote.report.value, d)!" target="_blank" class="text-primary" data-remote-answer>Open its answer page</ULink>
            </template>
          </p>
        </div>
      </li>
    </ul>
  </section>
</template>
