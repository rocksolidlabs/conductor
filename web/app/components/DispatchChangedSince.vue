<script setup lang="ts">
import type { LaunchChanges, LaunchSummary } from '~/composables/useDispatch'
import type { HistoryEntry } from '~/utils/dispatch'
import { changedSinceWords, changesSinceNote } from '~/utils/dispatchEvidence'
import { revisionTag } from '~/utils/dispatchPlans'
import { relativeTime } from '~/utils/sessions'

/**
 * A launched run whose ticket changed after it launched (Program item DS2, docs/plans/dispatch-evidence.md, "web/app", PR-11): which of
 * the title, the description and the acceptance criteria changed, and how many comments came since (the launch's `changed`); the
 * history between the launch and now in the history's own words, or as far as it is read, saying so; that the run keeps working from
 * what it was given; and Revise (a new plan revision) and Launch a follow-up (which needs the approval to hold again), never "update the
 * run": an edit of the ticket never rewrites a run in flight.
 */
const props = defineProps<{
  launch: LaunchSummary
  changed: LaunchChanges
  /** The history's entries after the launch's own row; null when the history read so far does not reach it. */
  rows: HistoryEntry[] | null
  /** Whether the history has later rows not read yet (it is read oldest first): the rows may stop short of now. */
  more: boolean
  locked: boolean
  now: number
}>()
const emit = defineEmits<{ revise: []; followUp: [] }>()

const words = computed(() => changedSinceWords(props.changed))
const note = computed(() => changesSinceNote(props.rows, props.more))
const open = ref(false)
</script>

<template>
  <UAlert
    v-if="words"
    color="warning"
    variant="subtle"
    icon="i-lucide-file-diff"
    :title="words"
    data-changed-since
    :data-changed-fields="changed.fields.join(',')"
    :data-changed-comments="changed.comments"
    :data-changed-rev="changed.rev"
  >
    <template #description>
      <div class="flex flex-col gap-2">
        <p>
          The run keeps working from what it was given: the ticket at its revision {{ launch.ticketRev }} and plan revision
          {{ launch.planN }} ({{ revisionTag(launch.planN) }}). The ticket is at its revision {{ changed.rev }} now. A change that matters
          is a new revision and a follow-up launch.
        </p>
        <div class="flex flex-wrap items-center gap-2">
          <UButton
            :label="open ? 'Hide what changed' : 'What changed'"
            :icon="open ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'"
            size="xs"
            color="neutral"
            variant="ghost"
            :aria-expanded="open"
            data-changed-history-open
            @click="open = !open"
          />
          <template v-if="!locked">
            <UButton label="Revise" icon="i-lucide-file-pen-line" size="xs" color="neutral" variant="outline" data-changed-revise @click="emit('revise')" />
            <UButton label="Launch a follow-up" icon="i-lucide-play" size="xs" color="neutral" variant="outline" data-changed-follow-up @click="emit('followUp')" />
          </template>
        </div>
        <template v-if="open">
          <p v-if="note === 'unread'" class="text-xs text-muted" data-changed-history-note="unread">The history read so far ends before this launch: More history, below, reads on.</p>
          <p v-else-if="note === 'none'" class="text-xs text-muted" data-changed-history-note="none">Nothing of the ticket's own history follows the launch's row yet.</p>
          <ol v-if="rows?.length" class="flex flex-col gap-1" data-changed-history>
            <li v-for="e in rows" :key="e.key" class="text-xs" data-changed-history-entry>
              <span class="font-medium">{{ e.who }}</span>
              <span class="mx-1 text-muted">·</span>
              <time class="text-muted" :datetime="e.at">{{ relativeTime(e.at, now, { suffix: true }) }}</time>
              <ul class="mt-0.5 flex flex-col gap-0.5">
                <li v-for="(line, i) in e.lines" :key="i" class="break-words" data-changed-history-line>{{ line }}</li>
              </ul>
            </li>
          </ol>
          <p v-if="note === 'partial'" class="text-xs text-muted" data-changed-history-note="partial">Later history is not read yet: More history, below, reads on.</p>
        </template>
      </div>
    </template>
  </UAlert>
</template>
