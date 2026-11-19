<script setup lang="ts">
import type { AgentInfo, CrewInfo } from '~/composables/useSessions'
import { plainTasks, type DraftCrew } from '~/utils/crews'
import { accountCountWords, launchAccounts, memberLogin } from '~/utils/launchAccounts'
import { COMPARISON_LAUNCHES_FROM_A_TICKET } from '~/utils/compareLaunch'
import { launchLines, launchYoloWords, readSheet, sheetNotes, sheetNotice, sheetRows, type SheetRead, type SheetRefusal, type SheetTarget } from '~/utils/crewLaunch'

/**
 * What a crew's launch asks first (docs/plans/capacity.md, PR-7), short (the review of 2026-11-18, C10): a table, a row a member (member ·
 * agent · model · account, in plain words); above it only what of a member's usage needs the person before this launch (sheetNotes); the
 * run's permission prompts in one line; then Launch, the one filled button. Everything else is under Details, nothing dropped: the sessions
 * this launch starts on each login, and each member's compact usage panel, read as the sheet opens. It keeps what it was opened for (`target`), and its
 * Launch hands the page exactly that: a saved crew is read from the server as the sheet opens and that copy is drawn, Launch held until
 * it is here (a failed read says why and offers to read again); the crew page's draft is drawn as it is and saved as it launches. The
 * sheet shows the crew as the server held it when the sheet read it, and its Launch names that copy's revision: a change saved after the
 * read (a save of this page still under way, a save from another page or another person) is refused by the server, which starts nothing
 * (#957); the page then gives the sheet a new target, which it reads again, and `refused` says why (sheetNotice, announced; once the
 * crew is drawn again the focus is on Launch). A saved crew read without a revision, or a draft whose save answered none, holds Launch
 * with words. The sheet says the run's yolo choice: the crew's own, or for a crew that inherits it the server's default at the launch,
 * drawn once that is read (readSheet), never before. Each read ends at its deadline in words with Read again, and closing the sheet or opening it for another
 * target ends the reads under way. A crew gone when it is read closes the sheet (`gone`). A compact panel is read as the sheet opens once
 * its first view has answered (at most once in five minutes per agent and account, as the server holds it); a view that failed first is
 * read by Read again, then by Read now. What the lines say never stops the launch: a window near its limit is said, and Launch stays the
 * person's. While a launch is under way Cancel and Launch are held; the close button and Escape still close the sheet, and a launch
 * under way goes on and says its result when the sheet is closed so and also when the page is left. A sheet opened again in the same
 * page while a draft's launch is saving it makes that first confirmation start nothing. For a crew that opens its run after a launch, the first launch's move to the run page
 * takes a sheet opened again meanwhile with it. Above the lines, the sessions this launch starts on each login (launchAccounts, CP1 PR-9):
 * its first tasks', or its members that start at once, and for a comparison up to its reviews' after its candidates. A comparison's crew
 * read here is not launched from the sheet: it says where a comparison is launched, and Launch is held.
 */
const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ target: SheetTarget | null; agents: AgentInfo[]; launching?: boolean; refused?: SheetRefusal | null }>()
const emit = defineEmits<{ launch: [crew: CrewInfo | null]; gone: [] }>()

const api = useSessions()
/** What the sheet read for the target it was read for; `got` null while it reads. */
const read = ref<{ target: SheetTarget; got: SheetRead<CrewInfo | DraftCrew> | null } | null>(null)
let reading: AbortController | null = null
async function readNow() {
  reading?.abort()
  const t = props.target
  if (!t || !open.value) return
  const ctl = (reading = new AbortController())
  read.value = { target: t, got: null }
  const got = await readSheet<CrewInfo | DraftCrew>('id' in t ? { id: t.id } : { draft: t.draft }, { crew: (id, s) => api.getCrew(id, s), yoloDefault: (s) => api.catalogInfo(s).then((c) => c.yoloDefault) }, ctl.signal)
  if (!got || reading !== ctl) return
  read.value = { target: t, got }
  if ('gone' in got && got.gone) emit('gone')
}
watch(
  [open, () => props.target],
  ([o, t]) => {
    if (!o || !t) reading?.abort()
    else if (read.value?.target !== t || !read.value.got) void readNow()
  },
  { immediate: true },
)
onBeforeUnmount(() => reading?.abort())
const got = computed(() => (read.value && read.value.target === props.target ? read.value.got : null))
const failed = computed(() => (got.value && 'error' in got.value ? got.value : null))
const crew = computed(() => {
  const g = got.value
  return g && 'crew' in g ? { name: g.crew.name, members: g.crew.members, managed: !!g.crew.tasks, yolo: g.yolo, inherits: typeof g.crew.yolo !== 'boolean', saved: props.target && 'id' in props.target ? (g.crew as CrewInfo) : null } : null
})
const notice = computed(() => sheetNotice(props.refused, failed.value ? 'failed' : crew.value ? 'drawn' : 'reading', !!crew.value?.saved && !crew.value.saved.revision))
// Once a refused launch's crew is drawn again, the focus goes to Launch: Launch was held through the launch and the read again.
const footer = useTemplateRef<HTMLElement>('footer')
watch(
  () => notice.value?.kind === 'changed' && !notice.value.hold && !props.launching,
  (ready) => ready && void nextTick(() => footer.value?.querySelector<HTMLButtonElement>('[data-crew-launch-confirm]')?.focus()),
)

const accounts = useAccounts()
const cap = useCapacity()
watch(open, (o) => o && void accounts.store.load(), { immediate: true })
/** The sessions this launch starts on each login, each member's login as its line resolves it (memberLogin: never a default of the page's). */
const perAccount = computed(() => {
  const g = got.value
  if (!g || !('crew' in g)) return []
  const members = g.crew.members
  return launchAccounts({
    members,
    tasks: g.crew.tasks ? plainTasks(g.crew.tasks) : undefined,
    compare: g.crew.compare,
    accountOf: (name) => {
      const m = members.find((x) => x.name === name) ?? { name, agentId: '' }
      const l = lines.value.find((x) => x.name === name)
      return memberLogin(m, { doc: accounts.doc.value, view: l ? cap.entry(l.query).view : null, agent: l?.agent ?? m.agentId })
    },
  })
})
/** Whether the crew read is a comparison's, which the server launches from a ticket's approved revision alone. */
const comparison = computed(() => {
  const g = got.value
  return !!(g && 'crew' in g && g.crew.compare)
})
const lines = computed(() => (crew.value ? launchLines(crew.value.members, { agents: props.agents, doc: accounts.doc.value, managed: crew.value.managed, yolo: crew.value.yolo, view: (q) => cap.entry(q).view }) : []))
const viewOf = (q: Parameters<typeof cap.entry>[0]) => cap.entry(q).view
/** The table's rows, and the notes that need the person before this launch. */
const rows = computed(() => (crew.value ? sheetRows(lines.value, crew.value.members, accounts.doc.value, viewOf) : []))
const notes = computed(() => sheetNotes(rows.value, viewOf))
</script>

<template>
  <UModal v-model:open="open" :title="crew ? `Launch ${crew.name}` : 'Launch'" :description="target?.label ? `As the run ${target.label}` : 'Each member with its agent, model and account'" :ui="{ content: 'max-w-2xl' }">
    <template #body>
      <div role="status">
        <p v-if="notice" class="mb-3 flex items-start gap-2 text-sm text-warning" :data-crew-launch-changed="notice.kind === 'changed' ? '' : undefined" :data-crew-launch-unrevised="notice.kind === 'unrevised' ? '' : undefined">
          <UIcon :name="notice.kind === 'changed' ? 'i-lucide-refresh-cw' : 'i-lucide-circle-alert'" class="mt-0.5 size-4 shrink-0" />
          <span class="min-w-0">{{ notice.words }}</span>
        </p>
      </div>
      <p v-if="failed" class="flex flex-wrap items-center gap-2 text-sm text-error [overflow-wrap:anywhere]" data-crew-launch-read-error>
        <span class="min-w-0">{{ failed.what === 'crew' ? 'The crew could not be read' : "The server's yolo default could not be read" }}, so nothing is launched: {{ failed.error }}</span>
        <UButton label="Read again" icon="i-lucide-refresh-cw" size="xs" color="neutral" variant="outline" :disabled="!open" data-crew-launch-read-again @click="readNow" />
      </p>
      <p v-else-if="!crew" class="flex items-center gap-2 text-sm text-muted" data-crew-launch-reading><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" />Reading the crew as the server holds it, and the server's yolo default when it inherits it…</p>
      <template v-else>
        <p v-if="comparison" class="mb-3 flex items-start gap-2 text-sm text-warning" data-crew-launch-comparison>
          <UIcon name="i-lucide-git-compare" class="mt-0.5 size-4 shrink-0" />
          <span class="min-w-0">{{ COMPARISON_LAUNCHES_FROM_A_TICKET }}.</span>
        </p>
        <!-- Only what needs the person before this launch; Launch stays theirs. -->
        <ul v-if="notes.length" class="mb-3 flex flex-col gap-1.5" data-crew-launch-notes>
          <li v-for="n in notes" :key="n.member" class="flex items-start gap-2 text-sm [overflow-wrap:anywhere]" :class="n.color === 'error' ? 'text-error' : 'text-warning'" :data-crew-launch-note="n.member" :data-crew-launch-note-color="n.color">
            <UIcon name="i-lucide-triangle-alert" class="mt-0.5 size-4 shrink-0" />
            <span class="min-w-0">{{ n.words }}</span>
          </li>
        </ul>
        <div role="table" aria-label="Who this launch starts" class="overflow-hidden rounded-md ring ring-default text-sm" data-crew-launch-sheet>
          <div role="row" class="hidden grid-cols-[8rem_1fr_1fr_1fr] gap-x-3 border-b border-default bg-muted px-3 py-2 text-[11px] font-semibold uppercase tracking-wider text-muted sm:grid">
            <span role="columnheader">Member</span>
            <span role="columnheader">Agent</span>
            <span role="columnheader">Model</span>
            <span role="columnheader">Account</span>
          </div>
          <div v-for="r in rows" :key="r.name" role="row" class="grid grid-cols-1 gap-x-3 gap-y-0.5 border-b border-default px-3 py-2 last:border-b-0 sm:grid-cols-[8rem_1fr_1fr_1fr]" :data-crew-launch-line="r.name">
            <span role="cell" class="min-w-0 font-mono font-medium text-highlighted [overflow-wrap:anywhere]" data-mono="id">{{ r.name }}</span>
            <span role="cell" class="min-w-0 [overflow-wrap:anywhere]"><span class="text-muted sm:hidden">Agent: </span><span data-crew-launch-agent>{{ r.agent }}</span></span>
            <span role="cell" class="min-w-0 [overflow-wrap:anywhere]"><span class="text-muted sm:hidden">Model: </span><span :class="r.query.model ? 'font-mono text-[13px]' : 'text-muted'" data-crew-launch-model>{{ r.model }}</span></span>
            <span role="cell" class="min-w-0 [overflow-wrap:anywhere]"><span class="text-muted sm:hidden">Account: </span><span data-crew-launch-account>{{ r.account }}</span></span>
          </div>
        </div>
        <p class="mt-3 text-sm text-muted" data-crew-launch-yolo>{{ launchYoloWords(crew.yolo, crew.inherits) }}</p>
        <!-- Everything else the sheet knows, one press away: the sessions per login, and each member's usage as the server read it. -->
        <details class="group mt-3 text-sm" data-crew-launch-details>
          <summary class="flex w-fit cursor-pointer list-none items-center gap-1 rounded-xs text-muted outline-none hover:text-default focus-visible:ring-2 focus-visible:ring-primary max-sm:min-h-11 [&::-webkit-details-marker]:hidden" data-crew-launch-details-open>
            <UIcon name="i-lucide-chevron-right" class="size-4 transition-transform group-open:rotate-90" />Details: sessions per account and each member's usage
          </summary>
          <div class="mt-2 flex flex-col gap-3">
            <ul v-if="perAccount.length" class="flex flex-col gap-0.5 text-xs text-muted" data-crew-launch-accounts>
              <li v-for="a in perAccount" :key="a.key" :data-crew-launch-account="a.key" :data-launch-account-first="a.first" :data-launch-account-later="a.later">{{ accountCountWords(a) }}</li>
            </ul>
            <div v-for="l in lines" :key="l.name" class="flex flex-col gap-1" :data-crew-launch-usage="l.name">
              <span class="text-xs text-muted"><span class="font-mono text-default" data-mono="id">{{ l.name }}</span> · {{ l.agent }} · usage</span>
              <CapacityPanel :query="open ? l.query : null" compact read-on-open />
            </div>
          </div>
        </details>
      </template>
    </template>
    <template #footer>
      <div ref="footer" class="flex w-full items-center justify-end gap-2">
        <UButton label="Cancel" color="neutral" variant="ghost" :disabled="!open || launching" data-crew-launch-cancel @click="open = false" />
        <UButton label="Launch" icon="i-lucide-play" :loading="launching" :disabled="!open || !crew || launching || notice?.hold || comparison" data-crew-launch-confirm @click="emit('launch', crew?.saved ?? null)" />
      </div>
    </template>
  </UModal>
</template>
