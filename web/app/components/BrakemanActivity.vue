<script setup lang="ts">
import type { BrakemanRole, RoleLogRow } from '~/utils/brakeman'
import { clockTime, rowWords } from '~/utils/brakemanRole'

/**
 * Wakes and notes (the Brakeman design review's Activity): the background role's log, newest first, each wake by its source and what
 * it was about by name, and what became of it (a turn's brief a link to it, never the batch's id); then the notes a person kept from
 * briefs, given to every later turn, with Clear. Opened by the role card's Activity or the Notes card's Open (`at` says where it
 * scrolls to); its content stays mounted while it is closed, so nothing is read again when it opens.
 */
const props = defineProps<{ role: BrakemanRole | null; log: RoleLogRow[]; busy: boolean; at: 'wakes' | 'notes' }>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ clear: [] }>()

const bk = useBrakeman()
const live = useAttention()
const names = {
  session: (id: string) => live.sessions.value.find((s) => s.id === id)?.name,
  run: (id: string) => live.runNames.value[id],
}
const requests = computed(() => new Map(bk.requests.value.map((r) => [r.id, r])))
/** A turn's words as a wake's fate links it: its brief's title, else "its brief" or "its turn". */
function turnWords(id: string): string {
  const r = requests.value.get(id)
  if (r?.hasBrief) return r.digest?.title ? `its brief: ${r.digest.title}` : 'its brief'
  return 'its turn'
}

watch(open, (o) => {
  if (!o) return
  nextTick(() => document.querySelector(props.at === 'notes' ? '[data-role-sheet] [data-role-notes]' : '[data-role-sheet] [data-role-log]')?.scrollIntoView({ block: 'start' }))
})
</script>

<template>
  <USlideover
    v-model:open="open"
    title="Wakes and notes"
    description="What woke Brakeman in the background, and the notes every turn is given."
    :ui="{ content: 'max-w-[32rem]', close: 'max-lg:min-h-11 max-lg:min-w-11 max-lg:justify-center' }"
  >
    <template #body>
      <div class="flex flex-col gap-6 text-sm" data-role-sheet>
        <section class="flex flex-col gap-2" data-role-log>
          <h3 class="text-sm font-semibold text-highlighted">Wakes</h3>
          <p v-if="!log.length" class="text-xs text-muted">Nothing has woken it yet.</p>
          <ul v-else class="flex flex-col gap-1.5">
            <li v-for="(row, i) in log" :key="`${row.at}-${i}`" class="flex flex-wrap gap-x-3 text-xs" :data-role-row="row.source" :data-role-batch="row.batch" :data-role-request="row.request" :data-role-held="row.held">
              <span class="text-muted tabular-nums">{{ clockTime(row.at) }}</span>
              <span class="min-w-0 break-words">{{ rowWords(row, names).what }}</span>
              <NuxtLink v-if="rowWords(row, names).request" :to="`/brakeman?request=${encodeURIComponent(row.request!)}`" class="min-w-0 break-words text-muted underline" data-role-fate>
                {{ rowWords(row, names).fate }}, {{ turnWords(row.request!) }}
              </NuxtLink>
              <span v-else-if="rowWords(row, names).fate" class="text-muted" data-role-fate>{{ rowWords(row, names).fate }}</span>
            </li>
          </ul>
        </section>

        <section class="flex flex-col gap-2 border-t border-default pt-5" data-role-notes>
          <div class="flex items-center gap-2">
            <h3 class="text-sm font-semibold text-highlighted">Notes</h3>
            <span class="text-xs text-muted">What you kept from briefs, given to every turn.</span>
            <UButton v-if="role?.notes?.length" label="Clear" size="xs" color="neutral" variant="ghost" class="ml-auto max-lg:min-h-11" :loading="busy" data-role-clear-notes @click="emit('clear')" />
          </div>
          <p v-if="!role?.notes?.length" class="text-xs text-muted">None kept.</p>
          <ul v-else class="flex flex-col gap-1">
            <li v-for="(n, i) in role.notes" :key="i" class="text-xs" data-role-note>
              {{ n.text }} <span v-if="n.from" class="text-muted">({{ n.from }})</span>
            </li>
          </ul>
        </section>
      </div>
    </template>
  </USlideover>
</template>
