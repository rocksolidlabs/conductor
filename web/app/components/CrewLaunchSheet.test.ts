import { readFileSync } from 'node:fs'
import { describe, expect, it, vi } from 'vitest'
import { useDispatchApi } from '../composables/useDispatch'
import { AGENT_CHOOSES, AT_LAUNCH, OWN_LOGIN, REFUSED_WORDS, SHEET_DEADLINE_MS, SheetConfirmation, accountName, afterRefusal, launchRefusal, accountUnreadable, addMemberProblem, boundWords, brakemanAccount, cleanFolder, launchAccountWords, launchLines, memberAccountItems, memberAccountProblem, namedAccounts, readNamedAccounts, launchYoloWords, readSheet, refusedTarget, sheetAccountWords, sheetNotes, sheetNotice, sheetRows, UNREVISED_WORDS, withAgent, type SheetTarget } from '../utils/crewLaunch'
import { capacityKey, type After, type CapacityView } from '../utils/capacity'
import { draftMember, toCrewInput, toCrewMember } from '../utils/crews'

// The crew launch sheet's lines and the crew's members on their accounts (docs/plans/capacity.md, PR-7). The app's vitest runs in node
// with no component mounted, so the sheet's lines are tested through the function it draws them from (launchLines), and the sheet's
// source is checked for the panels it draws; what the sheet reads as it opens is capacity-crew.spec.ts's.

/** The server's view of a line's question, as much of it as the words take. */
const view = (v: Partial<CapacityView>) => ({ state: 'never_read', host: { kind: 'native' }, account: { id: 'default' }, ...v }) as CapacityView

const doc = {
  accounts: [
    { id: 'a-aaaaaaaaaaaa', adapter: 'claude', label: 'Work', folder: '/home/me/.claude-work' },
    { id: 'a-bbbbbbbbbbbb', adapter: 'claude', label: 'Old', folder: '/home/me/.claude-old', retired: true },
    { id: 'a-cccccccccccc', adapter: 'codex', label: 'Team', folder: '/home/me/.codex-team' },
  ],
  adapters: [
    { adapter: 'claude', default: 'a-aaaaaaaaaaaa' },
    { adapter: 'codex', default: 'default' },
  ],
}
/** The one sentence for a member that names no model (modelWords). */
const NO_MODEL = 'no model named: the agent chooses, or its arguments do'
const agents = [
  { id: 'claude', name: 'Claude Code', adapter: 'claude' },
  { id: 'codex', name: 'Codex CLI', adapter: 'codex' },
  { id: 'plain', name: 'Plain shell' },
]

describe('the launch sheet', () => {
  it("draws one line per member: its agent, its model and its account, the default named by the server's view of the line", () => {
    const views: Record<string, CapacityView> = { claude: view({ account: { id: 'a-aaaaaaaaaaaa', label: 'Work' } }) }
    const lines = launchLines(
      [
        { name: 'builder', agentId: 'claude' },
        { name: 'reviewer', agentId: 'codex', account: 'a-cccccccccccc', model: 'gpt-e2e' },
        { name: 'shell', agentId: 'plain' },
      ],
      { agents, doc, managed: true, yolo: false, view: (q) => views[q.agent] ?? null },
    )
    expect(lines.map((l) => [l.name, l.agent, l.model, l.account])).toEqual([
      ['builder', 'Claude Code', NO_MODEL, 'the default at the launch (now Work)'],
      ['reviewer', 'Codex CLI', 'gpt-e2e', 'Team'],
      ['shell', 'Plain shell', NO_MODEL, 'the default at the launch'],
    ])
  })

  it("says a member that names no model in the one sentence the run page says, whether its arguments name one or not, never the agent's default", () => {
    // Members as a crew holds them, with the arguments the launch passes; the model its arguments name is the launch's (launchModel).
    const spaced = { name: 'spaced', agentId: 'claude', args: ['--model', 'x'] }
    const joined = { name: 'joined', agentId: 'claude', args: ['--model=x'] }
    const neither = { name: 'neither', agentId: 'claude', args: ['--verbose'] }
    const lines = launchLines([spaced, joined, neither], { agents, doc, managed: false, yolo: false })
    expect(lines.map((l) => l.model)).toEqual([NO_MODEL, NO_MODEL, NO_MODEL])
    expect(lines.map((l) => l.query.model)).toEqual([undefined, undefined, undefined])
    // The run page's Logins say the same sentence for a member whose run records no model.
    expect(boundWords({}, doc).model).toBe(NO_MODEL)
    expect(NO_MODEL).not.toContain("the agent's default")
  })

  it('names the login the server resolves for a member that names none: its entry, its launch, the default; nothing before the view', () => {
    // The accounts say Work is Claude Code's default; the words never take that from them.
    expect(launchAccountWords(doc, undefined, null)).toBe('the default at the launch')
    expect(launchAccountWords(doc, undefined, view({ entryLogin: 'ANTHROPIC_BASE_URL' }))).toBe('the login its entry sets (ANTHROPIC_BASE_URL)')
    expect(launchAccountWords(doc, undefined, view({ state: 'env_set' }))).toBe('the login its launch sets')
    expect(launchAccountWords(doc, undefined, view({}))).toBe('the default at the launch (now Default login)')
    expect(launchAccountWords(doc, undefined, view({ account: { id: 'a-aaaaaaaaaaaa', label: 'Work' } }))).toBe('the default at the launch (now Work)')
    // A member that names an account is said by it, whatever the view.
    expect(launchAccountWords(doc, 'a-cccccccccccc', view({ entryLogin: 'X' }))).toBe('Team')
  })

  it("asks each line's view as its launch would get it: the account only when the member names one, the model, the run's kind and yolo", () => {
    const [builder, reviewer] = launchLines(
      [
        { name: 'builder', agentId: 'claude' },
        { name: 'reviewer', agentId: 'codex', account: 'default', model: ' gpt-e2e ' },
      ],
      { agents, doc, managed: false, yolo: true },
    )
    expect(builder!.query).toEqual({ agent: 'claude', launch: 'legacy', yolo: true })
    expect(reviewer!.query).toEqual({ agent: 'codex', account: 'default', model: 'gpt-e2e', launch: 'legacy', yolo: true })
    // Two members of one agent on one account and model ask one question.
    const twins = launchLines([{ name: 'a', agentId: 'claude' }, { name: 'b', agentId: 'claude' }], { agents, doc, managed: true, yolo: false })
    expect(capacityKey(twins[0]!.query)).toBe(capacityKey(twins[1]!.query))
  })

  it('names an agent the catalog no longer has by its id, and accounts not read by their ids', () => {
    const [l] = launchLines([{ name: 'x', agentId: 'gone', account: 'a-aaaaaaaaaaaa' }], { agents, doc: null, managed: true, yolo: false })
    expect([l!.agent, l!.account]).toEqual(['gone', 'a-aaaaaaaaaaaa'])
  })

  /** A clock of the test's own: a deadline passes when the test says so. */
  function clock() {
    const due = new Set<() => void>()
    const after: After = (_ms, fn) => (due.add(fn), () => due.delete(fn))
    return { after, pass: () => [...due].forEach((f) => (due.delete(f), f())) }
  }
  const saved = { id: 'c', name: 'C', members: [], yolo: undefined as boolean | undefined }

  it("reads the saved crew and, for one that inherits its yolo, the server's default; a crew that sets its own asks none", async () => {
    const asked: string[] = []
    const reads = { crew: async (id: string) => (asked.push(`crew ${id}`), saved), yoloDefault: async () => (asked.push('default'), true) }
    expect(await readSheet({ id: 'c' }, reads, new AbortController().signal)).toEqual({ crew: saved, yolo: true })
    expect(await readSheet({ draft: { ...saved, yolo: false } }, reads, new AbortController().signal)).toEqual({ crew: { ...saved, yolo: false }, yolo: false })
    expect(asked).toEqual(['crew c', 'default'])
  })

  it("refuses a read that never answers at its deadline, in words, the crew's or the default's, and aborts it", async () => {
    const signals: AbortSignal[] = []
    const never = (s: AbortSignal) => (signals.push(s), new Promise<never>(() => {}))
    const c = clock()
    const crew = readSheet({ id: 'c' }, { crew: (_id, s) => never(s), yoloDefault: never }, new AbortController().signal, 5, c.after)
    c.pass()
    expect(await crew).toEqual({ error: 'The server did not answer within 0.005 s', what: 'crew', gone: false })
    const d = clock()
    const def = readSheet({ draft: saved }, { crew: (_id, s) => never(s), yoloDefault: never }, new AbortController().signal, SHEET_DEADLINE_MS, d.after)
    await Promise.resolve()
    d.pass()
    expect(await def).toEqual({ error: 'The server did not answer within 30 s', what: 'default', gone: false })
    expect(signals.map((s) => s.aborted)).toEqual([true, true])
  })

  it('draws nothing of a read the sheet ended (closed, or opened for another target), whatever comes after, and ends its request', async () => {
    let answer!: (c: typeof saved) => void
    let sent!: AbortSignal
    const ended = new AbortController()
    const got = readSheet({ id: 'c' }, { crew: (_id, s) => ((sent = s), new Promise<typeof saved>((r) => (answer = r))), yoloDefault: async () => true }, ended.signal, SHEET_DEADLINE_MS, clock().after)
    ended.abort()
    expect(sent.aborted).toBe(true)
    answer(saved)
    expect(await got).toBeNull()
  })

  it('says a crew the server no longer has is gone', async () => {
    const gone = Object.assign(new Error('no crew c'), { status: 404 })
    expect(await readSheet({ id: 'c' }, { crew: async () => Promise.reject(gone), yoloDefault: async () => true }, new AbortController().signal)).toEqual({ error: 'no crew c', what: 'crew', gone: true })
  })

  it('takes one confirmation per opening: never twice, never while a launch is under way, never from a sheet closed or closing', () => {
    const c = new SheetConfirmation()
    expect(c.take(true), 'before any opening').toBe(0)
    c.opened()
    expect(c.take(false), 'from a sheet closed or closing').toBe(0)
    expect(c.take(true), 'the opening').toBe(1)
    expect(c.take(true), 'the same confirmation again, its launch under way').toBe(0)
    c.done()
    expect(c.take(true), 'the same confirmation after its launch').toBe(0)
    c.opened()
    expect(c.take(true), 'the next opening').toBe(2)
    c.opened()
    expect(c.take(true), 'an opening while a launch is under way').toBe(0)
  })

  it('says whether the opening a launch was confirmed in is still the sheet: not once the sheet was opened again', () => {
    const c = new SheetConfirmation()
    c.opened()
    const n = c.take(true)
    expect(c.holds(n), 'the same opening, its launch under way').toBe(true)
    c.opened()
    expect(c.holds(n), 'opened again while the launch was under way').toBe(false)
    c.done()
    expect(c.holds(c.take(true)), "the new opening's own launch").toBe(true)
  })

  it("reads the crew again by its id once the server refused the launch crew_changed, as a new opening: a draft's too, the run's name kept", () => {
    const byId: SheetTarget = { id: 'pinned', label: 'fix login' }
    const again = refusedTarget(byId, 'pinned', 'changed', true, new SheetConfirmation())
    // A new target even for the same id: the sheet reads again only for a target it has not read (CrewLaunchSheet's watch).
    expect(again).toEqual({ id: 'pinned', label: 'fix login' })
    expect(again).not.toBe(byId)
    // A draft's launch named the revision its save answered: the crew is then read as saved, by the id the save gave it.
    const draft = { key: 'new', draft: {} as never, label: '' } as SheetTarget
    expect(refusedTarget(draft, 'made-2', 'changed', true, new SheetConfirmation())).toEqual({ id: 'made-2', label: '' })
    // A draft whose save answered no revision keeps its target, Launch held (sheetNotice).
    expect(refusedTarget(draft, 'made-2', 'unrevised', true, new SheetConfirmation())).toBe(draft)
    // An opening that is no longer the sheet's is not opened again: the page only says so.
    expect(refusedTarget(byId, 'pinned', 'changed', false, new SheetConfirmation())).toBeNull()
  })

  it('tells a refusal that lands after the page was left by a toast naming the crew, never by the sheet it no longer draws', () => {
    const byId: SheetTarget = { id: 'pinned', label: 'fix login' }
    const crew = { id: 'pinned', name: 'Pinned crew' }
    const here = { confirmed: true, mounted: true, open: true, at: 'pinned', selected: 'pinned' }
    // The page present, its sheet open on that opening: the sheet reads the crew again and says why (sheetNotice), no toast.
    expect(afterRefusal(byId, crew, 'changed', here, new SheetConfirmation())).toEqual({ target: { id: 'pinned', label: 'fix login' }, refused: 'changed' })
    // The page left while the launch was under way: Nuxt keeps its route and its sheet's state as they were, but it is drawn nowhere.
    const c = new SheetConfirmation()
    c.opened()
    const n = c.take(true)
    expect(afterRefusal(byId, crew, 'changed', { ...here, mounted: false }, c)).toEqual({ toast: 'Pinned crew was saved since the launch sheet read it: no run was started.' })
    expect(c.holds(n), 'no new opening of a sheet drawn nowhere').toBe(true)
    expect(afterRefusal(byId, crew, 'unrevised', { ...here, mounted: false }, c)).toEqual({ toast: 'Pinned crew came without a revision from this server: no run was started.' })
    // Closed, opened on another crew, or opened again: the toast too.
    for (const p of [{ open: false }, { selected: 'other' }, { confirmed: false }]) expect(afterRefusal(byId, crew, 'changed', { ...here, ...p }, c)).toHaveProperty('toast')
  })

  // Every code POST /api/crews/{id}/launch answers (internal/api/runs.go, launchSaved and runError; docs/protocol.md), and a launch
  // that reached no server: each stays on the sheet in the server's words, but crew_changed (read again) and not_found (the crew gone).
  it('keeps every refusal of a launch on the sheet in the server’s words, but a crew changed (read again) or gone (closed, said)', () => {
    const err = (code: string, message: string) => Object.assign(new Error(message), { code })
    const kept: Array<[string, string]> = [
      ['managed_off', 'crews with tasks are off on this server (crews.managed)'],
      ['ledger_unavailable', 'the ledger is held by another server (pid 4242)'],
      ['invalid_crew', 'member lead: agent claude is not installed on the server'],
      ['invalid_cwd', 'the folder is outside the allowed roots'],
      ['not_a_repo', 'not a git repository'],
      ['invalid_account', 'member lead: this agent’s entry uses another login'],
      ['invalid_request', 'label: at most 60 characters'],
      ['crew_unreadable', 'pinned.json: not JSON'],
      ['store_unavailable', 'no data directory is configured'],
      ['store_failed', 'could not read the crews'],
      ['launch_failed', 'git is not installed on the server'],
      ['run_stopped', 'the run was stopped'],
      ['switchyard', 'this server coordinates hosted sessions and launches none'],
      ['unauthorized', 'a workbench token is needed'],
      ['network', 'Cannot reach the server'],
    ]
    for (const [code, words] of kept) {
      const why = launchRefusal(err(code, words))
      expect(why, code).toEqual({ code, words })
      // The sheet shows the words as given, Launch not held, and keeps its target (the run's name the person set) as a new opening.
      expect(sheetNotice(why as Exclude<typeof why, 'gone'>, 'drawn', false), code).toEqual({ kind: 'refused', words: `${REFUSED_WORDS} ${words}`, hold: false, code })
      const c = new SheetConfirmation()
      c.opened()
      const n = c.take(true)
      const target: SheetTarget = { id: 'pinned', label: 'fix login' }
      const next = afterRefusal(target, { id: 'pinned', name: 'Pinned crew' }, why as Exclude<typeof why, 'gone'>, { confirmed: true, mounted: true, open: true, at: 'pinned', selected: 'pinned' }, c)
      expect(next, code).toEqual({ target, refused: { code, words } })
      expect(c.holds(n), `${code}: the launch's end leaves the sheet open`).toBe(false)
      // Off the page, a toast names the crew and keeps the words.
      expect(afterRefusal(target, { id: 'pinned', name: 'Pinned crew' }, why as Exclude<typeof why, 'gone'>, { confirmed: true, mounted: false, open: true, at: 'pinned', selected: 'pinned' }, c)).toEqual({ toast: `Pinned crew was refused by the server, so no run was started: ${words}` })
    }
    expect(launchRefusal(err('crew_changed', 'read it again'))).toBe('changed')
    expect(launchRefusal(err('not_found', 'no such crew'))).toBe('gone')
    // A failure with no code still keeps its words on the sheet.
    expect(launchRefusal(new Error('boom'))).toEqual({ code: 'error', words: 'boom' })
  })

  it('keeps the refused sheet open as a new opening, whose Launch takes a confirmation once the refused launch has ended', () => {
    for (const why of ['changed', 'unrevised'] as const) {
      const c = new SheetConfirmation()
      c.opened()
      const n = c.take(true)
      expect(n).toBe(1)
      refusedTarget({ id: 'pinned', label: '' }, 'pinned', why, true, c)
      // The page's launch closes the sheet at its end only while its opening holds: the sheet refused stays open.
      expect(c.holds(n), `${why}: the refused opening is no longer the sheet's`).toBe(false)
      c.done()
      expect(c.take(true), `${why}: the sheet's Launch, once read again`).toBe(2)
    }
    // A refusal for an opening no longer the sheet's opens nothing: the sheet opened since keeps its own confirmation.
    const c = new SheetConfirmation()
    c.opened()
    c.take(true)
    c.opened()
    refusedTarget({ id: 'pinned', label: '' }, 'pinned', 'changed', false, c)
    c.done()
    expect(c.take(true)).toBe(2)
  })

  it("says what the sheet does after a refusal as its read goes, and holds Launch until the crew is drawn again", () => {
    const first = 'The crew was saved since this sheet read it, so nothing was launched.'
    expect(sheetNotice('changed', 'reading', false)).toEqual({ kind: 'changed', words: `${first} It is being read again.`, hold: true })
    // The read's own error is drawn beside it: the notice adds nothing that contradicts it, and Launch has nothing to start.
    expect(sheetNotice('changed', 'failed', false)).toEqual({ kind: 'changed', words: first, hold: true })
    expect(sheetNotice('changed', 'drawn', false)).toEqual({ kind: 'changed', words: `${first} It is shown below as the server holds it now: Launch starts the crew as shown here, and what this sheet says is decided at the launch is decided then.`, hold: false })
    expect(sheetNotice(null, 'drawn', false)).toBeNull()
    expect(sheetNotice(undefined, 'reading', false)).toBeNull()
  })

  it('holds Launch for a crew read without a revision, and for a draft whose save answered none', () => {
    const held = { kind: 'unrevised', words: UNREVISED_WORDS, hold: true }
    expect(UNREVISED_WORDS).toBe('This server does not say which copy of the crew it holds, so this sheet cannot tell which copy Launch would start: nothing is launched. Load the page again.')
    expect(sheetNotice(null, 'drawn', true), 'a saved crew read without one').toEqual(held)
    expect(sheetNotice('changed', 'drawn', true), 'read again without one').toEqual(held)
    expect(sheetNotice('unrevised', 'drawn', false), "a draft's save that answered none").toEqual(held)
    expect(sheetNotice(null, 'reading', true), 'nothing drawn yet: the reading words alone').toBeNull()
  })

  it("says the run's Yolo in one plain line: the crew's setting, or the server's default now", () => {
    expect(launchYoloWords(true, false)).toBe("Yolo: on. The members act without asking (the crew's setting)")
    expect(launchYoloWords(false, false)).toBe("Yolo: off. The members ask before they act (the crew's setting)")
    expect(launchYoloWords(true, true)).toBe("Yolo: on. The members act without asking (the server's default now)")
    expect(launchYoloWords(false, true)).toBe("Yolo: off. The members ask before they act (the server's default now)")
    for (const [y, i] of [[true, true], [true, false], [false, true], [false, false]] as const) expect(launchYoloWords(y, i)).not.toMatch(/Permission prompts/)
  })

  it('draws its table a row a member: member, agent, model and account in plain words, the default named once the view says it', () => {
    const members = [
      { name: 'lead', agentId: 'claude' },
      { name: 'checker', agentId: 'codex', account: 'a-cccccccccccc', model: 'gpt-5' },
      { name: 'pinned', agentId: 'claude' },
    ]
    const lines = launchLines(members, { agents, doc, managed: false, yolo: false })
    const views: Record<string, CapacityView | null> = {
      lead: view({ account: { id: 'a-aaaaaaaaaaaa', label: 'Work' } }),
      checker: null,
      pinned: view({ entryLogin: 'ANTHROPIC_BASE_URL' }),
    }
    const byQuery = (q: { agent: string; account?: string }) => (q.account ? views.checker! : null)
    // Before any view has answered: the default account, named by nothing the page worked out.
    expect(sheetRows(lines, members, doc, byQuery).map((r) => [r.name, r.agent, r.model, r.account])).toEqual([
      ['lead', 'Claude Code', AGENT_CHOOSES, 'Default account'],
      ['checker', 'Codex CLI', 'gpt-5', 'Team'],
      ['pinned', 'Claude Code', AGENT_CHOOSES, 'Default account'],
    ])
    // Each view answered: the default by its name, the login an entry sets.
    let n = 0
    const each = () => [views.lead, views.checker, views.pinned][n++ % 3] ?? null
    expect(sheetRows(lines, members, doc, each).map((r) => r.account)).toEqual(['Work (the default)', 'Team', 'The login its entry sets (ANTHROPIC_BASE_URL)'])
    // The tool's own login, the default: said as it is.
    expect(sheetAccountWords(doc, undefined, view({ account: { id: 'default' } }))).toBe('Default login')
    expect(sheetAccountWords(doc, undefined, view({ state: 'env_set', host: { kind: 'native' } }))).toBe('The login its launch sets')
  })

  it('notes only what needs the person before this launch, saying what it means for it; a version not checked or a failed read stays in Details', () => {
    const rows = launchLines([{ name: 'a', agentId: 'claude' }, { name: 'b', agentId: 'claude' }, { name: 'c', agentId: 'claude' }, { name: 'd', agentId: 'claude' }, { name: 'e', agentId: 'claude' }, { name: 'f', agentId: 'claude' }], { agents, doc, managed: false, yolo: false })
    const r = sheetRows(rows, [], doc, () => null)
    const window = { kind: 'weekly', name: 'Weekly', percent: 95, severity: 'warning' }
    const v: Record<string, CapacityView> = {
      a: view({ state: 'ok', windows: [window] as CapacityView['windows'], credits: [], account: { id: 'a-aaaaaaaaaaaa', label: 'Work' } }),
      b: view({ state: 'ok', windows: [], credits: [], limits: { source: 'provider', reached: '2026-11-18T20:00:00Z' } }),
      c: view({ state: 'signed_out', windows: [], credits: [] }),
      d: view({ state: 'not_installed', windows: [], credits: [] }),
      e: view({ state: 'unqualified', windows: [], credits: [], words: 'Claude Code 2.1.287 is read only from 2.1.322' }),
      f: view({ state: 'error', windows: [], credits: [] }),
    }
    let i = 0
    const names = ['a', 'b', 'c', 'd', 'e', 'f']
    expect(sheetNotes(r, () => v[names[i++ % 6]!]!)).toEqual([
      { member: 'a', color: 'warning', words: 'a may stop before its usage resets: a window of Work is near its limit.' },
      { member: 'b', color: 'error', words: 'b may stop at once: the provider says Default login has reached a limit.' },
      { member: 'c', color: 'error', words: 'c would start signed out: Claude Code is not signed in on Default login. Sign it in first, or it cannot work.' },
      { member: 'd', color: 'error', words: 'd cannot start: Claude Code is not installed on this server.' },
    ])
    // No view yet: nothing to say.
    expect(sheetNotes(r, () => null)).toEqual([])
  })

  it("source: the page sends a launch only with the revision of what it launches and answers every refusal by launchRefusal; the sheet says why", () => {
    const page = readFileSync(new URL('../pages/crews/[[id]].vue', import.meta.url), 'utf8')
    expect(page).toContain("const body = crewLaunchBody(t.label, c.revision)\n    if (!body) return startedNothing(t, c, 'unrevised', confirmed())")
    expect(page).toContain("const why = launchRefusal(e)\n      if (why === 'gone') crewGone(c.id)\n      else startedNothing(t, c, why, confirmed())")
    const src = readFileSync(new URL('./CrewLaunchSheet.vue', import.meta.url), 'utf8')
    expect(src).toContain('<div role="status">')
    expect(src).toContain(':disabled="!open || !crew || launching || notice?.hold || comparison"')
  })

  it("source: the sheet's lines draw compact panels that read on open, the ticket dialog's compact panels do not", () => {
    // The read itself, on an account never read before, is capacity-crew.spec.ts's (the sheet's panels reach `ok`).
    const src = readFileSync(new URL('./CrewLaunchSheet.vue', import.meta.url), 'utf8')
    expect(src).toContain('<CapacityPanel :query="open ? l.query : null" compact read-on-open />')
    const dispatch = readFileSync(new URL('./DispatchLaunch.vue', import.meta.url), 'utf8')
    expect(dispatch).toContain('<CapacityPanel :query="open ? l.query : null" compact />')
    expect(dispatch).not.toContain('read-on-open')
  })
})

describe("the crew editor's Account choice", () => {
  it("offers the default at launch as the server's view names it, the default login and the accounts of the tool not retired, the default marked", () => {
    // The view resolves another default than the accounts' Work: the item says the server's.
    expect(memberAccountItems(doc, 'claude', '', view({ account: { id: 'default' } }))).toEqual([
      { value: AT_LAUNCH, label: 'Default account (now Default login)' },
      { value: 'default', label: 'Default login' },
      { value: 'a-aaaaaaaaaaaa', label: 'Work (default)' },
    ])
    expect(memberAccountItems(doc, 'codex', '', view({ account: { id: 'a-cccccccccccc', label: 'Team' } })).map((i) => i.label)).toEqual(['Default account (now Team)', 'Default login (default)', 'Team'])
  })

  it('names no default and offers no account before the view has answered, or when it failed', () => {
    expect(memberAccountItems(doc, 'claude')).toEqual([{ value: AT_LAUNCH, label: 'Default account' }])
    expect(memberAccountItems(doc, 'claude', 'a-aaaaaaaaaaaa', null)).toEqual([
      { value: AT_LAUNCH, label: 'Default account' },
      { value: 'a-aaaaaaaaaaaa', label: 'Work' },
    ])
  })

  it('keeps the account a member names when it is no choice: retired, or not in Accounts', () => {
    const v = view({ account: { id: 'a-aaaaaaaaaaaa', label: 'Work' } })
    expect(memberAccountItems(doc, 'claude', 'a-bbbbbbbbbbbb', v).at(-1)).toEqual({ value: 'a-bbbbbbbbbbbb', label: 'Old (retired)' })
    expect(memberAccountItems(doc, 'claude', 'a-zzzzzzzzzzzz', v).at(-1)).toEqual({ value: 'a-zzzzzzzzzzzz', label: 'a-zzzzzzzzzzzz (not in Accounts)' })
  })

  it('has no choice for a tool whose login is not moved, or before the accounts are read', () => {
    expect(memberAccountItems(doc, undefined)).toEqual([])
    expect(memberAccountItems(doc, 'goose')).toEqual([])
    expect(memberAccountItems(null, 'claude')).toEqual([])
  })

  it('says an account by its name', () => {
    expect(accountName(doc, 'default')).toBe('Default login')
    expect(accountName(doc, 'a-aaaaaaaaaaaa')).toBe('Work')
    expect(accountName(null, 'a-aaaaaaaaaaaa')).toBe('a-aaaaaaaaaaaa')
  })

  it("offers a launch whose entry or yolo recipe sets its login that login alone, and marks another account with the server's refusal", () => {
    const entry = view({ entryLogin: 'ANTHROPIC_BASE_URL' })
    expect(memberAccountItems(doc, 'claude', '', entry)).toEqual([{ value: AT_LAUNCH, label: 'The login its entry sets (ANTHROPIC_BASE_URL)' }])
    expect(memberAccountItems(doc, 'claude', 'a-aaaaaaaaaaaa', entry).at(-1)).toEqual({ value: 'a-aaaaaaaaaaaa', label: 'Work' })
    expect(memberAccountProblem('a-aaaaaaaaaaaa', entry)).toBe("this agent's entry sets ANTHROPIC_BASE_URL: it uses that login, its default account only")
    // Asked with yolo, as the crew launches: its recipe sets the login.
    const yolo = view({ state: 'env_set' })
    expect(memberAccountItems(doc, 'claude', '', yolo)).toEqual([{ value: AT_LAUNCH, label: 'The login its launch sets' }])
    expect(memberAccountProblem('a-aaaaaaaaaaaa', yolo)).toBe('this launch sets its login: it uses that login, its default account only')
    // The server takes none and the default login for it, any account for a launch whose login nothing sets, and nothing is marked before the view.
    expect([memberAccountProblem(undefined, entry), memberAccountProblem('default', yolo), memberAccountProblem('a-aaaaaaaaaaaa', view({})), memberAccountProblem('a-aaaaaaaaaaaa', null)]).toEqual(['', '', '', ''])
  })

  it("clears a member's account and model when its agent moves to another tool, or between two agents of no tool, and keeps both within one", () => {
    const adapterOf = (id: string) => agents.find((a) => a.id === id)?.adapter
    const m = { name: 'builder', agentId: 'claude', account: 'a-aaaaaaaaaaaa', model: 'opus' }
    expect(withAgent(m, 'codex', adapterOf)).toEqual({ name: 'builder', agentId: 'codex', account: undefined, model: undefined })
    expect(withAgent(m, 'plain', adapterOf)).toMatchObject({ account: undefined, model: undefined })
    const twin = [...agents, { id: 'claude-2', name: 'Claude Code 2', adapter: 'claude' }, { id: 'plain-2', name: 'Plain 2' }]
    expect(withAgent(m, 'claude-2', (id) => twin.find((a) => a.id === id)?.adapter)).toEqual({ ...m, agentId: 'claude-2' })
    expect(withAgent({ ...m, agentId: 'plain' }, 'plain-2', (id) => twin.find((a) => a.id === id)?.adapter)).toEqual({ name: 'builder', agentId: 'plain-2', account: undefined, model: undefined })
  })

  it("saves the account and model a member names, and a member naming neither is the body it was", () => {
    const m = { name: 'builder', agentId: 'claude', prompt: '', start: { when: 'task' as const } }
    expect(JSON.stringify(toCrewMember(m))).toBe('{"name":"builder","agentId":"claude","prompt":"","start":{"when":"task"}}')
    expect(toCrewMember({ ...m, account: 'a-aaaaaaaaaaaa', model: ' opus ' })).toMatchObject({ account: 'a-aaaaaaaaaaaa', model: 'opus' })
    const crew = toCrewInput({ name: 'c', goal: '', cwd: '', where: 'server', isolation: 'none', openAfterLaunch: false, members: [{ ...m, account: 'default', model: '' }] })
    expect(crew.members[0]).toEqual({ name: 'builder', agentId: 'claude', prompt: '', args: undefined, start: { when: 'task', member: undefined }, account: 'default', model: undefined })
  })
})

describe("the run page's logins", () => {
  it('names a bound account by its label, any other login by where it comes from, and the agent own login for none', () => {
    expect(boundWords({ account: { id: 'a-aaaaaaaaaaaa', source: 'account' }, model: 'opus' }, doc)).toEqual({ account: 'Work', model: 'opus', atLaunch: false })
    // While the accounts are read, and once their read failed: said in words, never by the account's id.
    expect(boundWords({ account: { id: 'a-aaaaaaaaaaaa', source: 'account' } }, null).account).toBe('a registered account, not read yet')
    expect(boundWords({ account: { id: 'a-aaaaaaaaaaaa', source: 'account' } }, null, undefined, false, accountUnreadable('e2e: held back')).account).toBe('a registered account that could not be read: e2e: held back')
    expect(boundWords({ account: { id: 'a-aaaaaaaaaaaa', source: 'account' } }, null).account).not.toContain('a-aaaaaaaaaaaa')
    expect(boundWords({}, doc)).toEqual({ account: "the agent's own login", model: 'no model named: the agent chooses, or its arguments do', atLaunch: false })
    expect(boundWords({ account: { id: 'default', source: 'pinned' } }, doc).account).toBe('the login its entry sets')
    expect(boundWords({ account: { id: 'default', source: 'launch_env' } }, doc).account).toBe('the login its launch sets')
    expect(boundWords({ account: { id: 'unlisted', source: 'unlisted' } }, doc).account).toBe('a folder not in Accounts')
    expect(boundWords({}, doc, undefined, true).account).toBe("the login Brakeman's settings name")
  })

  it("says a ticket's run took the default at the launch for a member its revision named no account for", () => {
    const named = namedAccounts([{ name: 'builder' }, { name: 'reviewer', account: 'a-cccccccccccc' }])
    expect(named).toEqual({ builder: '', reviewer: 'a-cccccccccccc' })
    expect(boundWords({ account: { id: 'a-aaaaaaaaaaaa', source: 'account' } }, doc, named.builder).atLaunch).toBe(true)
    expect(boundWords({ account: { id: 'a-cccccccccccc', source: 'account' } }, doc, named.reviewer).atLaunch).toBe(false)
    // A run from no ticket says nothing of it.
    expect(boundWords({ account: { id: 'a-aaaaaaaaaaaa', source: 'account' } }, doc).atLaunch).toBe(false)
  })
})

describe("the run page's Logins: the revision's read", () => {
  it("reads what the revision named for each member, and refuses a read that never answers at the sheet's reads' deadline, aborted", async () => {
    const plan = { body: { crew: { members: [{ name: 'builder' }, { name: 'reviewer', account: 'a-cccccccccccc' }] } } }
    expect(await readNamedAccounts(async () => plan)).toEqual({ builder: '', reviewer: 'a-cccccccccccc' })
    let sent!: AbortSignal
    let deadline = 0
    let pass!: () => void
    const after: After = (ms, fn) => ((deadline = ms), (pass = fn), () => {})
    const never = readNamedAccounts((s) => ((sent = s), new Promise<never>(() => {})), after)
    expect(deadline).toBe(SHEET_DEADLINE_MS)
    pass()
    await expect(never).rejects.toThrow('The server did not answer within 30 s')
    expect(sent.aborted).toBe(true)
    // The Logins say that failure and, opened again, read again: the handler the popover's opening calls.
    const header = readFileSync(new URL('./CrewRunHeader.vue', import.meta.url), 'utf8')
    expect(header).toContain('readNamedAccounts((signal) => dispatchApi.plan(plan, signal))')
    expect(header).toContain('if (namedNow.value?.error) void readNamed()')
  })

  it('hands the read its signal all the way to the request, so the deadline ends the request itself', async () => {
    const asked: Array<{ path: string; signal?: AbortSignal }> = []
    vi.stubGlobal('useApi', () => ({ request: async (path: string, o?: { signal?: AbortSignal }) => (asked.push({ path, signal: o?.signal }), { plan: { id: 'p-1' } }) }))
    vi.stubGlobal('useApiBase', () => ({ httpBase: 'http://api.test' }))
    vi.stubGlobal('useWorkbenchToken', () => ({}))
    try {
      const ctl = new AbortController()
      expect(await useDispatchApi().plan('p-1', ctl.signal)).toEqual({ id: 'p-1' })
      expect(asked).toEqual([{ path: '/api/dispatch/plans/p-1', signal: ctl.signal }])
    } finally {
      vi.unstubAllGlobals()
    }
  })
})

describe("the run page's Logins: a member's model", () => {
  it("says a model the run records, never the agent's default for one it does not (the member's arguments may choose it), and not shown for a Brakeman turn's", () => {
    expect(boundWords({ model: 'opus' }, doc).model).toBe('opus')
    // A member whose arguments chose its model (--model=opus) has none in the run's record: the line does not say the agent's default.
    expect(boundWords({ account: { id: 'a-aaaaaaaaaaaa', source: 'account' } }, doc).model).toBe('no model named: the agent chooses, or its arguments do')
    expect(boundWords({}, doc).model).not.toContain("the agent's default")
    // A Brakeman turn's model is in its arguments: its run records none, so none is said.
    expect(boundWords({}, doc, undefined, true).model).toBe("its model is not shown: a Brakeman turn's run does not record it")
    expect(boundWords({ model: 'opus' }, doc, undefined, true).model).toBe('opus')
  })
})

describe("the run's Add member dialog", () => {
  it('sends the account and model the added member names, and holds Add for a model the field marks', () => {
    const m = draftMember({ name: 'helper', agentId: 'claude', prompt: '', start: { when: 'immediately' }, account: 'a-aaaaaaaaaaaa', model: 'opus' })
    expect(toCrewMember(m)).toEqual({ name: 'helper', agentId: 'claude', prompt: '', args: undefined, start: { when: 'immediately', member: undefined }, account: 'a-aaaaaaaaaaaa', model: 'opus' })
    // The route's body is that member as toCrewMember makes it.
    expect(readFileSync(new URL('../composables/useSessions.ts', import.meta.url), 'utf8')).toContain('/members`, { method: \'POST\', body: toCrewMember(member) }')
    expect(addMemberProblem(m, ['builder'], ['nate'])).toBe('')
    expect(addMemberProblem({ ...m, model: '-bad model' }, ['builder'], [])).toMatch(/^A model is 1 to 100/)
    expect(addMemberProblem({ ...m, name: 'builder' }, ['builder'], [])).not.toBe('')
    expect(addMemberProblem({ ...m, agentId: '' }, [], [])).toBe('Pick an agent')
    expect(addMemberProblem(undefined, [], [])).toBe('Pick an agent')
    // An account the field marks refused holds Add with the field's own words.
    const refused = memberAccountProblem('a-aaaaaaaaaaaa', view({ entryLogin: 'ANTHROPIC_BASE_URL' }))
    expect(addMemberProblem(m, [], [], refused)).toBe(refused)
    expect(refused).not.toBe('')
  })

  it("asks as the server binds a member added to the run, under the run's yolo: a yolo recipe's login alone, and Add held for an account beside it", () => {
    // The server binds an added member under run.Yolo (bindMember): the table is handed the run's yolo, and asks its view with it.
    expect(readFileSync(new URL('./CrewRunHeader.vue', import.meta.url), 'utf8')).toContain(':accounts="accounts.doc.value" :yolo="run?.yolo" single')
    const yoloView = view({ state: 'env_set' })
    expect(memberAccountItems(doc, 'claude', '', yoloView)).toEqual([{ value: AT_LAUNCH, label: 'The login its launch sets' }])
    const m = draftMember({ name: 'helper', agentId: 'yolo-login', prompt: '', start: { when: 'immediately' }, account: 'a-aaaaaaaaaaaa' })
    expect(addMemberProblem(m, [], [], memberAccountProblem(m.account, yoloView))).toBe('this launch sets its login: it uses that login, its default account only')
  })
})

describe("Brakeman's Account choice", () => {
  it('offers the default login, saved as no folder, and each account of the tool not retired by its folder', () => {
    expect(brakemanAccount(doc, 'claude', '')).toEqual({
      items: [
        { value: OWN_LOGIN, label: 'Default login' },
        { value: '/home/me/.claude-work', label: 'Work' },
      ],
      value: OWN_LOGIN,
      unlisted: false,
      retired: '',
    })
  })

  it('shows a folder no account holds by that path as not in Accounts by this path, and a retired account by its label, said refused', () => {
    expect(brakemanAccount(doc, 'claude', '/home/me/elsewhere')).toMatchObject({ value: '/home/me/elsewhere', unlisted: true, retired: '' })
    expect(brakemanAccount(doc, 'claude', '/home/me/elsewhere').items.at(-1)).toEqual({ value: '/home/me/elsewhere', label: '/home/me/elsewhere (not in Accounts by this path)' })
    expect(brakemanAccount(doc, 'claude', '/home/me/.claude-old')).toMatchObject({ value: '/home/me/.claude-old', unlisted: false, retired: 'Old' })
    expect(brakemanAccount(doc, 'claude', '/home/me/.claude-old').items.at(-1)).toEqual({ value: '/home/me/.claude-old', label: 'Old (retired)' })
    expect(brakemanAccount(doc, 'claude', '/home/me/.claude-work')).toMatchObject({ value: '/home/me/.claude-work', unlisted: false })
    // Another tool's account's folder is not this tool's.
    expect(brakemanAccount(doc, 'claude', '/home/me/.codex-team').unlisted).toBe(true)
  })

  it("matches a folder written another way to its account, as the server cleans it: a trailing or doubled separator, a '.' segment", () => {
    for (const spelt of ['/home/me/.claude-work/', '/home/me//.claude-work', '/home/me/./.claude-work', '/home/me/x/../.claude-work']) {
      expect(brakemanAccount(doc, 'claude', spelt), spelt).toMatchObject({ value: '/home/me/.claude-work', unlisted: false, retired: '' })
      expect(brakemanAccount(doc, 'claude', spelt).items, spelt).toHaveLength(2)
    }
    expect(brakemanAccount(doc, 'claude', '/home/me/.claude-old/').retired).toBe('Old')
    expect(cleanFolder('/a//b/./c/')).toBe('/a/b/c')
    expect(cleanFolder('/')).toBe('/')
  })

  it('has no choice without the accounts or for a tool they do not list: the folder is typed then', () => {
    expect(brakemanAccount(null, 'claude', '').items).toEqual([])
    expect(brakemanAccount(doc, undefined, '').items).toEqual([])
    expect(brakemanAccount(null, 'claude', '/home/me/x').unlisted).toBe(false)
  })
})
