<script setup lang="ts">
import type { Repair } from '~/utils/dispatchEvidence'
import { shortCommit } from '~/utils/dispatchPlans'

/**
 * A run's repairs (Program item DS2, docs/plans/dispatch-evidence.md, "web/app", PR-11), oldest first: each failed check with the
 * attempt of the same task that started at the failed result, its whole log in its task file; each review that asked for changes with
 * the reviewed task's attempts that started at the commit it reviewed after its verdict, its findings in their task files
 * (utils/dispatchEvidence.ts, repairs). Who took each up is read from the task route, an attempt that never submitted included; while
 * that read is on its way it is said so, a failure of it in its words, and none yet is said so.
 */
const props = defineProps<{
  repairs: readonly Repair[]
  /** By task: why its attempts could not be read. */
  errors: ReadonlyMap<string, string>
}>()

/** Why a repair's attempts are not known, when the read of a task it waits on failed: '' while it reads. */
function unreadError(r: Repair): string {
  for (const t of r.unread) if (props.errors.has(t)) return props.errors.get(t) ?? ''
  return ''
}
</script>

<template>
  <div v-if="repairs.length" class="flex flex-col gap-1" data-repairs :data-count="repairs.length">
    <h5 class="text-xs font-medium text-muted">Repairs</h5>
    <ul class="flex flex-col gap-1 text-xs">
      <li v-for="r in repairs" :key="r.key" class="flex items-start gap-1.5" :data-repair="r.key" :data-repair-by="r.by.map((b) => `${b.task}/${b.attempt}`).join(',')">
        <UIcon :name="r.kind === 'check' ? 'i-lucide-wrench' : 'i-lucide-message-square-warning'" class="mt-0.5 size-3.5 flex-none text-muted" />
        <span class="min-w-0 break-words">
          <template v-if="r.kind === 'check'">
            <span class="font-medium">{{ r.task }}</span> attempt {{ r.attempt }} failed {{ r.what }} on <code class="font-mono" :title="r.commit">{{ shortCommit(r.commit) }}</code>:
          </template>
          <template v-else>
            <span class="font-medium">{{ r.task }}</span> attempt {{ r.attempt }} asked for changes ({{ r.what }}) on <code class="font-mono" :title="r.commit">{{ shortCommit(r.commit) }}</code>:
          </template>
          <span v-if="unreadError(r)" class="text-warning" data-repair-error>its attempts could not be read: {{ unreadError(r) }}</span>
          <span v-else-if="r.unread.length" class="text-dimmed" data-repair-reading>reading its attempts…</span>
          <template v-else-if="r.by.length">
            <template v-for="(b, i) in r.by" :key="`${b.task}/${b.attempt}`">
              {{ i ? ', ' : '' }}<span class="font-medium">{{ b.task }}</span> attempt {{ b.attempt }} started at {{ r.kind === 'check' ? 'its result' : 'the reviewed commit' }} ({{ b.state }})
            </template>
          </template>
          <span v-else class="text-dimmed">no attempt has started at it yet</span>
        </span>
      </li>
    </ul>
  </div>
</template>
