<script setup lang="ts">
import type { RunRowView } from '~/utils/hosts'
import { heldRunStatus, statusOfKey } from '~/utils/status'

/**
 * A managed run on the Hosts page (docs/plans/hosts.md, PR-3): its name linking to its run page, its id, its state in the status
 * language, what waits on it (a link to Needs you, PR-4) and who holds it with the note, all from the row it is given (runRowView:
 * that reply's row alone). No control: Stop, Continue, Retry and
 * the rest live on the run page.
 */
const props = defineProps<{ row: RunRowView }>()
const mark = computed(() => heldRunStatus(props.row.state, props.row.paused))
const needs = statusOfKey('needs')
</script>

<template>
  <li class="flex min-w-0 flex-col gap-1 rounded-md px-3 py-2 ring ring-default" :data-host-run="row.id">
    <div class="flex min-w-0 items-center gap-2">
      <ULink
        v-if="row.link"
        :to="row.link"
        class="min-w-0 truncate font-medium text-highlighted hover:underline pointer-coarse:py-2.5"
        :title="row.title"
        data-host-run-link
      >
        {{ row.title }}
      </ULink>
      <span v-else class="min-w-0 truncate font-medium text-highlighted" :title="row.title" data-host-run-title>{{ row.title }}</span>
      <StatusMark :status="mark" word class="ms-auto" data-host-run-state />
    </div>
    <div class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-muted">
      <span class="min-w-0 truncate font-mono" data-mono="id" :title="row.id">{{ row.id }}</span>
      <template v-if="row.waiting">
        <ULink v-if="row.waitingLink" :to="row.waitingLink" class="inline-flex items-center gap-1.5 text-default hover:underline" data-host-run-waiting :data-count="row.waitingCount">
          <StatusMark :status="needs" />
          <span>{{ row.waiting }}</span>
        </ULink>
        <span v-else class="inline-flex items-center gap-1.5" data-host-run-waiting :data-count="row.waitingCount">
          <StatusMark v-if="row.waitingCount > 0" :status="needs" />
          <span>{{ row.waiting }}</span>
        </span>
      </template>
      <span data-host-run-holder>
        <span data-host-run-holder-words>{{ row.holder }}</span><span v-if="row.note" data-host-run-note>: {{ row.note }}</span>
      </span>
    </div>
  </li>
</template>
