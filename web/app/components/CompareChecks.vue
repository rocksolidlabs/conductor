<script setup lang="ts">
import type { CandidateReceipt } from '~/composables/useDispatch'
import { MEASURED_BY, cellReceipt, cellWords, checkCommand, checkName, verdictWords, verifyFilesWords, type CheckCell, type ComparedCandidate, type ComparedCheck } from '~/utils/compare'

/**
 * A comparison's checks (Program item CP1, docs/plans/compare-crew.md, PR-4): one row per expected check and manifest verify step, each
 * with the server's verdict in words (comparable; not comparable, because…; comparable only if their test changes are fair, with each
 * candidate's changed test and build files) and one cell per candidate: passed, failed with its exit, timed out, or not run and why,
 * with its duration and where it ran. A cell opens its receipt in DS2's dialog (DispatchReceipt: the argv, the log and its SHA-256).
 * The verdict is the server's alone (I5); durations are shown, never compared.
 */
const props = defineProps<{ checks: ComparedCheck[]; candidates: ComparedCandidate[] }>()

const byTask = computed(() => new Map(props.candidates.map((c) => [c.task, c])))
/** The cells of a row under their columns: a candidate the row has no cell for (no commit) draws one saying so. */
const cellsOf = (check: ComparedCheck): CheckCell[] => cellsInColumns(check, props.candidates)
/** Why a candidate's test and build files are not said ('' when they were read). */
const filesUnread = (c: ComparedCandidate) => notMeasured(c).verify
const cols = computed(() => (props.candidates.length >= 4 ? 'sm:grid-cols-2 xl:grid-cols-4' : props.candidates.length === 3 ? 'sm:grid-cols-2 lg:grid-cols-3' : 'sm:grid-cols-2'))

const opened = ref<{ run: string; task: string; attempt: number; commit: string; receipt: CandidateReceipt } | null>(null)
const open = computed({
  get: () => !!opened.value,
  set: (v: boolean) => {
    if (!v) opened.value = null
  },
})
function openCell(check: ComparedCheck, cell: CheckCell) {
  opened.value = cellReceipt(check, cell, byTask.value.get(cell.task))
}
</script>

<template>
  <section class="flex flex-col gap-2" data-compare-checks>
    <div class="flex flex-wrap items-baseline gap-x-2">
      <h3 class="text-sm font-semibold text-highlighted">Checks</h3>
      <span class="text-xs text-muted" data-compare-measured-by>{{ MEASURED_BY }}</span>
    </div>
    <p v-if="!checks.length" class="text-sm text-muted" data-compare-checks-none>No check is expected of these candidates: the crew names none, and the base's manifest has no verify step.</p>
    <ol v-else class="flex flex-col gap-2">
      <li v-for="k in checks" :key="`${k.phase}/${k.id}`" class="flex min-w-0 flex-col gap-2 rounded-lg p-3 text-sm ring ring-default" :data-compare-check="`${k.phase}/${k.id}`" :data-verdict="k.verdict">
        <div class="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5">
          <span class="font-medium text-highlighted" data-compare-check-name>{{ checkName(k) }}</span>
          <code class="min-w-0 break-all font-mono text-xs text-muted" :title="k.argvDigest ? `SHA-256 of the argv: ${k.argvDigest}` : undefined" data-mono="command" data-compare-check-argv>{{ checkCommand(k) }}</code>
          <span v-if="k.timeoutSeconds" class="text-xs text-muted">· at most {{ k.timeoutSeconds }} s</span>
        </div>
        <div class="flex min-w-0 flex-col gap-0.5" data-compare-verdict>
          <span class="text-default" data-compare-verdict-words>{{ verdictWords(k).label }}<template v-if="verdictWords(k).lead">, {{ verdictWords(k).lead }}</template></span>
          <ul v-if="verdictWords(k).why.length" class="flex list-disc flex-col gap-0.5 pl-5 text-xs text-muted">
            <li v-for="(w, i) in verdictWords(k).why" :key="i" class="break-words" data-compare-why>{{ w }}</li>
          </ul>
        </div>
        <!-- Each candidate's changed test and build files, beside a row that depends on them. -->
        <ul v-if="k.verdict === 'unknown'" class="flex flex-col gap-0.5 text-xs text-muted" data-compare-check-files>
          <li v-for="c in candidates" :key="c.task" class="break-words" :data-compare-check-files-of="c.task">
            <template v-if="filesUnread(c)">{{ c.task }}: {{ notMeasuredLine(filesUnread(c)) }}</template>
            <template v-else>{{ verifyFilesWords(c.task, c.verifyFiles).summary }}</template><template v-if="!filesUnread(c) && verifyFilesWords(c.task, c.verifyFiles).files.length">: <span class="font-mono" data-mono="path">{{ verifyFilesWords(c.task, c.verifyFiles).files.join('; ') }}</span></template>
          </li>
        </ul>
        <div class="grid grid-cols-1 gap-2" :class="cols">
          <component
            :is="cellOpens(c) ? 'button' : 'div'"
            v-for="c in cellsOf(k)"
            :key="c.task"
            :type="cellOpens(c) ? 'button' : undefined"
            class="flex min-w-0 flex-col items-stretch gap-0.5 rounded-md bg-elevated/40 px-2.5 py-1.5 text-left text-xs"
            :class="cellOpens(c) && 'cursor-pointer hover:bg-elevated focus-visible:outline-2 focus-visible:outline-primary'"
            :aria-label="cellOpens(c) ? `${c.task}: ${checkName(k)} ${cellWords(c).word}, open its receipt` : undefined"
            :data-compare-cell="c.task"
            :data-cell-state="cellWords(c).state"
            @click="cellOpens(c) && openCell(k, c)"
          >
            <span class="flex min-w-0 flex-wrap items-center gap-x-2">
              <span class="font-mono text-muted" data-mono="id">{{ c.task }}</span>
              <StatusMark :status="cellWords(c).mark" />
              <span class="text-default" data-compare-cell-words>{{ cellWords(c).word }}</span>
            </span>
            <span v-if="cellWords(c).detail" class="text-muted" data-compare-cell-detail>{{ cellWords(c).detail }}</span>
          </component>
        </div>
      </li>
    </ol>
    <DispatchReceipt v-model:open="open" :run-id="opened?.run ?? ''" :task="opened?.task ?? ''" :attempt="opened?.attempt ?? 0" :commit="opened?.commit ?? ''" :receipt="opened?.receipt ?? null" />
  </section>
</template>
