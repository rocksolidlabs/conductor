import type { CrewSummary, RunInfo, SessionInfo } from '~/composables/useSessions'
import type { TicketSummary } from '~/composables/useDispatch'
import { parseTicketKey, pickEdit, priorityWord, statusWord, ticketPath, ticketSummaryOf, type PickField } from '~/utils/dispatch'
import { taskCounts } from '~/utils/tasks'
import { usePalette, type PaletteItem } from '~/composables/usePalette'
import type { LaunchPrefill } from '~/composables/useLaunchModal'
import { sameQuestions } from '~/utils/needsCursor'
import { rowActions, type Action } from '~/utils/actions'
import { answersInTerminal, isActive, isEnded, needingInput, waitsForTerminal } from '~/utils/attention'
import type { FeedEntry } from '~/utils/events'
import { joinedDot, joinedPath } from '~/utils/joined'
import { resolveTyped } from '~/utils/fileTree'
import { capGroup, parsePrefix, parseVerb, rank, readRecent, type Named, type PaletteGroup, type ParsedVerb, type Recent } from '~/utils/palette'
import { agentInitials, sessionMeta, shortCwd } from '~/utils/sessions'
import { blockSubtitle, rowState, sessionRow, sidebarModel, type RunBlock, type SessionRow } from '~/utils/sidebar'
import { rowPrompt, sessionLive } from '~/utils/sidebarActions'
import { feedTime, fileOpWords } from '~/utils/events'
import { skipWords } from '~/utils/crews'
import { mergeTouched, type FeedFile } from '~/utils/touchedIndex'
import { sayWords, type SayOutcome } from '~/utils/sayWords'
import { SayDrafts } from '~/utils/sayDrafts'

/**
 * The palette's groups for a query (#302): every row read from the live
 * stores (principle 1), built with the handlers the sidebar's rows and
 * the pages already have (principle 2), ranked by `rank`. The component
 * renders what this returns and runs a row on Enter.
 */
export interface PaletteContext {
  /** What the row's Share… opens (the Share dialog on a session or a run), with the role asked for when a verb named one. */
  share: (target: { sessionId: string; sessionName?: string } | { runId: string; sessionName?: string }, role?: 'view' | 'control') => void
  /** The Launch agent dialog, prefilled when asked (#303). */
  launch: (prefill?: LaunchPrefill) => void
  /** Your name… */
  name: () => void
  /** The workbench token dialog. */
  token: () => void
  /** The Keyboard shortcuts modal. */
  shortcuts: () => void
  /** Closes the palette, for a row that navigates. */
  close: () => void
  /** The time the rows are read at: ages and "how long" are from it. */
  now: () => number
  /** A ticket's picker (Set status…, Set priority…), opened once the palette has closed. */
  pickTicket: (field: PickField, t: TicketSummary) => void
}

const PAGES: Array<{ id: string; label: string; to: string; kbds: string[]; icon: string }> = [
  { id: 'yard', label: 'Yard', to: '/yard', kbds: ['G', 'Y'], icon: 'i-lucide-layout-grid' },
  { id: 'roundhouse', label: 'Roundhouse', to: '/roundhouse', kbds: ['G', 'R'], icon: 'i-lucide-gallery-horizontal' },
  { id: 'crews', label: 'Crews', to: '/crews', kbds: ['G', 'C'], icon: 'i-lucide-users' },
  { id: 'dispatch', label: 'Dispatch', to: '/dispatch', kbds: ['G', 'D'], icon: 'i-lucide-ticket' },
  { id: 'agents', label: 'Agents', to: '/agents', kbds: ['G', 'A'], icon: 'i-lucide-bot' },
  { id: 'events', label: 'Events', to: '/events', kbds: ['G', 'E'], icon: 'i-lucide-radio-tower' },
  { id: 'events-routing', label: 'Events · Routing', to: '/events?tab=routing', kbds: [], icon: 'i-lucide-route' },
  { id: 'events-integrations', label: 'Events · Integrations', to: '/events?tab=integrations', kbds: [], icon: 'i-lucide-plug' },
  { id: 'paste', label: 'Paste invite', to: '/paste', kbds: [], icon: 'i-lucide-clipboard-paste' },
]

/** The Tickets group's rows shown with nothing typed, and with something typed (capGroup). */
const TICKETS_IDLE = 10
const TICKETS_MATCHED = 50

/** The row's second line, in the sidebar's grammar: `agent · where · how long`. */
function sessionDetail(s: SessionInfo, now: number): string {
  return sessionMeta(s, now)
}

export function usePaletteRows(ctx: PaletteContext) {
  const attention = useAttention()
  const events = useEvents()
  const joined = useJoined()
  const desktop = useDesktop()
  const update = useDesktopUpdate()
  const settings = useAttentionSettings()
  const sidebar = useSidebar()
  const folds = useSidebarFolds()
  const colorMode = useColorMode()
  const admin = useWorkbenchToken()
  const fullscreen = useFullscreenToggle()
  const palette = usePalette()
  const router = useRouter()
  const api = useSessions()
  const toast = useToast()
  const copy = useCopy()
  const quick = useQuickReply()
  const shortcutsModal = useShortcutsModal()
  const renameModal = useRenameModal()
  const identity = useIdentity()
  // Through the app's Tickets gate (usePalette), which a snapshot resets: the next edit expects the revision the store has.
  const ticketOps = useDispatchOps(palette.ticketsGate)
  const integrations = ref<Awaited<ReturnType<typeof api.integrations>>['integrations']>([])
  const showExited = ref(false)

  const recent = ref<Recent>({})
  function readRecentNow() {
    recent.value = readRecent(typeof localStorage === 'undefined' ? null : localStorage)
    verbDrafts.clear()
  }

  /** The hooks not installed yet: read once with the lists (a request, so not on open). */
  async function loadIntegrations() {
    if (!admin.hasToken.value) return
    try {
      integrations.value = (await api.integrations()).integrations
    } catch {
      integrations.value = []
    }
  }

  // ---------------------------------------------------------------------------
  // The handlers a row runs, the sidebar's (SessionSidebar.vue) in one place.

  function go(to: string) {
    ctx.close()
    void router.push(to)
  }
  function fail(name: string) {
    return (e: unknown) => toast.add({ title: `${name} failed`, description: (e as Error).message, color: 'error' })
  }
  async function stopSession(s: SessionInfo) {
    try {
      await api.stop(s.id)
      toast.add({ title: `Stopping ${s.name}`, icon: 'i-lucide-square', color: 'neutral' })
    } catch (e) {
      fail(`Stop ${s.name}`)(e)
    }
  }
  async function stopRun(r: RunInfo) {
    try {
      attention.applyRun(await api.stopRun(r.id))
      toast.add({ title: `Stopping ${r.label || r.name}`, description: "Every member's session is stopped.", icon: 'i-lucide-square', color: 'neutral' })
    } catch (e) {
      fail(`Stop ${r.label || r.name}`)(e)
    }
  }
  async function resumeSession(s: SessionInfo) {
    try {
      const r = s.crew ? await api.resumeRunMember(s.crew.runId, s.crew.member) : await api.resumeSession(s.id)
      if (r.notice) toast.add({ title: `${r.session.name} started anew`, description: r.notice, icon: 'i-lucide-rotate-ccw', color: 'neutral' })
      else toast.add({ title: `${r.session.name} resumed`, icon: 'i-lucide-history', color: 'success' })
      go(`/sessions/${r.session.id}`)
    } catch (e) {
      toast.add({ title: 'It did not start', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
    }
  }
  async function resumeRun(r: RunInfo) {
    try {
      const n = await api.resumeRun(r.id)
      attention.applyRun(n)
      toast.add({ title: `${n.label || n.name} resumed as a new run`, icon: 'i-lucide-history', color: 'success' })
      go(`/runs/${encodeURIComponent(n.id)}`)
    } catch (e) {
      fail(`Resume ${r.label || r.name}`)(e)
    }
  }
  /** Copy a view link now: made and copied at once; the toast says where it reaches, with the rendezvous note the Share dialog gives. */
  async function copyViewLink(s: SessionInfo) {
    try {
      const made = await api.createLink(s.id, { role: 'view', ttlSeconds: 7200 })
      const note = made.rendezvous ? `Made here, not on ${made.rendezvous.server}: ${made.rendezvous.error}` : made.remote ? 'Minted by the switchyard: it works from anywhere.' : 'It works where this server is reachable.'
      await copy(made.url, `View link to ${s.name} copied`, note)
    } catch (e) {
      fail(`Link to ${s.name}`)(e)
    }
  }
  function answer(s: SessionInfo, i: number, question: string | undefined) {
    const o = s.attention?.options?.[i]
    if (o) quick.send(s, o.input, { question }).catch((e) => toast.add({ title: `${s.name}: not answered`, description: (e as Error).message, color: 'error' }))
  }
  function reply(s: SessionInfo, text: string, question: string | undefined) {
    return quick.reply(s, text, { question }).catch((e) => toast.add({ title: `${s.name}: not sent`, description: (e as Error).message, color: 'error' }))
  }
  /** A line a session refused (#322), by session: the row's field opens with it again, to send as it is or change. */
  const sayDrafts = reactive(new SayDrafts())
  /**
   * Types a line into a session that is not asking (#303); one waiting on a prompt, or not running, is refused with the
   * broadcast's words. The owner's word says it was typed (#322: submit_ok, or its refusal, the line kept for the field); an
   * older owner says nothing, and the line is said sent.
   */
  async function say(s: SessionInfo, text: string) {
    const line = text.trim()
    if (!line) return
    // A line already on its way to this session: this one is not sent, said so, and the one on its way keeps its generation,
    // so its own word still settles its draft (second whole-batch round).
    if (quick.sending.value.has(s.id)) {
      toast.add({ title: `${s.name}: a line is already on its way`, description: 'Its word comes first; type this one again after it.', icon: 'i-lucide-hourglass', color: 'neutral' })
      return
    }
    // This send's generation: its outcome settles the draft unless a newer send began since (whole-batch round).
    const gen = sayDrafts.begin(s.id)
    const said = (outcome: SayOutcome) => {
      const r = sayWords(s.name, line, outcome)
      sayDrafts.settle(s.id, gen, r.keepDraft, line)
      toast.add({ ...r.toast, icon: r.keepDraft ? 'i-lucide-triangle-alert' : 'i-lucide-keyboard' })
    }
    const live = attention.sessions.value.find((x) => x.id === s.id) ?? s
    const reason = live.attention?.state === 'needs_input' ? 'needs_input' : sessionLive(live.status) ? null : 'not_running'
    if (reason) {
      said({ refused: skipWords(reason) })
      return
    }
    // Looked at again once the connection is up, the latest this page knows: a prompt that came meanwhile refuses the line; the
    // owner's own guard (unlessWaiting) holds what comes after that look. The outcome comes back typed, sent, refused (nothing
    // typed: the line kept for the field) or not confirmed (not offered again); null while a line is already on its way.
    const outcome = await quick.say(s, line, { waiting: () => attention.sessions.value.find((x) => x.id === s.id)?.attention?.state === 'needs_input' })
    if (outcome) said(outcome)
  }
  async function signal(s: SessionInfo, state: 'needs_input' | 'working' | 'clear') {
    try {
      await api.setAttention(s.id, state, state === 'needs_input' ? 'flagged from the workbench' : undefined)
      toast.add({ title: state === 'clear' ? `${s.name}: signal cleared` : `${s.name} marked as ${state === 'needs_input' ? 'needs input' : 'working'}`, icon: 'i-lucide-check', color: 'neutral' })
    } catch (e) {
      fail('Signal')(e)
    }
  }
  async function broadcast(r: RunInfo, text: string) {
    try {
      const res = await api.broadcastRun(r.id, { text })
      const sent = res.sent?.length ?? 0
      const skipped = res.skipped?.length ?? 0
      toast.add({ title: `Sent to ${sent}${skipped ? `, ${skipped} skipped` : ''}`, description: skipped ? res.skipped.map((m) => `${m.member}: ${skipWords(m.reason)}`).join(' · ') : undefined, icon: 'i-lucide-megaphone', color: skipped ? 'warning' : 'success' })
    } catch (e) {
      fail('Broadcast')(e)
    }
  }
  async function launchCrew(c: CrewSummary, label?: string) {
    try {
      const r = await api.launchCrew(c.id, label ? { label } : undefined)
      attention.applyRun(r.run)
      toast.add({ title: `${r.run.label || r.run.name} launched`, icon: 'i-lucide-play', color: 'success' })
      go(`/runs/${encodeURIComponent(r.run.id)}`)
    } catch (e) {
      fail(`Launch ${c.name}`)(e)
    }
  }
  async function startMember(r: RunInfo, name: string) {
    try {
      attention.applyRun(await api.startRunMember(r.id, name))
      toast.add({ title: `${name} started`, icon: 'i-lucide-play', color: 'success' })
    } catch (e) {
      fail(`Start ${name}`)(e)
    }
  }
  async function installHooks(id: string, name: string) {
    try {
      await api.installIntegration(id)
      toast.add({ title: `${name}: hooks installed`, icon: 'i-lucide-check', color: 'success' })
      void loadIntegrations()
    } catch (e) {
      fail(`Install ${name}`)(e)
    }
  }
  /** A path opens in the page's own editor (a page has its own tabs); a page without one says so. */
  function openFile(path: string, line?: number) {
    const page = palette.page.value
    if (!page?.openFile) {
      toast.add({ title: 'Open a session first', description: 'A path opens in the editor of the session page you are on.', color: 'neutral' })
      return
    }
    // As the Files box does: a relative path is the session's working directory's.
    page.openFile({ path: resolveTyped(page.cwd || '/', path), line })
    ctx.close()
  }

  // ---------------------------------------------------------------------------
  // Rows.

  /** Open Files / Chat / Activity / People: the page's own inspector when the row is this page's session, else its page by ?tab= (whole-batch round). */
  function openView(s: SessionInfo, view: 'explorer' | 'chat' | 'activity' | 'people', tab: string) {
    const page = palette.page.value
    if (page?.sessionId === s.id && page.showView) {
      ctx.close()
      page.showView(view)
      return
    }
    go(`/sessions/${s.id}?tab=${tab}`)
  }

  /**
   * A reply or answer verb is bound to the question its row first showed in this open (as a row's draft is), by the session
   * alone, whatever the line becomes as it is typed: another question meanwhile leaves the row disabled, saying so, rather than
   * sending the line to the new question (whole-batch rounds).
   */
  const verbDrafts = new Map<string, string | undefined>()
  function boundQuestion(key: string, current: string | undefined): string | undefined {
    if (!verbDrafts.has(key)) verbDrafts.set(key, current)
    return verbDrafts.get(key)
  }

  /** A session's row: the sidebar's grammar, its actions under →, Enter opens it. */
  function sessionItem(s: SessionInfo, now: number, extra: Partial<PaletteItem> = {}): PaletteItem {
    const row = sessionRow(s)
    const live = sessionLive(s.status)
    const own = rowActions(
      { kind: 'session', name: s.name, live, inRun: !!s.crew, renamable: s.kind === 'server', id: s.id },
      {
        open: () => go(`/sessions/${s.id}`),
        openRun: () => s.crew && go(`/runs/${encodeURIComponent(s.crew.runId)}`),
        share: () => ctx.share({ sessionId: s.id, sessionName: s.name }),
        yard: () => go(`/yard?focus=${encodeURIComponent(s.id)}`),
        rename: () => renameModal.show(s),
        stop: () => void stopSession(s),
      },
    ).flat()
    const more: Action[] = [
      { id: 'copy-link', kind: 'session', target: s.id, label: 'Copy a view link now', icon: 'i-lucide-link', kbds: ['shift', 'S'], disabled: !live, run: () => void copyViewLink(s) },
      { id: 'copy-path', kind: 'session', target: s.id, label: 'Copy path', icon: 'i-lucide-folder', run: () => void copy(s.cwd, 'Path copied') },
      ...(s.branch ? [{ id: 'copy-branch', kind: 'session' as const, target: s.id, label: 'Copy branch', icon: 'i-lucide-git-branch', run: () => void copy(s.branch!, 'Branch copied') }] : []),
      ...(s.agentSession?.id ? [{ id: 'copy-agent-session', kind: 'session' as const, target: s.id, label: "Copy the agent's session id", icon: 'i-lucide-hash', run: () => void copy(s.agentSession!.id, 'Session id copied') }] : []),
      { id: 'open-files', kind: 'session', target: s.id, label: 'Open Files', icon: 'i-lucide-folder-open', run: () => openView(s, 'explorer', 'files') },
      { id: 'open-chat', kind: 'session', target: s.id, label: 'Open Chat', icon: 'i-lucide-message-circle', run: () => openView(s, 'chat', 'chat') },
      { id: 'open-activity', kind: 'session', target: s.id, label: 'Open Activity', icon: 'i-lucide-activity', run: () => openView(s, 'activity', 'activity') },
      { id: 'open-people', kind: 'session', target: s.id, label: 'Open People', icon: 'i-lucide-users', run: () => openView(s, 'people', 'people') },
      { id: 'field', kind: 'session', target: s.id, label: 'Type a line into it', icon: 'i-lucide-keyboard', kbds: ['I'], disabled: !live || s.attention?.state === 'needs_input', run: () => {} },
    ]
    if (isEnded(s.status)) more.unshift({ id: 'resume', kind: 'session', target: s.id, label: s.agentSession?.resumable ? 'Resume' : 'Relaunch', icon: s.agentSession?.resumable ? 'i-lucide-history' : 'i-lucide-rotate-ccw', run: () => void resumeSession(s) })
    const state = rowState(s)
    return {
      key: `session:${s.id}`,
      label: s.name,
      detail: row.prompt?.message ?? sessionDetail(s, now),
      words: [s.cwd, shortCwd(s.cwd), s.hostName ?? '', s.hostUser ?? '', s.branch ?? '', s.agentId, agentInitials(s.agentId), s.crew ? (attention.runNames.value[s.crew.runId] ?? '') : ''],
      agentId: s.agentId,
      state,
      session: s,
      prompt: rowPrompt(row),
      run: () => go(`/sessions/${s.id}`),
      actions: [...own.filter((a) => a.id !== 'open'), ...more],
      field: live ? { placeholder: `A line for ${s.name}…`, button: 'Type', submit: (text: string) => say(s, text), draft: sayDrafts.get(s.id) } : undefined,
      href: `/sessions/${s.id}`,
      yardId: s.id,
      runId: s.crew?.runId,
      ...extra,
    }
  }

  /** Answer all (#303): the same question asked by several live sessions, one row whose choices answer every one. */
  function answerAllItems(): PaletteItem[] {
    return sameQuestions(attention.sessions.value.filter((s) => sessionLive(s.status))).map((g) => {
      const names = g.sessions.map((s) => s.name).join(', ')
      return {
        key: `answer-all:${g.sessions.map((s) => s.id).join(',')}`,
        label: `Answer all ${g.sessions.length}: ${names}`,
        detail: g.message,
        words: [g.message, ...g.sessions.map((s) => s.name)],
        icon: 'i-lucide-reply-all',
        state: 'needs',
        prompt: { message: g.message, choices: g.choices, away: false, held: false, placeholder: '', question: undefined },
        answerAll: (i: number) => {
          const o = g.choices[i]
          if (!o) return
          for (const s of g.sessions) quick.send(s, o.input, { question: s.attention?.id }).catch((e) => toast.add({ title: `${s.name}: not answered`, description: (e as Error).message, color: 'error' }))
          toast.add({ title: `Answered ${g.sessions.length}: ${o.label}`, description: names, icon: 'i-lucide-reply-all', color: 'success' })
        },
        run: () => go(`/sessions/${g.sessions[0]!.id}`),
      }
    })
  }

  /** Launch <agent> here (each agent, in the page's directory) and Launch another like this (#303), when the page has a directory or a session. */
  function launchItems(): PaletteItem[] {
    const page = palette.page.value
    const out: PaletteItem[] = []
    if (page?.cwd) {
      for (const a of palette.catalog.value) out.push({ key: `action:launch-here:${a.id}`, label: `Launch ${a.name} here`, detail: shortCwd(page.cwd), agentId: a.id, run: () => ctx.launch({ agentId: a.id, cwd: page.cwd }) })
    }
    const s = page?.sessionId ? attention.sessions.value.find((x) => x.id === page.sessionId) : undefined
    if (s) {
      // The agent, the directory and yolo: the command a session shows carries the flags a launch generates (its session id, its
      // adapter's, yolo's), never the arguments typed at launch, so none are cloned (Codex's round on #303).
      out.push({ key: `action:launch-like:${s.id}`, label: `Launch another like ${s.name}`, detail: `${s.agentId} · ${shortCwd(s.cwd)}${s.yolo ? ' · yolo' : ''} · the arguments typed at its launch are not kept`, agentId: s.agentId, run: () => ctx.launch({ agentId: s.agentId, cwd: s.cwd, yolo: s.yolo }) })
    }
    return out
  }

  function runItem(block: RunBlock, r: RunInfo | undefined): PaletteItem {
    const live = !block.stoppedAt && block.state !== 'exited'
    const own = rowActions(
      { kind: 'run', name: block.title, live, id: block.runId },
      { open: () => go(`/runs/${encodeURIComponent(block.runId)}`), share: () => ctx.share({ runId: block.runId, sessionName: block.title }), stop: () => r && void stopRun(r) },
    ).flat()
    const more: Action[] = []
    if (r && live) {
      more.push({ id: 'broadcast', kind: 'run', target: r.id, label: 'Broadcast…', icon: 'i-lucide-megaphone', run: () => go(`/runs/${encodeURIComponent(r.id)}`) })
      more.push({ id: 'add-agent', kind: 'run', target: r.id, label: 'Add agent…', icon: 'i-lucide-user-plus', run: () => go(`/runs/${encodeURIComponent(r.id)}`) })
    }
    if (r && !live) more.push({ id: 'resume-run', kind: 'run', target: r.id, label: 'Resume as new run', icon: 'i-lucide-history', run: () => void resumeRun(r) })
    if (r && live && r.managed) more.push({ id: 'tasks', kind: 'run', target: r.id, label: 'Open the tasks', icon: 'i-lucide-list-checks', run: () => go(`/runs/${encodeURIComponent(r.id)}?view=tasks`) })
    const asking = block.members.filter((m) => m.state === 'needs').map((m) => m.session.name)
    // A managed run says its tasks: `2/5 tasks · 1 failed · paused`.
    const tc = r?.managed ? taskCounts(r.tasks) : null
    const detail = [blockSubtitle(block), tc ? `${tc.done}/${tc.total} tasks${tc.failed ? ` · ${tc.failed} failed` : ''}${r?.paused ? ' · paused' : ''}` : '', asking.length ? `${asking.join(', ')} ${asking.length === 1 ? 'asks' : 'ask'}` : '', `${block.members.filter((m) => m.state === 'running' || m.state === 'idle').length} running`, block.members.some((m) => m.state === 'exited') ? `${block.members.filter((m) => m.state === 'exited').length} exited` : '']
      .filter(Boolean)
      .join(' · ')
    return {
      key: `run:${block.runId}`,
      label: block.title,
      detail,
      words: [r?.goal ?? '', r?.cwd ?? '', ...block.members.map((m) => m.session.name)],
      icon: 'i-lucide-play',
      state: block.state,
      run: () => go(`/runs/${encodeURIComponent(block.runId)}`),
      actions: [...own.filter((a) => a.id !== 'open'), ...more],
      href: `/runs/${encodeURIComponent(block.runId)}`,
    }
  }

  function pageItems(): PaletteItem[] {
    const n = attention.count.value
    return PAGES.filter((p) => (p.id !== 'settings' ? true : desktop.isDesktop.value)).map((p) => ({
      key: `go:${p.id}`,
      label: p.id === 'yard' && n ? `Yard · ${n} ${n === 1 ? 'needs' : 'need'} you` : p.label,
      icon: p.icon,
      kbds: p.kbds,
      run: () => go(p.to),
      href: p.to,
    }))
  }

  function settingsItem(): PaletteItem[] {
    return desktop.isDesktop.value ? [{ key: 'go:settings', label: 'Settings', icon: 'i-lucide-settings', kbds: ['G', 'S'], run: () => go('/settings'), href: '/settings' }] : []
  }

  // ---------------------------------------------------------------------------
  // Dispatch's tickets (docs/plans/dispatch.md, "web/app"; Owner decisions, 11: no new prefix, no new verb): the open tickets of every
  // project in the search, a key or a word of the title finding one, a key typed whole ranking its ticket first; → offers Open, Copy key,
  // Set status…, Set priority… and Assign to me.

  /** Assign to me: the display name, as a person, added to the ticket's assignees; one operation, said in a toast. */
  function assignToMe(t: TicketSummary) {
    const name = identity.name.value.trim()
    if (!name) return
    // Queued behind the palette's other edits, and made on the ticket as they left it (usePalette's editTicket).
    void palette.editTicket(t, (x) => ticketOps.editTickets([x], (y) => pickEdit('assignees', y, `person:${name}`, true), { apply: (y) => palette.patchTicket(ticketSummaryOf(y)), done: () => `${t.key} assigned to ${name}` }))
  }

  function ticketItem(t: TicketSummary): PaletteItem {
    const status = statusWord(t.status)
    const name = identity.name.value.trim()
    const mine = !!name && t.assignees.includes(`person:${name}`)
    const assignees = t.assignees.map((a) => (a.startsWith('person:') ? a.slice(7) : a.startsWith('agent:') ? (palette.catalog.value.find((x) => `agent:${x.id}` === a)?.name ?? a.slice(6)) : a))
    const path = ticketPath(t.key)
    return {
      key: `ticket:${t.key}`,
      label: `${t.key} ${t.title}`,
      detail: [status.label, priorityWord(t.priority).label, t.owner ? `owner ${t.owner}` : '', assignees.length ? assignees.join(', ') : ''].filter(Boolean).join(' · '),
      words: [t.key, t.title, t.owner, ...assignees],
      exact: [t.key],
      icon: status.icon,
      run: () => go(path),
      href: path,
      actions: [
        { id: 'open', kind: 'page', target: t.key, label: 'Open', icon: 'i-lucide-arrow-right', run: () => go(path) },
        { id: 'copy-key', kind: 'page', target: t.key, label: 'Copy key', icon: 'i-lucide-copy', kbds: ['C'], run: () => void copy(t.key, `${t.key} copied`) },
        { id: 'set-status', kind: 'page', target: t.key, label: 'Set status…', icon: 'i-lucide-circle-dot', kbds: ['S'], run: () => ctx.pickTicket('status', t) },
        { id: 'set-priority', kind: 'page', target: t.key, label: 'Set priority…', icon: 'i-lucide-signal-high', kbds: ['P'], run: () => ctx.pickTicket('priority', t) },
        { id: 'assign-me', kind: 'page', target: t.key, label: name ? (mine ? `Assigned to you (${name})` : `Assign to me (${name})`) : 'Assign to me: say your name first', icon: 'i-lucide-user-plus', kbds: ['A'], disabled: !name || mine, run: () => assignToMe(t) },
      ],
    }
  }

  /** The rows of the tickets read, made once per read (not per keystroke): a query filters them. */
  const ticketRows = computed(() => (admin.hasToken.value ? palette.tickets.value.map(ticketItem) : []))
  /**
   * The Tickets group: the open tickets read (usePalette.tickets), the latest updated first; a key typed whole that none of them has is a
   * row of its own that opens its page (a closed ticket's, one past the most read), which says what it is.
   */
  function ticketItems(query: string): PaletteItem[] {
    if (!admin.hasToken.value) return []
    const rows = [...ticketRows.value]
    const typed = parseTicketKey(query.trim().toUpperCase())
    if (typed && !palette.tickets.value.some((t) => t.key === typed.key)) {
      rows.unshift({ key: `ticket:${typed.key}`, label: `Open ${typed.key}`, detail: 'Not among the open tickets read: its page says what it is', words: [typed.key], exact: [typed.key], icon: 'i-lucide-ticket', run: () => go(ticketPath(typed.key)), href: ticketPath(typed.key) })
    }
    return rows
  }

  /** `>` alone: every action (the page's group goes first, outside this). */
  function actionItems(): PaletteItem[] {
    const out: PaletteItem[] = []
    out.push({ key: 'action:launch', label: 'Launch an agent', icon: 'i-lucide-play', kbds: ['N'], run: () => ctx.launch() })
    out.push(...launchItems())
    // New ticket in each project not archived (docs/plans/dispatch.md: `>` gains them), the dialog opened by the palette.
    for (const p of palette.projects.value) {
      out.push({ key: `action:new-ticket:${p.key}`, label: `New ticket in ${p.key}`, detail: p.name, icon: 'i-lucide-ticket-plus', run: () => palette.askNewTicket(p.key) })
    }
    const done = attention.sessions.value.filter((s) => sessionLive(s.status) && s.attention?.state === 'done')
    out.push({
      key: 'action:stop-done',
      label: `Stop every session that is done${done.length ? ` · ${done.length}` : ''}`,
      detail: done.length ? done.map((s) => s.name).join(', ') : 'None is done',
      icon: 'i-lucide-square',
      disabled: !done.length,
      ask: { title: `Stop ${done.length} ${done.length === 1 ? 'session' : 'sessions'} that ${done.length === 1 ? 'is' : 'are'} done?`, detail: done.map((s) => s.name).join(', ') },
      run: () => {
        for (const s of done) void stopSession(s)
      },
    })
    const marked = Object.keys(events.marks.value)
    out.push({ key: 'action:clear-marks', label: `Clear the new-event badges${marked.length ? ` · ${marked.length}` : ''}`, icon: 'i-lucide-bell-off', disabled: !marked.length, keepOpen: true, run: () => marked.forEach((id) => events.clearMark(id)) })
    out.push({ key: 'action:notifications', label: 'Browser notifications', icon: 'i-lucide-bell', on: settings.settings.value.notifications, run: () => settings.setNotifications(!settings.settings.value.notifications) })
    out.push({ key: 'action:chime', label: 'Chime when a session needs you', icon: 'i-lucide-bell-ring', on: settings.settings.value.chime, run: () => settings.setChime(!settings.settings.value.chime) })
    const needs = events.routes.value.needs_input
    for (const k of ['badge', 'browser', 'wall', 'feed'] as const) {
      const words: Record<typeof k, string> = { badge: 'the sidebar badge', browser: 'a browser alert', wall: 'the Roundhouse jump', feed: 'the Events feed' }
      out.push({ key: `action:route:${k}`, label: `Needs input → ${words[k]}`, icon: 'i-lucide-route', on: needs[k], run: () => events.setRoute('needs_input', k, !needs[k]) })
    }
    for (const i of integrations.value.filter((i) => i.installable && !i.installed)) out.push({ key: `action:install:${i.id}`, label: `Install hooks for ${i.name}`, icon: 'i-lucide-plug', run: () => void installHooks(i.id, i.name) })
    for (const s of attention.sessions.value.filter((s) => sessionLive(s.status))) out.push({ key: `action:copy-link:${s.id}`, label: `Copy a view link to ${s.name}`, icon: 'i-lucide-link', agentId: s.agentId, run: () => void copyViewLink(s) })
    out.push({ key: 'action:paste-make', label: 'Make a paste invite', icon: 'i-lucide-clipboard-paste', run: () => go('/paste') })
    out.push({ key: 'action:paste-answer', label: 'Answer a paste invite', icon: 'i-lucide-reply', run: () => go('/paste') })
    if (desktop.isDesktop.value) {
      if (update.ready.value) out.push({ key: 'action:update', label: `Install update ${update.ready.value}`, icon: 'i-lucide-circle-arrow-up', run: () => (update.restartFor.value = update.ready.value) })
      out.push({ key: 'action:open-browser', label: 'Open in browser', icon: 'i-lucide-external-link', run: () => void desktop.bridge.value?.openInBrowser() })
      out.push({ key: 'action:server-log', label: 'Server log', icon: 'i-lucide-scroll-text', run: () => void desktop.bridge.value?.showLog() })
      const n = attention.sessions.value.filter((s) => s.kind === 'server' && sessionLive(s.status)).length
      out.push({ key: 'action:restart', label: 'Restart server', icon: 'i-lucide-rotate-ccw', ask: { title: 'Restart the server?', detail: n ? `${n} ${n === 1 ? 'session ends' : 'sessions end'} with it.` : 'No session is running.' }, run: () => void desktop.bridge.value?.restartServer() })
      out.push({ key: 'action:settings', label: 'Settings', icon: 'i-lucide-settings', kbds: ['G', 'S'], run: () => go('/settings') })
    }
    out.push({ key: 'action:name', label: 'Your name…', icon: 'i-lucide-user-round', run: () => ctx.name() })
    out.push({ key: 'action:theme', label: 'Toggle theme', icon: 'i-lucide-sun-moon', run: () => (colorMode.preference = colorMode.value === 'dark' ? 'light' : 'dark') })
    out.push({ key: 'action:token', label: admin.hasToken.value ? 'Workbench token…' : 'Set workbench token…', icon: admin.hasToken.value ? 'i-lucide-key-round' : 'i-lucide-lock', run: () => ctx.token() })
    if (admin.hasToken.value) out.push({ key: 'action:forget-token', label: 'Forget token', icon: 'i-lucide-log-out', run: () => admin.clear() })
    out.push({ key: 'action:shortcuts', label: 'Keyboard shortcuts', icon: 'i-lucide-keyboard', kbds: ['?'], run: () => ctx.shortcuts() })
    out.push({ key: 'action:sidebar', label: sidebar.rail.value ? 'Expand the sidebar' : 'Collapse the sidebar to the rail', icon: 'i-lucide-panel-left', kbds: ['meta', 'B'], run: () => sidebar.toggle() })
    out.push({ key: 'action:fullscreen', label: 'Fullscreen', icon: 'i-lucide-maximize', kbds: ['F'], run: () => void fullscreen.toggle() })
    for (const k of ['needs', 'running', 'shared', 'exited'] as const) {
      const names = { needs: 'Needs you', running: 'Running', shared: 'Shared with you', exited: 'Exited' }
      out.push({ key: `action:fold:${k}`, label: `${folds.folds.value[k] ? 'Unfold' : 'Fold'} the sidebar's ${names[k]}`, icon: folds.folds.value[k] ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right', run: () => folds.toggle(k) })
    }
    return out
  }

  function artifactItems(): PaletteItem[] {
    return [...events.entries.value]
      .reverse()
      .filter((e) => e.event === 'artifact' && e.link)
      .map((e) => ({
        key: `artifact:${e.sessionId}:${e.at}`,
        label: e.message || e.link!,
        detail: `${e.sessionName} · ${e.time}`,
        words: [e.link!],
        icon: 'i-lucide-package',
        run: () => window.open(e.link!, '_blank', 'noopener,noreferrer'),
        href: e.link!,
      }))
  }

  function sharedItems(): PaletteItem[] {
    return joined.list.value.map((e) => ({
      key: `shared:${e.id}`,
      label: e.name,
      detail: `${e.kind === 'run' ? 'crew' : (e.agentId ?? 'session')} · via ${e.host} · ${e.role}`,
      words: [e.host, e.agentId ?? ''],
      icon: e.kind === 'run' ? 'i-lucide-users' : 'i-lucide-globe',
      state: joinedDot(e) === 'running' ? 'running' : joinedDot(e) === 'ended' ? 'exited' : 'idle',
      run: () => go(joinedPath(e)),
      actions: rowActions({ kind: 'shared', name: e.name, live: true, id: e.id }, { open: () => go(joinedPath(e)), forget: () => joined.forget(e.id) }).flat(),
      href: joinedPath(e),
    }))
  }

  /** `#`: live runs with their actions, ended runs, members waiting for Start now, then the saved crews. */
  function runModeGroups(now: number): PaletteGroup<PaletteItem>[] {
    const model = sidebarModel(attention.sessions.value, attention.runOf)
    const blocks = [...model.needs, ...model.running, ...model.exited].filter((it): it is RunBlock => it.kind === 'run')
    const seen = new Set(blocks.map((b) => b.runId))
    const live = blocks.filter((b) => !b.stoppedAt && b.state !== 'exited')
    const ended = blocks.filter((b) => b.stoppedAt || b.state === 'exited')
    const runs: PaletteItem[] = live.map((b) => runItem(b, attention.runOf(b.runId)))
    for (const r of attention.runs.value.filter((r) => !seen.has(r.id) && !r.stoppedAt)) runs.push(runItem({ kind: 'run', id: `run:${r.id}`, runId: r.id, title: r.label || r.name, startedAt: r.startedAt, agents: r.members.length, state: 'idle', needs: 0, members: [] }, r))
    const endedItems: PaletteItem[] = ended.map((b) => runItem(b, attention.runOf(b.runId)))
    const waiting: PaletteItem[] = []
    for (const r of attention.runs.value.filter((r) => !r.stoppedAt)) {
      for (const m of r.members.filter((m) => m.status === 'pending' && m.start.when === 'manual')) {
        waiting.push({ key: `member:${r.id}:${m.name}`, label: `${m.name} · Start now`, detail: `${r.label || r.name} · ${m.agentId} · waiting for a hand`, agentId: m.agentId, icon: 'i-lucide-play', run: () => void startMember(r, m.name) })
      }
    }
    const crews: PaletteItem[] = palette.crews.value.map((c) => {
      const elsewhere = c.where === 'host'
      return {
        key: `crew:${c.id}`,
        label: c.name,
        detail: `${c.goal || 'no goal'} · ${c.members.length} ${c.members.length === 1 ? 'member' : 'members'} · ${shortCwd(c.cwd) || 'no folder'}`,
        words: [c.goal, c.cwd, ...c.members.map((m) => m.name), ...c.members.map((m) => m.agentId)],
        icon: 'i-lucide-users',
        note: elsewhere ? 'Runs on another machine: launch it from its Crews page' : undefined,
        disabled: elsewhere,
        run: () => (elsewhere ? go(`/crews/${encodeURIComponent(c.id)}`) : launchCrew(c)),
        field: elsewhere ? undefined : { placeholder: 'Name the run…', maxLength: 60, button: 'Launch named', submit: (label: string) => launchCrew(c, label.trim() || undefined) },
        actions: [
          ...(elsewhere ? [] : [{ id: 'field', kind: 'page' as const, target: c.id, label: 'Launch named…', icon: 'i-lucide-tag', kbds: ['L'], run: () => {} }]),
          { id: 'edit', kind: 'page', target: c.id, label: 'Edit', icon: 'i-lucide-pencil', run: () => go(`/crews/${encodeURIComponent(c.id)}`) },
          { id: 'runs', kind: 'page', target: c.id, label: "The crew's runs", icon: 'i-lucide-list', run: () => go(`/crews/${encodeURIComponent(c.id)}?tab=runs`) },
          {
            id: 'duplicate',
            kind: 'page',
            target: c.id,
            label: 'Duplicate',
            icon: 'i-lucide-copy',
            run: () =>
              void api
                .duplicateCrew(c.id)
                .then((d) => {
                  toast.add({ title: `${d.name} made`, icon: 'i-lucide-copy', color: 'success' })
                  void palette.loadLists(true)
                })
                .catch(fail(`Duplicate ${c.name}`)),
          },
        ],
        href: `/crews/${encodeURIComponent(c.id)}`,
      }
    })
    const more: PaletteItem[] = [
      { key: 'crews:new', label: 'New crew', icon: 'i-lucide-plus', run: () => go('/crews/new') },
      { key: 'crews:examples', label: 'Load the example crews', icon: 'i-lucide-package-open', run: () => void api.loadExampleCrews().then((r) => { toast.add({ title: `${r.added.length} added${r.skipped.length ? `, ${r.skipped.length} already there` : ''}`, icon: 'i-lucide-package-open', color: 'success' }); void palette.loadLists(true) }).catch(fail('Load the examples')) },
    ]
    void now
    return [
      { id: 'runs', label: 'Runs', rows: runs },
      { id: 'ended', label: 'Ended runs', rows: endedItems },
      { id: 'waiting', label: 'Waiting for a hand', rows: waiting },
      { id: 'crews', label: 'Crews', rows: crews },
      { id: 'crews-more', label: 'Crews · more', rows: more },
    ]
  }

  /**
   * Touched across sessions (#304): the server's index (usePalette.touched, read after the first paint) with the feed's live
   * `file` entries over it; a row of this page's session opens the file in its editor, another session's opens that page
   * with the file (`?open=`).
   */
  function touchedItems(): PaletteItem[] {
    // Nothing without the token: the index is the admin's (whole-batch round).
    if (!admin.hasToken.value) return []
    const page = palette.page.value
    const cwdOf = (id: string) => attention.sessions.value.find((s) => s.id === id)?.cwd
    const feed: FeedFile[] = events.entries.value
      .filter((e): e is FeedEntry => e.event === 'file' && !!e.path)
      .map((e) => ({ sessionId: e.sessionId, sessionName: e.sessionName, cwd: cwdOf(e.sessionId), path: e.path!, op: e.op ?? '', tool: e.tool, by: e.byName, at: e.at }))
    return mergeTouched(palette.touched.value, feed).map((h) => ({
      key: `touched:${h.sessionId}:${h.path}`,
      label: h.rel,
      detail: `${h.sessionName} · ${fileOpWords(h.op)}${h.tool ? ` with ${h.tool}` : ''} · ${feedTime(h.at)}${h.count > 1 ? ` · ${h.count} times` : ''}`,
      words: [h.sessionName, h.tool, h.path],
      icon: 'i-lucide-file-clock',
      run: () => (page?.sessionId === h.sessionId ? openFile(h.path) : go(`/sessions/${encodeURIComponent(h.sessionId)}?open=${encodeURIComponent(h.path)}`)),
      href: page?.sessionId === h.sessionId ? undefined : `/sessions/${encodeURIComponent(h.sessionId)}?open=${encodeURIComponent(h.path)}`,
    }))
  }

  /** `:`: a path opens; then the page's tabs, changes and commits; then Touched across sessions (#304); then Artifacts. */
  function fileModeGroups(rest: string): PaletteGroup<PaletteItem>[] {
    const groups: PaletteGroup<PaletteItem>[] = []
    const m = /^(\S.*?)(?::(\d+))?$/.exec(rest.trim())
    const page = palette.page.value
    if (m && m[1] && /[\\/.]|^[\w-]+$/.test(m[1])) {
      const path = m[1]
      const line = m[2] ? Number(m[2]) : undefined
      groups.push({
        id: 'open',
        label: 'Open',
        pinned: true,
        rows: [{ key: `open:${path}`, label: `Open ${path}${line ? ` at line ${line}` : ''}`, detail: page?.openFile ? 'In this page’s editor' : 'Open a session page first', icon: 'i-lucide-file', disabled: !page?.openFile, run: () => openFile(path, line) }],
      })
    }
    if (page?.files?.length) groups.push({ id: 'page-files', label: page.label, rows: page.files })
    groups.push({ id: 'touched', label: 'Touched', rows: touchedItems() })
    groups.push({ id: 'artifacts', label: 'Artifacts', rows: artifactItems() })
    return groups
  }

  function eventModeGroups(): PaletteGroup<PaletteItem>[] {
    const rows: PaletteItem[] = [...events.entries.value].reverse().slice(0, 200).map((e) => {
      const run = e.event === 'handoff' ? attention.sessions.value.find((s) => s.id === e.sessionId)?.crew?.runId : undefined
      const to = e.event === 'needs_input' ? `/sessions/${e.sessionId}` : run ? `/runs/${encodeURIComponent(run)}` : e.link ? e.link : `/sessions/${e.sessionId}`
      return {
        key: `event:${e.sessionId}:${e.seq}`,
        label: `${e.sessionName}: ${e.detail || e.event}`,
        detail: `${e.event.replace('_', ' ')} · ${e.time}`,
        words: [e.event, e.message ?? '', e.tool ?? '', e.path ?? ''],
        icon: e.event === 'needs_input' ? 'i-lucide-hand' : e.event === 'artifact' ? 'i-lucide-package' : e.event === 'handoff' ? 'i-lucide-arrow-right-left' : 'i-lucide-radio-tower',
        run: () => (e.link && !run && e.event !== 'needs_input' ? window.open(e.link, '_blank', 'noopener,noreferrer') : go(to)),
        href: to,
      }
    })
    return [{ id: 'events', label: 'Events, newest first', rows }]
  }

  function shortcutGroups(): PaletteGroup<PaletteItem>[] {
    return shortcutsModal.groups.value.map((g) => ({
      id: `shortcuts:${g.title}`,
      label: g.title,
      rows: g.rows.map((r) => ({ key: `shortcut:${g.title}:${r.label}`, label: r.label, kbds: r.keys.length ? r.keys : r.terminal, detail: r.terminal && r.keys.length ? `In a terminal: ${r.terminal.join(' + ')}` : undefined, run: () => {}, disabled: true })),
    }))
  }

  function peopleGroups(): PaletteGroup<PaletteItem>[] {
    const page = palette.page.value
    const groups: PaletteGroup<PaletteItem>[] = []
    if (page?.people?.length) groups.push({ id: 'people', label: page.label, rows: page.people })
    groups.push({ id: 'you', label: 'You', rows: [{ key: 'action:name', label: 'Your name…', icon: 'i-lucide-user-round', run: () => ctx.name() }] })
    return groups
  }

  // ---------------------------------------------------------------------------
  // Verbs.

  function names(): Named[] {
    const out: Named[] = []
    for (const s of attention.sessions.value) out.push({ kind: 'session', id: s.id, name: s.name, words: [s.cwd, s.hostName ?? '', s.branch ?? '', s.agentId] })
    for (const r of attention.runs.value) out.push({ kind: 'run', id: r.id, name: r.label || r.name, words: [r.goal] })
    for (const c of palette.crews.value) out.push({ kind: 'crew', id: c.id, name: c.name, words: [c.goal] })
    for (const a of palette.catalog.value) out.push({ kind: 'agent', id: a.id, name: a.name, words: [a.id] })
    return out
  }

  function verbRow(v: ParsedVerb, now: number): PaletteItem | null {
    const session = (id: string) => attention.sessions.value.find((s) => s.id === id)
    const run = (id: string) => attention.runOf(id) ?? attention.runs.value.find((r) => r.id === id)
    switch (v.verb) {
      case 'stop': {
        if (v.target.kind === 'run') {
          const r = run(v.target.id)
          return r ? { key: `verb:stop:${r.id}`, label: `Stop ${v.target.name}`, detail: 'Every member’s terminal closes. The run can be resumed as a new run.', icon: 'i-lucide-square', ask: { title: `Stop ${v.target.name}?`, detail: "Every member's terminal closes. The run can be resumed as a new run." }, run: () => stopRun(r) } : null
        }
        const s = session(v.target.id)
        return s ? { ...sessionItem(s, now), key: `verb:stop:${s.id}`, label: `Stop ${s.name}`, detail: 'Its terminal closes. Resume brings the conversation back from Exited.', ask: { title: `Stop ${s.name}?`, detail: 'Its terminal closes. Resume brings the conversation back from Exited.' }, disabled: !sessionLive(s.status), run: () => stopSession(s), actions: undefined, prompt: null } : null
      }
      case 'share': {
        if (v.target.kind === 'run') return { key: `verb:share:${v.target.id}`, label: `Share ${v.target.name} (${v.role})`, detail: `Opens the Share dialog on the run, a ${v.role} link`, icon: 'i-lucide-share-2', run: () => ctx.share({ runId: v.target.id, sessionName: v.target.name }, v.role) }
        const s = session(v.target.id)
        return s ? { key: `verb:share:${s.id}`, label: `${v.role === 'view' ? 'Copy a view link to' : 'Share'} ${s.name}`, detail: v.role === 'view' ? 'The link is made and copied at once' : 'Opens the Share dialog on control', icon: 'i-lucide-share-2', agentId: s.agentId, run: () => (v.role === 'view' ? copyViewLink(s) : ctx.share({ sessionId: s.id, sessionName: s.name }, v.role)) } : null
      }
      case 'answer': {
        const s = session(v.target.id)
        const o = s?.attention?.options?.[v.choice - 1]
        if (!s) return null
        const bound = boundQuestion(`answer:${s.id}`, s.attention?.id)
        const moved = bound !== s.attention?.id
        return { key: `verb:answer:${s.id}`, label: `Answer ${s.name}: ${v.choice}${o ? ` · ${o.label}` : ''}`, detail: moved ? 'The question moved on: read the row again' : o ? (s.attention?.message ?? '') : 'It has no such choice now', icon: 'i-lucide-reply', agentId: s.agentId, disabled: !o || moved, run: () => answer(s, v.choice - 1, bound) }
      }
      case 'reply': {
        const s = session(v.target.id)
        if (!s) return null
        const asking = s.attention?.state === 'needs_input' && !waitsForTerminal(s.attention) && !answersInTerminal(s.attention)
        const bound = boundQuestion(`reply:${s.id}`, s.attention?.id)
        const moved = bound !== s.attention?.id
        return { key: `verb:reply:${s.id}`, label: `Reply to ${s.name}: “${v.text}”`, detail: moved ? 'The question moved on: read the row again' : asking ? (s.attention?.message ?? 'Typed, then Enter a quarter of a second later') : 'It is not asking a question a reply can answer', icon: 'i-lucide-reply', agentId: s.agentId, disabled: !asking || moved, run: () => reply(s, v.text, bound) }
      }
      case 'say': {
        const s = session(v.target.id)
        if (!s) return null
        const waiting = s.attention?.state === 'needs_input'
        return { key: `verb:say:${s.id}`, label: `Say to ${s.name}: “${v.text}”`, detail: waiting ? skipWords('needs_input') : sessionLive(s.status) ? 'Typed as a line, Enter a quarter of a second later' : skipWords('not_running'), icon: 'i-lucide-keyboard', agentId: s.agentId, disabled: waiting || !sessionLive(s.status), run: () => say(s, v.text) }
      }
      case 'broadcast': {
        const r = run(v.target.id)
        if (!r) return null
        const typed = r.members.filter((m) => m.status === 'running' && !m.needsInput)
        const skipped = r.members.filter((m) => m.status !== 'running' || m.needsInput)
        return { key: `verb:broadcast:${r.id}`, label: `Broadcast to ${v.target.name}: “${v.text}”`, detail: `Send to ${typed.length}${skipped.length ? ` · skipped: ${skipped.map((m) => m.name).join(', ')}` : ''}`, icon: 'i-lucide-megaphone', disabled: !typed.length, run: () => broadcast(r, v.text) }
      }
      case 'launch':
        return { key: `verb:launch:${v.target.id}`, label: `Launch ${v.target.name}${v.dir ? ` in ${v.dir}` : ''}`, detail: 'Opens the Launch dialog, filled in', icon: 'i-lucide-play', agentId: v.target.id, run: () => ctx.launch({ agentId: v.target.id, cwd: v.dir }) }
      case 'run': {
        const c = palette.crews.value.find((c) => c.id === v.target.id)
        if (!c) return null
        const elsewhere = c.where === 'host'
        return { key: `verb:run:${c.id}`, label: `Run ${c.name}${v.label ? ` as “${v.label}”` : ''}`, detail: elsewhere ? 'Runs on another machine: launch it from its Crews page' : c.goal, icon: 'i-lucide-play', disabled: elsewhere, run: () => launchCrew(c, v.label) }
      }
      case 'resume': {
        if (v.target.kind === 'run') {
          const r = run(v.target.id)
          return r ? { key: `verb:resume:${r.id}`, label: `Resume ${v.target.name} as a new run`, icon: 'i-lucide-history', disabled: !r.stoppedAt, detail: r.stoppedAt ? 'A new run of the same crew' : 'It is still running', run: () => resumeRun(r) } : null
        }
        const s = session(v.target.id)
        return s ? { key: `verb:resume:${s.id}`, label: `${s.agentSession?.resumable ? 'Resume' : 'Relaunch'} ${s.name}`, detail: isEnded(s.status) ? (s.agentSession?.resumable ? 'The conversation comes back from Exited' : 'Nothing to resume: it starts anew') : 'It is still running', icon: 'i-lucide-history', agentId: s.agentId, disabled: !isEnded(s.status), run: () => resumeSession(s) } : null
      }
      case 'open':
        return { key: `verb:open:${v.path}`, label: `Open ${v.path}${v.line ? ` at line ${v.line}` : ''}`, detail: palette.page.value?.openFile ? 'In this page’s editor' : 'Open a session page first', icon: 'i-lucide-file', disabled: !palette.page.value?.openFile, run: () => openFile(v.path, v.line) }
      case 'mark': {
        const s = session(v.target.id)
        const state = v.state === 'waiting' ? 'needs_input' : v.state
        return s ? { key: `verb:mark:${s.id}`, label: `Mark ${s.name} as ${v.state === 'clear' ? 'clear' : v.state}`, icon: 'i-lucide-hand', agentId: s.agentId, run: () => signal(s, state) } : null
      }
      case 'go': {
        const p = PAGES.find((p) => p.id === v.page) ?? (v.page === 'settings' ? { to: '/settings', label: 'Settings', icon: 'i-lucide-settings' } : null)
        return p ? { key: `verb:go:${v.page}`, label: `Go to ${p.label}`, icon: p.icon, run: () => go(p.to) } : null
      }
    }
  }

  // ---------------------------------------------------------------------------
  // The groups for a query.

  function searchGroups(query: string, now: number): PaletteGroup<PaletteItem>[] {
    const page = palette.page.value
    const model = sidebarModel(attention.sessions.value, attention.runOf)
    const needs = needingInput(attention.sessions.value).sort((a, b) => (a.attention?.since ?? '').localeCompare(b.attention?.since ?? ''))
    const needsIds = new Set(needs.map((s) => s.id))
    // Every session in the sidebar's order, its run's section aside (a run sits where its most urgent member is: a working
    // member of a run that needs you is Running here, an exited member of a live run Exited), then each by its own state.
    const flat = (items: readonly (SessionRow | RunBlock)[]) => items.flatMap((it) => (it.kind === 'run' ? it.members : [it]))
    const everyRow = flat([...model.needs, ...model.running, ...model.exited])
    const running = everyRow.filter((r) => !needsIds.has(r.id) && isActive(r.session))
    const exited = everyRow.filter((r) => isEnded(r.session.status))
    const blocks = [...model.needs, ...model.running].filter((it): it is RunBlock => it.kind === 'run' && !it.stoppedAt && it.state !== 'exited')
    const groups: PaletteGroup<PaletteItem>[] = [
      { id: 'needs', label: 'Needs you', pinned: true, rows: [...answerAllItems(), ...needs.map((s) => sessionItem(s, now))] },
      ...(page ? [{ id: 'page', label: page.label, pinned: true, rows: page.rows }] : []),
      { id: 'running', label: 'Running', rows: running.map((r) => sessionItem(r.session, now)) },
    ]
    if (exited.length) {
      if (query || showExited.value) groups.push({ id: 'exited', label: 'Exited', rows: exited.map((r) => sessionItem(r.session, now)) })
      else groups.push({ id: 'exited', label: 'Exited', rows: [{ key: 'exited:unfold', label: `${exited.length} exited ${exited.length === 1 ? 'session' : 'sessions'}`, detail: 'Enter unfolds them', icon: 'i-lucide-chevron-right', keepOpen: true, run: () => (showExited.value = true) }] })
    }
    groups.push({ id: 'runs', label: 'Runs', rows: blocks.map((b) => runItem(b, attention.runOf(b.runId))) })
    // A ticket's key typed whole puts the Tickets group first after Needs you and the page's, so Enter opens that ticket.
    const tickets: PaletteGroup<PaletteItem> = { id: 'tickets', label: 'Tickets', rows: ticketItems(query) }
    if (parseTicketKey(query.trim().toUpperCase())) groups.splice(page ? 2 : 1, 0, tickets)
    else groups.push(tickets)
    groups.push({ id: 'shared', label: 'Shared with you', rows: sharedItems() })
    groups.push({ id: 'artifacts', label: 'Artifacts', rows: artifactItems() })
    groups.push({ id: 'goto', label: 'Go to', rows: [...pageItems(), ...settingsItem()] })
    groups.push({ id: 'actions', label: 'Actions', rows: actionItems() })
    return groups
  }

  /** The groups for the query as typed: the mode by its prefix, a verb when the line reads as one, ranked. */
  function groupsFor(query: string): { mode: ReturnType<typeof parsePrefix>['mode']; groups: PaletteGroup<PaletteItem>[]; verb: ParsedVerb | null } {
    const now = ctx.now()
    const { mode, rest } = parsePrefix(query)
    const r = recent.value
    if (mode === 'search') {
      const verb = rest ? parseVerb(rest, names()) : null
      if (verb) {
        const row = verbRow(verb, now)
        if (row) return { mode, verb, groups: [{ id: 'verb', label: 'Do', pinned: true, rows: [row] }, ...rank(searchGroups(rest, now).filter((g) => g.id === 'needs'), '', r, now)] }
      }
      // The Tickets group shows its first rows as ranked: a few with nothing typed (those used lately, then the latest updated), more
      // once something is; the rest are found by typing.
      return { mode, verb: null, groups: capGroup(rank(searchGroups(rest, now), rest, r, now), 'tickets', rest ? TICKETS_MATCHED : TICKETS_IDLE) }
    }
    if (mode === 'actions') {
      const page = palette.page.value
      const groups: PaletteGroup<PaletteItem>[] = [...(page ? [{ id: 'page', label: page.label, pinned: true, rows: page.rows }] : []), { id: 'actions', label: 'Actions', rows: actionItems() }, { id: 'goto', label: 'Go to', rows: [...pageItems(), ...settingsItem()] }]
      // A verb typed under `>` reads as under no prefix: "> stop docs" is the verb, first.
      const verb = rest ? parseVerb(rest, names()) : null
      const row = verb ? verbRow(verb, now) : null
      return { mode, verb: row ? verb : null, groups: [...(row ? [{ id: 'verb', label: 'Do', pinned: true, rows: [row] }] : []), ...rank(groups, rest, r, now)] }
    }
    if (mode === 'runs') return { mode, verb: null, groups: rank(runModeGroups(now), rest, r, now) }
    if (mode === 'files') {
      const groups = fileModeGroups(rest)
      // The typed path is the query: the rows beneath are filtered by it, the Open row stays.
      return { mode, verb: null, groups: rank(groups, rest.replace(/:\d+$/, ''), r, now) }
    }
    if (mode === 'people') return { mode, verb: null, groups: rank(peopleGroups(), rest, r, now) }
    if (mode === 'events') return { mode, verb: null, groups: rank(eventModeGroups(), rest, r, now) }
    return { mode, verb: null, groups: rank(shortcutGroups(), rest, r, now) }
  }

  return { groupsFor, readRecentNow, recent, loadIntegrations, showExited, answer, reply }
}
