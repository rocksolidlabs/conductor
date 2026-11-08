<script setup lang="ts">
import { CommandRefused } from '~/utils/tasks'
import { relativeTime, shortCwd } from '~/utils/sessions'
import { lostQuestion, recordAgent, recordEnding, recordEntryWords, recordLost, resumeQuestion, resumeReceiptWords, type Availability, type RecordReply, type ResumeReceipt } from '~/utils/history'

// A session's record (Program item HS1, docs/plans/history.md, "web/app", PR-6): what the server kept of a session it ran, after the
// session is gone from the list as much as while it is listed. Its header (the agent with its version and its own session, the host,
// the folder, the repository, the branch, the commits it started and ended at, how it ended), its run, task and attempt, its entries
// in order, each at `#e<seq>` so a link opens on it, what the record never keeps, and the receipts of earlier resumes. Resume takes up
// the agent's own conversation in a new session (POST …/resume, a command with a receipt: useHistory) and asks first; Forget deletes the
// record and asks first. Nothing on this page starts or ends anything without a person's yes.

const route = useRoute()
const router = useRouter()
const admin = useWorkbenchToken()
const history = useHistory()
const toast = useToast()

const id = computed(() => String(route.params.id))
const reply = ref<RecordReply | null>(null)
const record = computed(() => reply.value?.record ?? null)
const error = ref('')
const missing = ref(false)
const forgotten = ref(false)
/** The record's entry in the history's index, for whether Resume is offered and why not; absent while the index has not read it. */
const availability = ref<Availability | null>(null)
const now = ref(Date.now())

useHead({ title: computed(() => (record.value ? `${record.value.name || record.value.id} · record` : 'Session record')) })

async function load() {
  if (!admin.hasToken.value) {
    admin.needsToken.value = true
    return
  }
  const want = id.value
  try {
    const r = await history.record(want)
    if (want !== id.value) return
    reply.value = r
    error.value = ''
    missing.value = false
  } catch (e) {
    if (want !== id.value) return
    const err = e as { status?: number; message?: string }
    if (err.status === 404) {
      missing.value = true
      reply.value = null
    } else error.value = err.message || 'The record could not be read.'
    return
  }
  // The offer is computed as it is asked (docs/protocol.md, History: GET /api/history/entry); the route judges again when Resume is sent.
  history
    .entry(`session:${want}`)
    .then((e) => {
      if (want === id.value) availability.value = e.availability
    })
    .catch(() => {
      if (want === id.value) availability.value = null
    })
}

// A link to an entry (`#e<seq>`) opens on it once the record is drawn.
const marked = computed(() => (/^#e\d+$/.test(route.hash) ? route.hash.slice(1) : ''))
watch(
  [record, marked],
  async ([r, m]) => {
    if (!r || !m) return
    await nextTick()
    document.getElementById(m)?.scrollIntoView({ block: 'center' })
  },
  { flush: 'post' },
)

watch(id, () => {
  reply.value = null
  availability.value = null
  forgotten.value = false
  load()
})
watch(() => admin.token.value, load)
let tick: number | undefined
onMounted(() => {
  load()
  tick = window.setInterval(() => (now.value = Date.now()), 5000)
})
onBeforeUnmount(() => window.clearInterval(tick))

const live = computed(() => !!reply.value?.live)
const lost = computed(() => (record.value ? recordLost(record.value) : false))
const offer = computed(() => availability.value?.resume)
/** Resume is offered unless the record's entry says no; a check that could not be made (`unknown`) is left to the route, which judges. */
const resumable = computed(() => !!record.value && !live.value && !reply.value?.newer && offer.value?.offered !== 'no')
const short = (c?: string) => (c ? c.slice(0, 12) : '')
const runLink = computed(() => {
  const c = record.value?.crew
  if (!c) return ''
  const q = c.task ? `?view=tasks&task=${encodeURIComponent(c.task)}${c.attempt ? `&attempt=${c.attempt}` : ''}` : ''
  return `/runs/${encodeURIComponent(c.runId)}${q}`
})
const tools = computed(() => Object.entries(record.value?.tools ?? {}).sort((a, b) => b[1] - a[1]))
/** How each question ended: the answer entry that names it (`ref`), by the question's id. */
const endings = computed(() => new Map((record.value?.entries ?? []).filter((e) => e.kind === 'answer' && e.ref).map((e) => [e.ref!, e])))

// ---- Resume: asked first; a lost record asked a second time before the route is told the person confirmed it.

type Step = 'ask' | 'lost' | null
const step = ref<Step>(null)
/** The second question's words: the record's, or the route's when it found the record open with its session gone. */
const lostWords = ref<{ title: string; detail: string }>({ title: '', detail: '' })
const resuming = ref(false)
// Whether the session is still listed decides its variables (the route resumes a listed session with its own launch): the entry says.
const question = computed(() => (record.value ? resumeQuestion(record.value, availability.value ? availability.value.state === 'listed' : undefined) : null))
const modalOpen = computed({
  get: () => step.value !== null,
  set: (v: boolean) => {
    if (!v) step.value = null
  },
})

function askResume() {
  if (record.value) step.value = 'ask'
}

async function firstYes() {
  const r = record.value
  if (!r) return
  if (lost.value) {
    lostWords.value = lostQuestion(r)
    step.value = 'lost'
    return
  }
  await resume(false)
}

async function resume(confirmLost: boolean) {
  const r = record.value
  const q = question.value
  if (!r || !q) return
  resuming.value = true
  let left = false
  try {
    const res = await history.resumeRecord(r.id, { withoutEnv: q.withoutEnv, confirmLost })
    step.value = null
    if (res.notice) toast.add({ title: `${r.name || 'The session'} resumed`, description: res.notice, icon: 'i-lucide-history', color: 'neutral' })
    else toast.add({ title: `${r.name || 'The session'} resumed`, icon: 'i-lucide-history', color: 'success' })
    const next = res.session?.id ?? res.receipt.session
    if (next) {
      left = true
      await router.push(`/sessions/${encodeURIComponent(next)}`)
    }
  } catch (e) {
    const code = e instanceof CommandRefused ? (e.receipt as ResumeReceipt).code : (e as { code?: string }).code
    if (code === 'lost_unconfirmed' && !confirmLost) {
      // The route found the record open with its session no longer listed: its process may still run. Asked a second time.
      lostWords.value = { title: lostQuestion(r).title, detail: (e as Error).message }
      step.value = 'lost'
      return
    }
    step.value = null
    const receipt = e instanceof CommandRefused ? (e.receipt as ResumeReceipt) : null
    const state = receipt?.state ?? ''
    // An uncertain receipt is no refusal: the server stopped while starting it, and its process may have started (its receipt says so).
    if (receipt && state === 'uncertain') toast.add({ title: 'Whether it resumed is not known', description: resumeReceiptWords(receipt), icon: 'i-lucide-circle-help', color: 'warning' })
    else if (state === 'pending') toast.add({ title: 'It is still being started', description: 'Its receipt below says how it ends.', icon: 'i-lucide-loader', color: 'neutral' })
    else toast.add({ title: 'It did not resume', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: e instanceof CommandRefused ? 'warning' : 'error' })
  } finally {
    resuming.value = false
    // The receipts read again, the new one among them, unless the page has left for the new session.
    if (!left) void load()
  }
}

// ---- Forget: asked first.

const forgetting = ref(false)
const forgetOpen = ref(false)
async function forget() {
  const r = record.value
  if (!r) return
  forgetting.value = true
  try {
    await history.forgetRecord(r.id)
    forgetOpen.value = false
    forgotten.value = true
    reply.value = null
    toast.add({ title: 'Record forgotten', description: `${r.name || r.id}'s record is deleted from this server.`, icon: 'i-lucide-trash-2', color: 'neutral' })
  } catch (e) {
    toast.add({ title: 'Not forgotten', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    forgetting.value = false
  }
}
</script>

<template>
  <UDashboardPanel id="history-record">
    <template #header>
      <UDashboardNavbar :toggle="false" :title="record?.name || 'Session record'">
        <template #leading>
          <UIcon name="i-lucide-history" class="size-5 text-muted" />
        </template>
        <template #right>
          <template v-if="record">
            <UButton label="Resume…" icon="i-lucide-history" size="sm" color="primary" variant="soft" :disabled="!resumable" :title="live ? 'The session is still running' : offer?.offered === 'no' ? offer.why : undefined" data-record-resume @click="askResume" />
            <UButton label="Forget…" icon="i-lucide-trash-2" size="sm" color="neutral" variant="ghost" :disabled="live" :title="live ? 'The session is still running' : undefined" data-record-forget @click="forgetOpen = true" />
          </template>
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <div class="mx-auto flex w-full max-w-4xl flex-col gap-5" data-record-page :data-record-id="id">
        <UAlert v-if="error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />
        <UAlert v-if="forgotten" color="neutral" variant="subtle" icon="i-lucide-trash-2" title="This record was forgotten" description="Its file is deleted from this server's data directory, and the history no longer holds anything of it." data-record-forgotten />
        <UAlert v-else-if="missing" color="neutral" variant="subtle" icon="i-lucide-search-x" :title="`No record has the id ${id}`" description="It was forgotten, pruned past the newest 2,000, or never kept (history.sessions off, or a hosted session)." data-record-missing />
        <p v-else-if="!record && !error" class="text-sm text-muted">Reading the record…</p>

        <template v-if="record">
          <UAlert v-if="reply?.newer" color="neutral" variant="subtle" icon="i-lucide-info" title="A later version of Conductor wrote this record" description="It is shown as it is and never rewritten here." />
          <UAlert v-if="lost" color="warning" variant="subtle" icon="i-lucide-circle-help" :title="recordEnding(record)" :description="`The server stopped observing it${record.where ? ` on ${record.where}` : ''}. Resume asks a second time.`" data-record-lost />

          <!-- The header: who ran, where, from which commit, and how it ended. -->
          <section class="flex flex-col gap-2" data-record-header>
            <dl class="grid grid-cols-1 gap-x-4 gap-y-1 text-sm sm:grid-cols-[auto_1fr]">
              <dt class="text-muted">Agent</dt>
              <dd class="min-w-0 break-words" data-record-agent>
                {{ recordAgent(record) }}
                <span v-if="record.agent.session" class="text-muted"> · its own session <code class="font-mono text-xs" data-record-agent-session>{{ record.agent.session.id }}</code><template v-if="!record.agent.session.resumable"> (not resumable)</template></span>
                <span v-else class="text-muted"> · its own session not known</span>
              </dd>
              <dt class="text-muted">Host</dt>
              <dd class="min-w-0 break-words">{{ record.host.name || 'this server' }}<span v-if="record.where" class="text-muted"> · ran {{ record.where === 'native' ? 'natively' : `on ${record.where}` }}</span></dd>
              <dt class="text-muted">Folder</dt>
              <dd class="min-w-0 break-all font-mono text-xs" :title="record.cwd" data-record-cwd>{{ shortCwd(record.cwd) }}</dd>
              <template v-if="record.repo">
                <dt class="text-muted">Repository</dt>
                <dd class="min-w-0 break-all font-mono text-xs" :title="record.repo">{{ shortCwd(record.repo) }}<span v-if="record.branch" class="text-muted"> on {{ record.branch }}</span></dd>
              </template>
              <template v-if="record.startCommit || record.endCommit">
                <dt class="text-muted">Commits</dt>
                <dd class="min-w-0 font-mono text-xs">
                  <span v-if="record.startCommit" :title="record.startCommit" data-record-start-commit>from {{ short(record.startCommit) }}</span>
                  <span v-if="record.endCommit" :title="record.endCommit" data-record-end-commit> → {{ short(record.endCommit) }}</span>
                  <span v-else-if="record.endedAt" class="font-sans text-muted"> · its end commit was not read</span>
                </dd>
              </template>
              <dt class="text-muted">Started</dt>
              <dd :title="record.startedAt">{{ relativeTime(record.startedAt, now, { suffix: true }) }}</dd>
              <dt class="text-muted">Ended</dt>
              <dd :title="record.endedAt || record.lostAt || undefined" data-record-ending>{{ recordEnding(record, live) }}</dd>
              <template v-if="record.crew">
                <dt class="text-muted">Run</dt>
                <dd class="min-w-0 break-words" data-record-crew>
                  <NuxtLink :to="runLink" class="font-mono text-xs hover:underline">{{ record.crew.runId }}</NuxtLink>
                  <span v-if="record.crew.member" class="text-muted"> · member {{ record.crew.member }}</span>
                  <span v-if="record.crew.task" class="text-muted"> · task {{ record.crew.task }}<template v-if="record.crew.attempt">, attempt {{ record.crew.attempt }}</template></span>
                </dd>
              </template>
              <template v-if="record.resumedFrom">
                <dt class="text-muted">Resumed from</dt>
                <dd><NuxtLink :to="`/history/sessions/${encodeURIComponent(record.resumedFrom)}`" class="font-mono text-xs hover:underline" data-record-resumed-from>{{ record.resumedFrom }}</NuxtLink></dd>
              </template>
              <template v-if="record.launch">
                <dt class="text-muted">Launch</dt>
                <dd class="min-w-0 break-words text-xs" data-record-launch>
                  <code v-if="record.launch.argsKept && record.launch.args?.length" class="break-all font-mono">{{ record.launch.args.join(' ') }}</code>
                  <span v-else-if="record.launch.argsKept" class="text-muted">no arguments</span>
                  <span v-else class="text-warning">{{ record.launch.argCount }} arguments, too long to keep: Resume is refused</span>
                  <span v-if="record.launch.envNames?.length" class="text-muted"> · variables {{ record.launch.envNames.join(', ') }} (never their values)</span>
                  <span v-else-if="record.launch.envCount" class="text-muted"> · {{ record.launch.envCount }} variables (never their values)</span>
                  <span v-if="record.launch.yolo" class="text-muted"> · yolo</span>
                </dd>
              </template>
            </dl>
            <p v-if="!live && offer && offer.offered !== 'yes' && offer.why" class="text-sm text-muted" data-record-resume-why>
              {{ offer.why }}<NuxtLink v-if="offer.url" :to="offer.url" class="ml-1 hover:underline">Open it</NuxtLink>
            </p>
          </section>

          <!-- Its entries, in order: what passed through Conductor. -->
          <section class="flex flex-col gap-2" data-record-entries>
            <h2 class="text-xs font-semibold uppercase tracking-wide text-muted">What passed through Conductor</h2>
            <p class="text-xs text-dimmed" data-record-never-kept>A record never keeps what a person types into the terminal or a reply box, nor a typed answer's words, nor a variable's value.</p>
            <p v-if="record.dropped" class="text-xs text-muted" data-record-dropped>{{ record.dropped }} {{ record.dropped === 1 ? 'entry was' : 'entries were' }} let go to keep the record within its bound, the oldest first.</p>
            <p v-if="!record.entries.length" class="text-sm text-dimmed">Nothing was kept.</p>
            <ol v-else class="flex flex-col gap-1.5">
              <li
                v-for="e in record.entries"
                :id="`e${e.seq}`"
                :key="e.seq"
                class="flex min-w-0 flex-col gap-0.5 rounded-md px-3 py-1.5 text-sm ring"
                :class="marked === `e${e.seq}` ? 'bg-primary/5 ring-primary' : 'ring-default'"
                :data-record-entry="e.seq"
                :data-record-entry-kind="e.kind"
                :data-record-entry-marked="marked === `e${e.seq}` ? '' : undefined"
              >
                <div class="flex flex-wrap items-center gap-x-2 text-xs text-muted">
                  <UIcon :name="recordEntryWords(e).icon" class="size-3.5 flex-none" />
                  <span class="font-medium text-highlighted">{{ recordEntryWords(e).label }}</span>
                  <span class="ml-auto" :title="e.at">{{ relativeTime(e.at, now, { suffix: true }) }}</span>
                </div>
                <p v-if="recordEntryWords(e).text" class="whitespace-pre-wrap break-words" data-record-entry-text>{{ recordEntryWords(e).text }}</p>
                <ul v-if="e.choices?.length" class="flex flex-wrap gap-1">
                  <li v-for="c in e.choices" :key="c"><UBadge :label="c" color="neutral" variant="outline" size="sm" /></li>
                </ul>
                <p v-if="e.kind === 'question'" class="text-xs text-muted" data-record-question-ended>
                  <template v-if="e.id && endings.get(e.id)">
                    <a :href="`#e${endings.get(e.id)!.seq}`" class="hover:underline">{{ recordEntryWords(endings.get(e.id)!).label }}</a><template v-if="endings.get(e.id)!.outcome === 'answered' && endings.get(e.id)!.message">: {{ endings.get(e.id)!.message }}</template>
                  </template>
                  <template v-else>Not answered in this record.</template>
                </p>
              </li>
            </ol>
          </section>

          <section v-if="record.files?.length" class="flex flex-col gap-1" data-record-files>
            <h2 class="text-xs font-semibold uppercase tracking-wide text-muted">Files it reported touching</h2>
            <ul class="flex flex-col gap-0.5 text-xs">
              <li v-for="f in record.files" :key="`${f.op}:${f.path}`" class="flex min-w-0 gap-2">
                <span class="w-14 flex-none text-muted">{{ f.op }}</span>
                <span class="min-w-0 flex-1 break-all font-mono">{{ f.path }}</span>
                <span class="flex-none text-muted">×{{ f.count }}</span>
              </li>
            </ul>
          </section>

          <p v-if="tools.length" class="text-xs text-muted" data-record-tools>Tools used: {{ tools.map(([t, n]) => `${t} ×${n}`).join(', ') }}</p>

          <section v-if="record.resumes?.length" class="flex flex-col gap-1" data-record-resumes>
            <h2 class="text-xs font-semibold uppercase tracking-wide text-muted">Resumes from this record</h2>
            <ul class="flex flex-col gap-0.5 text-sm">
              <li v-for="r in record.resumes" :key="r.commandId" class="flex min-w-0 flex-wrap items-baseline gap-x-2" :data-record-resume-receipt="r.commandId" :data-record-resume-state="r.state">
                <UBadge :label="r.state" :color="r.state === 'started' ? 'success' : r.state === 'failed' ? 'neutral' : 'warning'" variant="subtle" size="sm" />
                <span class="min-w-0 break-words" :class="r.state === 'uncertain' ? 'text-warning' : ''">{{ resumeReceiptWords(r) }}</span>
                <NuxtLink v-if="r.state === 'started' && r.session" :to="`/history/sessions/${encodeURIComponent(r.session)}`" class="text-xs hover:underline">its record</NuxtLink>
                <span class="ml-auto text-xs text-muted" :title="r.at">{{ relativeTime(r.at, now, { suffix: true }) }}</span>
              </li>
            </ul>
          </section>
        </template>
      </div>
    </template>
  </UDashboardPanel>

  <UModal v-model:open="modalOpen" :title="step === 'lost' ? lostWords.title : (question?.title ?? '')" :description="step === 'lost' ? lostWords.detail : (question?.detail ?? '')">
    <template #footer>
      <div class="flex w-full justify-end gap-2" :data-record-resume-ask="step">
        <UButton label="Keep it" color="neutral" variant="ghost" @click="step = null" />
        <UButton
          v-if="step === 'ask'"
          :label="question?.withoutEnv && availability ? 'Resume without them' : 'Resume'"
          icon="i-lucide-history"
          color="primary"
          :loading="resuming"
          data-record-resume-confirm
          @click="firstYes"
        />
        <UButton v-else label="Resume anyway" icon="i-lucide-history" color="warning" :loading="resuming" data-record-resume-confirm-lost @click="resume(true)" />
      </div>
    </template>
  </UModal>

  <UModal v-model:open="forgetOpen" :title="`Forget ${record?.name || 'this record'}?`" description="Its record is deleted from this server, and the history no longer finds anything it said. The agent's own conversation is not touched.">
    <template #footer>
      <div class="flex w-full justify-end gap-2">
        <UButton label="Keep it" color="neutral" variant="ghost" @click="forgetOpen = false" />
        <UButton label="Forget" icon="i-lucide-trash-2" color="error" :loading="forgetting" data-record-forget-confirm @click="forget" />
      </div>
    </template>
  </UModal>
</template>
