import type { Ref } from 'vue'
import { CommandRefused } from '~/utils/tasks'
import { lostQuestion, recordLost, resumeDestination, resumeQuestion, resumeReceiptWords, type Availability, type RecordReply, type ResumeReceipt } from '~/utils/history'

/** The page's question about a resume: the first ask, a lost record's second one, or none shown. */
export type ResumeStep = 'ask' | 'lost' | null

/**
 * A session record page's state and the requests that change it (Program item HS1, docs/plans/history.md, "web/app", PR-6): the record
 * and its entry's availability read, Resume asked first (a lost record a second time) and Forget. The page draws what this holds.
 *
 * One rule governs every reply: it is applied only while the page still shows what it asked for. Each request carries a ticket, the
 * record id it was asked about and the page's generation when it was asked; the generation moves on when the page moves on (Forget
 * done, another record opened, the page left), so a reply that lands after that changes nothing: a record read overtaken by Forget does
 * not bring the record back, and a resume answered after the person left does not take them anywhere. A resume still `pending` stops
 * being asked again when the page goes; what was sent stays sent, and the server keeps its receipt.
 */
export function useSessionRecord(id: Ref<string>) {
  const admin = useWorkbenchToken()
  const history = useHistory()
  const router = useRouter()
  const toast = useToast()

  const reply = ref<RecordReply | null>(null)
  const record = computed(() => reply.value?.record ?? null)
  const error = ref('')
  const missing = ref(false)
  const forgotten = ref(false)
  /** The record's entry in the history's index, for whether Resume is offered and why not; absent while the index has not read it. */
  const availability = ref<Availability | null>(null)

  /** Resume's question on the page (Resume below): which is shown, if any. */
  const step = ref<ResumeStep>(null)
  /** The second question's words: the record's, or the route's when it found the record open with its session gone. */
  const lostWords = ref<{ title: string; detail: string }>({ title: '', detail: '' })
  const resuming = ref(false)

  // ---- The rule: a reply is applied only while the page still shows what it asked for.
  let generation = 0
  let gone = false
  const life = new AbortController()
  type Ticket = { id: string; generation: number }
  const ask = (): Ticket => ({ id: id.value, generation })
  const holds = (t: Ticket) => !gone && t.generation === generation && t.id === id.value
  /** The page moved on: every reply asked before it is applied no more, and a resume's question about what it showed closes. */
  const moveOn = () => {
    generation++
    step.value = null
    resuming.value = false
  }

  async function load() {
    if (!admin.hasToken.value) {
      admin.needsToken.value = true
      return
    }
    const t = ask()
    try {
      const r = await history.record(t.id)
      if (!holds(t)) return
      reply.value = r
      error.value = ''
      missing.value = false
    } catch (e) {
      if (!holds(t)) return
      const err = e as { status?: number; message?: string }
      if (err.status === 404) {
        missing.value = true
        reply.value = null
      } else error.value = err.message || 'The record could not be read.'
      return
    }
    // The offer is computed as it is asked (docs/protocol.md, History: GET /api/history/entry); the route judges again when Resume is sent.
    history
      .entry(`session:${t.id}`)
      .then((e) => {
        if (holds(t)) availability.value = e.availability
      })
      .catch(() => {
        if (holds(t)) availability.value = null
      })
  }

  const live = computed(() => !!reply.value?.live)
  const lost = computed(() => (record.value ? recordLost(record.value) : false))

  // ---- Resume: asked first; a lost record asked a second time before the route is told the person confirmed it.

  // Whether the session is still listed decides its variables (the route resumes a listed session with its own launch): the entry says.
  const question = computed(() => (record.value ? resumeQuestion(record.value, availability.value ? availability.value.state === 'listed' : undefined) : null))

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
    const t = ask()
    resuming.value = true
    let left = false
    try {
      const res = await history.resumeRecord(r.id, { withoutEnv: q.withoutEnv, confirmLost, signal: life.signal })
      if (!holds(t)) return
      step.value = null
      const to = resumeDestination(res)
      const title = `${r.name || 'The session'} resumed`
      if (to && !to.listed) toast.add({ title, description: [res.notice, 'It is no longer listed: its record opens.'].filter(Boolean).join(' '), icon: 'i-lucide-history', color: 'neutral' })
      else if (res.notice) toast.add({ title, description: res.notice, icon: 'i-lucide-history', color: 'neutral' })
      else toast.add({ title, icon: 'i-lucide-history', color: 'success' })
      if (to) {
        left = true
        await router.push(to.to)
      }
    } catch (e) {
      if (!holds(t)) return
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
      if (holds(t)) {
        resuming.value = false
        // The receipts read again, the new one among them, unless the page has left for the new session.
        if (!left) void load()
      }
    }
  }

  // ---- Forget: asked first.

  const forgetting = ref(false)
  const forgetOpen = ref(false)
  async function forget() {
    const r = record.value
    if (!r) return
    const t = ask()
    forgetting.value = true
    try {
      await history.forgetRecord(r.id)
      if (!holds(t)) return
      // Forgotten: a read asked before this (a failed resume's, the token's) is applied no more.
      moveOn()
      forgetOpen.value = false
      forgotten.value = true
      reply.value = null
      availability.value = null
      toast.add({ title: 'Record forgotten', description: `${r.name || r.id}'s record is deleted from this server.`, icon: 'i-lucide-trash-2', color: 'neutral' })
    } catch (e) {
      if (!holds(t)) return
      toast.add({ title: 'Not forgotten', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
    } finally {
      forgetting.value = false
    }
  }

  watch(id, () => {
    // Another record: what was asked about the last one is applied no more, and its questions close.
    moveOn()
    reply.value = null
    availability.value = null
    forgotten.value = false
    load()
  })
  watch(() => admin.token.value, load)
  onBeforeUnmount(() => {
    gone = true
    moveOn()
    life.abort(new DOMException('The page was left', 'AbortError'))
  })

  return { reply, record, error, missing, forgotten, availability, live, lost, step, lostWords, resuming, question, forgetting, forgetOpen, load, askResume, firstYes, resume, forget }
}
