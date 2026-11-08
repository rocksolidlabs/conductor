import type { PaletteItem } from '~/composables/usePalette'
import type { Action } from '~/utils/actions'
import { parsePrefix, type PaletteGroup } from '~/utils/palette'
import {
  byAnchor,
  classBadge,
  cutWords,
  historyDetail,
  historyIcon,
  historyQuestions,
  historySession,
  historyTarget,
  indexingWords,
  isReceipt,
  linkLine,
  machineOf,
  repeatWords,
  resumeDestination,
  resumeFailure,
  type HistoryItem,
} from '~/utils/history'

/** What `~` alone asks for beside the four questions: the latest sessions, newest first by the filter alone. */
export const LATEST_SESSIONS = 'kind:session'
/** The plain search's History group: asked from three characters typed, its five best rows shown. */
export const HISTORY_MIN_CHARS = 3
export const HISTORY_IN_SEARCH = 5

/**
 * The palette's history (Program item HS1, docs/plans/history.md, "web/app", PR-7): the `~` mode's groups and the plain search's
 * History group, over a search of the palette's own (useHistorySearch). Its rows are the server's answer as ranked, never filtered or
 * ranked again here; each says what it is (its badge), its six fields (the detail line) and its links (the third line), and Enter
 * opens the page that shows its evidence. → offers Open evidence, the session or its record, Resume where the record's entry says it is
 * offered (read as → reveals the actions, by the entry's key and the read's number), Find this elsewhere for a receipt, Copy reference.
 */
export function usePaletteHistory(o: { go: (to: string) => void; token: () => void }) {
  const palette = usePalette()
  const admin = useWorkbenchToken()
  const route = useRoute()
  const copy = useCopy()
  const toast = useToast()
  const history = useHistory()
  const search = useHistorySearch()

  // What the palette's query asks of the history: `~` and its words (the latest sessions with none), or the plain search's words from
  // three characters; nothing while the palette is closed or without the token. The search waits 200 ms after the last key.
  watch(
    () => {
      if (!palette.open.value || !admin.hasToken.value) return null
      const { mode, rest } = parsePrefix(palette.query.value)
      if (mode === 'history') return rest || LATEST_SESSIONS
      if (mode === 'search' && rest.length >= HISTORY_MIN_CHARS) return rest
      return null
    },
    (q) => (q === null ? search.stop() : search.search(q)),
    { immediate: true },
  )

  /** This machine's name, from the latest sessions' records: "Sessions on this machine" asks for it once known. */
  const machine = ref('')
  watch(
    () => search.result.value,
    (r) => {
      if (r?.q !== LATEST_SESSIONS) return
      const m = machineOf(r.reply.entries)
      if (m) machine.value = m
    },
  )
  watch(
    () => admin.token.value,
    () => (machine.value = ''),
  )

  // ---- Resume: whether it is offered is the entry route's to say (the search's rows carry no `resume`), read as → reveals a
  // session's actions, through the search's read rule (useHistorySearch's readEntry: one at a time, the next → aborting it, its answer
  // applied only to the read that asked, a busy refusal asked again, asked again with a new token, cancelled as the palette closes).
  const entryRead = search.entryRead

  async function resume(id: string, label: string) {
    const record = `/history/sessions/${encodeURIComponent(id)}`
    try {
      const r = await history.resumeRecord(id)
      const to = resumeDestination(r)
      toast.add({ title: `${label} resumed`, description: r.notice, icon: 'i-lucide-history', color: 'success' })
      if (to) o.go(to.to)
    } catch (e) {
      // An unknown or pending outcome is said as such, never as "did not resume", and its record opens: its receipts say how it ended.
      // A question only the record's page asks in full (the launch's variables, a lost record's second yes): that page, saying why.
      const f = resumeFailure(e, { unknown: 'its record, opened now, keeps the receipt that says whether it did', pending: 'Its record, opened now, keeps the receipt that says how it ends.' })
      const title = f.outcome === 'refused' || f.outcome === 'failed' ? `${label} did not resume` : `${label}: ${f.title.charAt(0).toLowerCase()}${f.title.slice(1)}`
      toast.add({ title, description: f.description, icon: f.icon, color: f.color })
      if (f.outcome === 'unknown' || f.outcome === 'pending' || f.code === 'env_not_kept' || f.code === 'lost_unconfirmed') o.go(record)
    }
  }

  function resumeAction(e: HistoryItem): Action | null {
    if (e.kind !== 'session') return null
    const s = historySession(e)
    if (!s) return null
    const base = { id: 'resume', kind: 'page' as const, target: e.key, icon: 'i-lucide-history' }
    const read = entryRead.value?.key === e.key ? entryRead.value : null
    if (!read || (!read.entry && !read.error)) return { ...base, label: 'Resume', disabled: true, reason: 'Asking whether it can be resumed…', run: () => {} }
    const r = read.entry?.availability.resume
    if (!r || r.offered !== 'yes') return { ...base, label: 'Resume', disabled: true, reason: r?.why || read.error || 'Not offered for this session', run: () => {} }
    // A lost record asks a second time, and a launch with variables names them: its record's page asks both.
    if (r.confirm) return { ...base, label: 'Resume on its record…', run: () => o.go(`/history/sessions/${encodeURIComponent(s.id)}`) }
    return { ...base, label: 'Resume', ask: { title: `Resume ${e.title}?`, detail: "A new session takes up its agent's own conversation." }, run: () => void resume(s.id, e.title) }
  }

  function item(e: HistoryItem, now: number): PaletteItem {
    const to = historyTarget(e)
    const session = historySession(e)
    const actions: Action[] = []
    if (to) actions.push({ id: 'open', kind: 'page', target: e.key, label: 'Open evidence', icon: 'i-lucide-arrow-right', run: () => o.go(to) })
    if (session?.url && session.url !== to) actions.push({ id: 'open-session', kind: 'page', target: e.key, label: session.listed ? 'Open the session' : 'Open the record', icon: 'i-lucide-terminal', run: () => o.go(session.url!) })
    const r = resumeAction(e)
    if (r) actions.push(r)
    if (isReceipt(e)) {
      actions.push({ id: 'elsewhere', kind: 'page', target: e.key, label: 'Find this elsewhere', icon: 'i-lucide-search', keepOpen: true, run: () => void search.findSimilar(e, 'repo') })
      actions.push({ id: 'elsewhere-all', kind: 'page', target: e.key, label: 'Find this in every repository', icon: 'i-lucide-globe', keepOpen: true, run: () => void search.findSimilar(e, 'all') })
    }
    actions.push({ id: 'copy-ref', kind: 'page', target: e.key, label: 'Copy reference', icon: 'i-lucide-copy', kbds: ['C'], run: () => void copy(e.key, 'Reference copied') })
    return {
      key: `history:${e.key}`,
      label: e.title,
      detail: historyDetail(e, now),
      third: [linkLine(e), repeatWords(e)].filter(Boolean).join(' · ') || undefined,
      badge: classBadge(e) ?? undefined,
      icon: historyIcon(e),
      disabled: !to,
      run: () => to && o.go(to),
      href: to ?? undefined,
      actions,
      reveal: e.kind === 'session' ? () => void search.readEntry(e.key) : undefined,
    }
  }

  const note = (key: string, label: string, detail?: string, icon = 'i-lucide-info'): PaletteItem => ({ key, label, detail, icon, disabled: true, run: () => {} })

  /** The answer's rows, grouped under their anchors (the session or the task), each group where its first row ranked. */
  function anchored(entries: readonly HistoryItem[], now: number): PaletteGroup<PaletteItem>[] {
    return byAnchor(entries).map((g) => ({ id: `history:${g.anchor.ref}`, label: g.anchor.label, rows: g.entries.map((e) => item(e, now)) }))
  }

  /** Find this elsewhere's answer, shown in place of the search's rows (in `~` and in the plain search alike), or null when none was asked. */
  function similarGroup(now: number): PaletteGroup<PaletteItem> | null {
    const sim = search.similar.value
    if (!sim) return null
    const where = sim.scope === 'all' ? 'in every repository' : 'in its repository'
    const rows: PaletteItem[] = sim.reply ? sim.reply.entries.map((e) => item(e, now)) : [note('history:loading', 'Loading…', `The receipts of the same command ${where}`, 'i-lucide-loader')]
    if (sim.reply && !sim.reply.entries.length) rows.push(note('history:similar-none', sim.error ? `Not found: ${sim.error}` : `The same command ran nowhere else ${where}`))
    if (sim.scope === 'repo' && sim.reply) rows.push({ key: 'history:similar-all', label: 'Find this in every repository', icon: 'i-lucide-globe', keepOpen: true, run: () => void search.findSimilar({ key: sim.key, title: sim.title }, 'all') })
    return { id: 'history-similar', label: `Elsewhere: ${sim.title}, ${where}`, rows }
  }

  /** `~`'s groups: the four questions and the latest sessions with nothing typed, the answer grouped under its anchors otherwise. */
  function modeGroups(rest: string, now: number): PaletteGroup<PaletteItem>[] {
    if (!admin.hasToken.value) return [{ id: 'history', label: 'History', rows: [{ key: 'history:token', label: 'History needs the workbench token', detail: 'Enter asks for it', icon: 'i-lucide-key-round', run: () => o.token() }] }]
    const groups: PaletteGroup<PaletteItem>[] = []
    const sim = similarGroup(now)
    if (sim) return [sim]
    if (!rest) {
      const project = route.path.startsWith('/dispatch/') ? String(route.params.project ?? '').toUpperCase() : ''
      const rows = historyQuestions({ project: project || undefined, host: machine.value || undefined }).map<PaletteItem>((q) => ({
        key: `history-question:${q.id}`,
        label: q.label,
        detail: `~ ${q.query}`,
        icon: 'i-lucide-circle-help',
        keepOpen: true,
        run: () => (palette.query.value = `~ ${q.query}`),
      }))
      groups.push({ id: 'history-questions', label: 'Ask the history', rows })
    }
    const q = rest || LATEST_SESSIONS
    const shown = search.result.value?.q === q ? search.result.value.reply : null
    const err = search.error.value?.q === q ? search.error.value : null
    const status: PaletteItem[] = []
    if (!shown && !err) status.push(note('history:loading', 'Loading…', rest ? 'Searching the history' : 'The latest sessions', 'i-lucide-loader'))
    if (err) status.push(note('history:error', err.message, err.code, 'i-lucide-circle-alert'))
    const indexing = indexingWords(shown?.indexing)
    if (indexing) status.push(note('history:indexing', indexing, undefined, 'i-lucide-database'))
    if (status.length) groups.push({ id: 'history-status', label: 'History', rows: status })
    if (shown) {
      if (!rest) {
        if (shown.entries.length) groups.push({ id: 'history-latest', label: 'Latest sessions', rows: shown.entries.slice(0, HISTORY_IN_SEARCH).map((e) => item(e, now)) })
      } else {
        groups.push(...anchored(shown.entries, now))
        if (!shown.entries.length) groups.push({ id: 'history-none', label: 'History', rows: [note('history:none', 'Nothing in the history matches', 'Words are matched whole; filters narrow: class: outcome: project: host: agent: since:')] })
      }
      if (rest && (shown.next || shown.total > shown.entries.length)) {
        const to = `/history?q=${encodeURIComponent(rest)}`
        // A search cut at a bound shows fewer than it counts: the page reaches what was searched, and the row says which bound.
        const total = `${shown.total}${shown.totalCapped ? '+' : ''}`
        const label = shown.cut ? `${total} matches, the searched ones on the History page` : `All ${total} matches on the History page`
        groups.push({ id: 'history-more', label: 'More', rows: [{ key: 'history:page', label, detail: cutWords(shown.cut) || undefined, icon: 'i-lucide-history', run: () => o.go(to), href: to }] })
      }
    }
    return groups
  }

  /** The plain search's History group: from three characters, the answer's five best rows, then "All history matches (~)"; Find this elsewhere's answer in its place once asked from one of them. */
  function searchGroup(rest: string, now: number): PaletteGroup<PaletteItem> {
    const sim = admin.hasToken.value && rest.length >= HISTORY_MIN_CHARS ? similarGroup(now) : null
    if (sim) return sim
    const shown = admin.hasToken.value && rest.length >= HISTORY_MIN_CHARS && search.result.value?.q === rest ? search.result.value.reply : null
    if (!shown?.entries.length) return { id: 'history', label: 'History', rows: [] }
    const rows = shown.entries.slice(0, HISTORY_IN_SEARCH).map((e) => item(e, now))
    rows.push({ key: 'history:all', label: 'All history matches (~)', detail: [`${shown.total}${shown.totalCapped ? '+' : ''} in the history`, cutWords(shown.cut)].filter(Boolean).join('; '), icon: 'i-lucide-history', keepOpen: true, run: () => (palette.query.value = `~ ${rest}`) })
    return { id: 'history', label: 'History', rows }
  }

  return { modeGroups, searchGroup, search }
}
