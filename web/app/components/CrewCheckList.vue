<script setup lang="ts">
import { watch } from 'vue'
import { TASK_LIMITS, checkProblems, secondsText, typedSeconds, type DraftCheck } from '~/utils/crews'

/**
 * The commands of a task's checks or of a project manifest's phase (#334, EN1): an id, an argv as chips (ArgvInput: never a shell line, a
 * quoted argument one chip) and a timeout in seconds. The list's owner names what they are. A project manifest's lists hand in each row's
 * problem (manifestMarks), since the manifest's rules span its lists and the tasks' checks; a task's checks are held to their own.
 */
const checks = defineModel<DraftCheck[]>({ default: () => [] })
const props = defineProps<{
  what: string
  max?: number
  /** Each row's first problem, by index, from the owner's rules: in place of the list's own. */
  problems?: readonly string[]
  /** Ids taken outside this list (the manifest's other lists, the tasks' checks): a new row's id is none of them. */
  taken?: readonly string[]
  /** The id a new row is given, with `-2`, `-3`… while it is taken; `check` when not given. */
  idBase?: string
  /** What a row without a timeout gets, in seconds: the field's placeholder. A check's default when not given. */
  timeoutDefault?: number
  /** No timeout field (a service runs as long as its tree), but on a row that has one, so that it can be cleared. */
  noTimeout?: boolean
  /** The add button's words; `Add <what>` when not given. */
  addLabel?: string
}>()

const max = computed(() => props.max ?? TASK_LIMITS.checks)

// Two updates in one tick (ArgvInput commits its chips and clears its text
// together) each see the list as the parent last passed it, so the later
// would undo the earlier: the latest list sent up stands until the parent's
// comes back.
let latest: DraftCheck[] | null = null
watch(checks, () => {
  latest = null
})
function send(next: DraftCheck[]) {
  latest = next
  checks.value = next
}

function update(i: number, patch: Partial<DraftCheck>) {
  send((latest ?? checks.value).map((c, j) => (j === i ? { ...c, ...patch } : c)))
}

function remove(i: number) {
  send((latest ?? checks.value).filter((_, j) => j !== i))
}

function add() {
  const ids = [...checks.value.map((c) => c.id), ...(props.taken ?? [])]
  const base = props.idBase ?? 'check'
  let id = base
  for (let n = 2; ids.includes(id); n++) id = `${base}-${n}`
  checks.value = [...checks.value, { id, argv: [] }]
}

/** The first problem of a row, for the mark under it. */
function problem(c: DraftCheck, i: number): string {
  if (props.problems) return props.problems[i] ?? ''
  const taken = checks.value.filter((_, j) => j !== i).map((x) => x.id)
  return checkProblems(c, props.what, taken)[0]?.replace(`${props.what}: `, '') ?? ''
}

// A timeout as typed: a negative or a fraction stays, for the rules to mark (typedSeconds).
function setTimeout(i: number, text: string) {
  update(i, { timeoutSeconds: typedSeconds(text) })
}
</script>

<template>
  <div class="flex flex-col gap-2" :data-crew-checks="what">
    <div v-for="(c, i) in checks" :key="i" class="flex flex-col gap-1" data-crew-check :data-check-id="c.id">
      <div class="flex flex-wrap items-start gap-2">
        <UInput :model-value="c.id" placeholder="id" maxlength="40" autocapitalize="off" spellcheck="false" :aria-label="`Id of ${what} ${i + 1}`" :ui="{ base: 'font-mono' }" class="w-28" @update:model-value="update(i, { id: String($event) })" />
        <div class="min-w-48 flex-1">
          <!-- The text typed and not yet a chip (an unclosed quote) stays in the draft: it blocks a save rather than vanish. -->
          <ArgvInput :model-value="c.argv" :pending="c.pending ?? ''" :placeholder="`program and arguments; a quoted argument is one`" :invalid="!c.argv.length || !!c.pending" :aria-label="`Command of ${what} ${i + 1}`" @update:model-value="update(i, { argv: $event })" @update:pending="update(i, { pending: $event || undefined })" />
        </div>
        <UInput v-if="!noTimeout || !!c.timeoutSeconds" :model-value="secondsText(c.timeoutSeconds)" type="number" inputmode="numeric" min="1" :max="TASK_LIMITS.timeout" :placeholder="String(timeoutDefault ?? TASK_LIMITS.timeoutDefault)" :aria-label="`Timeout in seconds of ${what} ${i + 1}`" class="w-24" @update:model-value="setTimeout(i, String($event))">
          <template #trailing><span class="text-xs text-muted">s</span></template>
        </UInput>
        <IconButton icon="i-lucide-x" color="neutral" variant="ghost" size="xs" :label="`Remove ${what} ${i + 1}`" @click="remove(i)" />
      </div>
      <span v-if="problem(c, i)" class="text-xs leading-snug text-error" data-check-error>{{ problem(c, i) }}</span>
    </div>
    <UButton :label="addLabel ?? `Add ${what}`" icon="i-lucide-plus" color="secondary" variant="link" size="sm" class="self-start px-0" :disabled="checks.length >= max" data-add-check @click="add" />
  </div>
</template>
