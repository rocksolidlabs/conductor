import { describe, expect, it } from 'vitest'
import type { DispatchChange } from '~/composables/useDispatch'
import {
  AssigneePicks,
  HORIZONS,
  assigneesEdit,
  LABEL_COLORS,
  LIST_SORTS,
  PRIORITIES,
  TICKET_STATUSES,
  boardColumns,
  changeWords,
  closeCard,
  closePolicyWords,
  closeRefusalWords,
  closeRequiresOf,
  deliveryLinks,
  acceptChangedWords,
  linkLabel,
  policyOnChange,
  policySave,
  policyWords,
  runLinkedWords,
  samePolicy,
  historyEntries,
  horizonWord,
  isSort,
  labelDot,
  listFocusStep,
  listKeepSelected,
  listRefocus,
  listToggle,
  parseTicketKey,
  priorityWord,
  roadmapColumns,
  sortDesc,
  statusWord,
  ticketPath,
  ticketWords,
  validProjectKey,
} from './dispatch'
import { PICK_KEYS, allPages, boardStep, milestoneProgress, moveEdit, newestRows, paletteListed, patchRows, pickAdds, pickEdit, pickHeld, roadmapStep, targetWords, ticketSummaryOf, typedPick, withMoves, withPatched } from './dispatch'
import type { DispatchTicket, TicketSummary } from '~/composables/useDispatch'
import { candidateName, candidateOptions, candidateRef, candidateSummary, candidateWithdrawn, parseCandidate, ticketDecisions, ticketPeople, verificationWords, verifiedShortfall } from './dispatch'
import type { CandidateReceipt, LinkVerified, PlanSummary, RunCandidate, TaskCandidates, TicketLink } from '~/composables/useDispatch'

describe('the key parser', () => {
  it("reads a ticket's key as the store does: the project's key, a dash, a number without a leading zero", () => {
    expect(parseTicketKey('CND-12')).toEqual({ project: 'CND', number: 12, key: 'CND-12' })
    expect(parseTicketKey('A1-1')).toEqual({ project: 'A1', number: 1, key: 'A1-1' })
    expect(parseTicketKey('ABCDEF-999999999999999999')?.project).toBe('ABCDEF')
    for (const bad of ['cnd-12', 'CND-012', 'CND-0', 'CND-', 'CND12', '-12', 'C-12', 'ABCDEFG-1', '1CN-2', 'CND-12 ', 'CND-1x', 'CND-1234567890123456789', '']) {
      expect(parseTicketKey(bad), bad).toBeNull()
    }
  })
  it("checks a project's key: one upper-case letter, then one to five upper-case letters or digits", () => {
    for (const ok of ['CND', 'A1', 'ABCDEF', 'E2E']) expect(validProjectKey(ok), ok).toBe(true)
    for (const bad of ['C', 'cnd', 'ABCDEFG', '1AB', 'A-B', '']) expect(validProjectKey(bad), bad).toBe(false)
  })
  it("gives a ticket's page by its key, and none for what is no key", () => {
    expect(ticketPath('CND-12')).toBe('/dispatch/CND/12')
    expect(ticketPath('cnd-12')).toBe('')
  })
})

describe('the vocabularies', () => {
  it('name the six statuses in a board order, each with its words, icon and colour, done in the brand colour', () => {
    expect(TICKET_STATUSES).toEqual(['backlog', 'ready', 'in_progress', 'in_review', 'done', 'cancelled'])
    expect(TICKET_STATUSES.map((s) => statusWord(s).label)).toEqual(['Backlog', 'Ready', 'In progress', 'In review', 'Done', 'Cancelled'])
    expect(statusWord('in_progress').color).toBe('success')
    expect(statusWord('in_review').color).toBe('info')
    expect(statusWord('done')).toEqual({ label: 'Done', icon: 'i-lucide-circle-check', color: 'primary' })
    for (const s of TICKET_STATUSES) expect(statusWord(s).icon).toMatch(/^i-lucide-/)
    // A status a newer server has: its own words.
    expect(statusWord('triage').label).toBe('triage')
  })
  it('name the five priorities by their numbers, none to urgent, urgent alone coloured', () => {
    expect(PRIORITIES.map((p) => [p.value, p.name, p.label])).toEqual([
      [0, 'none', 'No priority'],
      [1, 'low', 'Low'],
      [2, 'medium', 'Medium'],
      [3, 'high', 'High'],
      [4, 'urgent', 'Urgent'],
    ])
    expect(PRIORITIES.filter((p) => p.color !== 'neutral').map((p) => p.name)).toEqual(['urgent'])
    expect(priorityWord(3).label).toBe('High')
    expect(priorityWord(9).label).toBe('Priority 9')
  })
  it("name the horizons in the roadmap's order, not planned last and empty", () => {
    expect(HORIZONS.map((h) => [h.value, h.label])).toEqual([
      ['now', 'Now'],
      ['next', 'Next'],
      ['later', 'Later'],
      ['', 'Not planned'],
    ])
    expect(horizonWord('').name).toBe('none')
    expect(horizonWord('next').label).toBe('Next')
  })
  it("map each of the store's twelve label colours to a brand scale, never terracotta, and an unknown one to grey", () => {
    expect(Object.keys(LABEL_COLORS)).toEqual(['gray', 'brown', 'red', 'orange', 'amber', 'yellow', 'lime', 'green', 'teal', 'cyan', 'blue', 'indigo'])
    for (const cls of Object.values(LABEL_COLORS)) {
      expect(cls).toMatch(/^bg-(zinc|forest|signal|active|harbor|brick)-\d{3}$/)
      expect(cls).not.toMatch(/terracotta/)
    }
    expect(labelDot('teal')).toBe('bg-forest-400')
    expect(labelDot('magenta')).toBe(LABEL_COLORS.gray)
  })
  it("name the list's sorts as the store has them, each read in its own direction", () => {
    expect(LIST_SORTS.map((s) => s.value)).toEqual(['updated', 'created', 'priority', 'status', 'title', 'key', 'milestone'])
    expect(sortDesc('updated')).toBe(true)
    expect(sortDesc('priority')).toBe(true)
    expect(sortDesc('title')).toBe(false)
    expect(isSort('key')).toBe(true)
    expect(isSort('rank')).toBe(false)
  })
})

describe("a ticket's words", () => {
  it('say a deleted, a closed, a blocked and an open ticket apart', () => {
    expect(ticketWords({ status: 'in_progress', blocked: false })).toEqual({ text: 'In progress', tone: 'default' })
    expect(ticketWords({ status: 'ready', blocked: true })).toEqual({ text: 'Ready · blocked', tone: 'warning' })
    expect(ticketWords({ status: 'done', blocked: true })).toEqual({ text: 'Done', tone: 'success' })
    expect(ticketWords({ status: 'cancelled', blocked: false })).toEqual({ text: 'Cancelled', tone: 'muted' })
    expect(ticketWords({ status: 'ready', blocked: true, deletedAt: '2026-10-30T10:00:00Z' })).toEqual({ text: 'Deleted', tone: 'muted' })
  })
})

describe("a ticket's assignees", () => {
  it('change by what is added and removed, in one edit, and by nothing when they are the same', () => {
    expect(assigneesEdit([], ['agent:claude'])).toEqual({ add: { assignees: ['agent:claude'] } })
    expect(assigneesEdit(['person:Bob', 'agent:claude'], ['agent:claude', 'person:Ann'])).toEqual({ add: { assignees: ['person:Ann'] }, remove: { assignees: ['person:Bob'] } })
    expect(assigneesEdit(['person:Bob'], [])).toEqual({ remove: { assignees: ['person:Bob'] } })
    // Ticked and unticked before the first edit was sent: nothing is left to send.
    expect(assigneesEdit(['person:Bob', 'agent:claude'], ['agent:claude', 'person:Bob'])).toBeNull()
    expect(assigneesEdit([], [])).toBeNull()
  })
})

describe("a person's picks of a ticket's assignees", () => {
  it('keep two ticks made before the first one\'s edit answers: each edit is made from the picks pending when it is sent', () => {
    const p = new AssigneePicks()
    // Claude ticked: the picker built its list from what it showed. Its edit is sent at once.
    const first = p.pick(p.shown([]), ['agent:claude'])
    const sent = p.take([])
    expect(sent.edit).toEqual({ add: { assignees: ['agent:claude'] } })
    // Codex ticked while that edit is on its way: the picker shows Claude ticked, so the list it builds holds both.
    expect(p.shown([])).toEqual(['agent:claude'])
    const second = p.pick(p.shown([]), ['agent:claude', 'agent:codex'])
    // The first edit answers: the store holds Claude, and Codex is still pending, shown.
    p.settle(sent.carried)
    p.settle(first)
    expect(p.shown(['agent:claude'])).toEqual(['agent:claude', 'agent:codex'])
    // The second edit is made then, from the ticket the first left: it adds Codex and removes nothing.
    const next = p.take(['agent:claude'])
    expect(next.edit).toEqual({ add: { assignees: ['agent:codex'] } })
    p.settle(next.carried)
    p.settle(second)
    expect(p.take(['agent:claude', 'agent:codex']).edit).toBeNull()
    expect(p.shown(['agent:claude', 'agent:codex'])).toEqual(['agent:claude', 'agent:codex'])
  })

  it('send nothing for a member ticked and unticked before its edit went', () => {
    const p = new AssigneePicks()
    p.pick(p.shown(['person:Bob']), ['person:Bob', 'agent:goose'])
    expect(p.shown(['person:Bob'])).toEqual(['person:Bob', 'agent:goose'])
    p.pick(p.shown(['person:Bob']), ['person:Bob'])
    expect(p.shown(['person:Bob'])).toEqual(['person:Bob'])
    expect(p.take(['person:Bob']).edit).toBeNull()
  })

  it('touch only the members the person picked, whatever another person changed meanwhile', () => {
    const p = new AssigneePicks()
    p.pick(['person:Bob'], ['person:Bob', 'agent:claude'])
    p.pick(['person:Bob', 'agent:claude'], ['agent:claude'])
    // Another person assigned Ann, and unassigned Bob already: Ann stays, and Bob is no change.
    expect(p.take(['person:Ann']).edit).toEqual({ add: { assignees: ['agent:claude'] } })
    expect(p.shown(['person:Ann'])).toEqual(['person:Ann', 'agent:claude'])
  })

  it('keep a member picked again while the edit carrying its first pick is on its way, and let go of one whose edit failed', () => {
    const p = new AssigneePicks()
    p.pick([], ['agent:claude'])
    const sent = p.take([])
    // Unticked while the tick's edit is on its way; the tick's edit answers, and the store holds Claude.
    p.pick(['agent:claude'], [])
    p.settle(sent.carried)
    expect(p.shown(['agent:claude'])).toEqual([])
    expect(p.take(['agent:claude']).edit).toEqual({ remove: { assignees: ['agent:claude'] } })
    // An edit refused: its picks are no longer pending, and the picker shows the store's.
    const q = new AssigneePicks()
    const made = q.pick([], ['agent:codex'])
    q.settle(q.take([]).carried)
    q.settle(made)
    expect(q.shown([])).toEqual([])
    // Another ticket: every pick forgotten.
    q.pick([], ['agent:goose'])
    q.clear()
    expect(q.shown(['person:Bob'])).toEqual(['person:Bob'])
  })
})

describe("the close policy's words", () => {
  it('say what closing takes, what is met, and why an acceptance on record is not', () => {
    const w = closePolicyWords({ requires: ['acceptance', 'delivery'], met: { acceptance: false, delivery: true }, missing: ['acceptance'], why: 'accepted for Alice; the owner is now Bob' })
    expect(w.summary).toBe("To close: the owner's acceptance · a delivery link")
    expect(w.items).toEqual([
      { requirement: 'acceptance', words: "the owner's acceptance", met: false },
      { requirement: 'delivery', words: 'a delivery link', met: true },
    ])
    expect(w.why).toBe('accepted for Alice; the owner is now Bob')
    expect(closePolicyWords({ requires: [], met: { acceptance: false, delivery: false }, missing: [] })).toEqual({ summary: 'Closes with nothing required', items: [], why: '' })
  })
  it('say what a refused move to Done lacks', () => {
    expect(closeRefusalWords(['acceptance'])).toBe("It needs the owner's acceptance to be done.")
    expect(closeRefusalWords(['acceptance', 'delivery'])).toBe("It needs the owner's acceptance and a delivery link to be done.")
    expect(closeRefusalWords([])).toBe('The close policy refused it.')
  })
  it("say a run held by another ticket, the receipt's key naming it", () => {
    expect(runLinkedWords('CND-3')).toBe('The run is linked to CND-3 already: a run belongs to one ticket.')
    expect(runLinkedWords()).toBe('The run is linked to another ticket already: a run belongs to one ticket.')
  })
})

describe("the close card's words (PR-11)", () => {
  const link = (kind: 'delivery' | 'run' | 'external', ref: string, deletedAt?: string) => ({ id: ref, kind, ref, url: `https://h/${ref}`, note: '', createdAt: '', createdBy: '', ...(deletedAt ? { deletedAt } : {}) })
  const base = {
    owner: 'Alice',
    status: 'in_review' as const,
    links: [] as ReturnType<typeof link>[],
    close: { requires: ['acceptance', 'delivery'] as Array<'acceptance' | 'delivery'>, met: { acceptance: false, delivery: false }, missing: ['acceptance', 'delivery'] as Array<'acceptance' | 'delivery'> },
  }
  it('say what closing takes and that nothing of it is met yet, the owner named', () => {
    const c = closeCard(base)
    expect(c.summary).toBe("To close: the owner's acceptance · a delivery link")
    expect(c.items.map((i) => [i.requirement, i.met, i.state])).toEqual([
      ['acceptance', false, 'Not accepted yet: Alice is the owner'],
      ['delivery', false, 'No delivery link yet'],
    ])
    expect([c.ready, c.closed, c.acceptBlocked]).toEqual([false, false, ''])
    expect(c.acceptance).toEqual({ text: 'Not accepted yet: Alice is the owner', tone: 'muted', recorded: false, met: false })
  })
  it('say who accepted for whom, and count the live delivery links alone', () => {
    const c = closeCard({
      ...base,
      acceptedAt: '2026-10-31T00:00:00Z',
      acceptedBy: 'Nate',
      acceptedFor: 'Alice',
      links: [link('delivery', 'a'), link('delivery', 'b'), link('delivery', 'c', '2026-10-31T00:00:00Z'), link('run', 'team-0000aaaa'), link('external', 'x')],
      close: { requires: ['acceptance', 'delivery'], met: { acceptance: true, delivery: true }, missing: [] },
    })
    expect(c.items.map((i) => [i.met, i.state])).toEqual([
      [true, 'Accepted by Nate for Alice'],
      [true, 'Delivered: 2 links'],
    ])
    expect(c.acceptance.tone).toBe('success')
    expect(c.ready).toBe(true)
    expect(closeCard({ ...base, links: [link('delivery', 'a')], close: { ...base.close, met: { acceptance: false, delivery: true } } }).items[1]!.state).toBe('Delivered: one link')
  })
  it("say why an acceptance on record is unmet in the server's words: the owner changed, the criteria changed", () => {
    const owner = closeCard({ ...base, owner: 'Bob', acceptedAt: 'x', acceptedBy: 'Nate', acceptedFor: 'Alice', close: { ...base.close, why: 'accepted for Alice; the owner is now Bob' } })
    expect(owner.items[0]).toEqual({ requirement: 'acceptance', words: "the owner's acceptance", met: false, state: 'accepted for Alice; the owner is now Bob' })
    expect(owner.acceptance).toEqual({ text: 'accepted for Alice; the owner is now Bob', tone: 'warning', recorded: true, met: false })
    const criteria = closeCard({ ...base, acceptedAt: 'x', acceptedBy: 'Nate', acceptedFor: 'Alice', close: { ...base.close, why: 'accepted before the criteria changed' } })
    expect(criteria.items[0]!.state).toBe('accepted before the criteria changed')
  })
  it('say that a ticket with no owner has no one to accept it for, and that a closed ticket offers no Close', () => {
    const c = closeCard({ ...base, owner: '', status: 'done' })
    expect(c.acceptBlocked).toBe('It needs an owner to accept it for.')
    expect(c.acceptance.text).toBe('Not accepted yet: the ticket has no owner to accept it')
    expect(c.closed).toBe(true)
    expect(closeCard({ ...base, status: 'cancelled' }).closed).toBe(true)
  })
  it('say the acceptance on record when the policy does not require it, and that nothing is required', () => {
    const c = closeCard({ ...base, acceptedAt: 'x', acceptedBy: 'Nate', acceptedFor: 'Alice', close: { requires: [], met: { acceptance: true, delivery: false }, missing: [] } })
    expect(c.summary).toBe('Closes with nothing required')
    expect(c.items).toEqual([])
    expect(c.acceptance.text).toBe('Accepted by Nate for Alice')
    expect(c.ready).toBe(true)
  })
  it('keep the live delivery links alone', () => {
    expect(deliveryLinks([link('delivery', 'a'), link('external', 'b'), link('delivery', 'c', 'gone')]).map((l) => l.ref)).toEqual(['a'])
  })
})

describe("the close policy's editor", () => {
  it('keeps the policy in its order, each requirement once, nothing it does not know', () => {
    expect(closeRequiresOf(['delivery', 'acceptance', 'delivery'])).toEqual(['acceptance', 'delivery'])
    expect(closeRequiresOf(['verified', 'delivery', 'merged'])).toEqual(['delivery', 'verified'])
    expect(closeRequiresOf([])).toEqual([])
  })
  it('tells two policies apart whatever their order', () => {
    expect(samePolicy(['delivery', 'acceptance'], ['acceptance', 'delivery'])).toBe(true)
    expect(samePolicy(['acceptance'], ['acceptance', 'delivery'])).toBe(false)
    expect(samePolicy([], [])).toBe(true)
  })

  it('says a policy in words for a sentence', () => {
    expect(policyWords(['delivery', 'acceptance'])).toBe("the owner's acceptance and a delivery link")
    expect(policyWords(['acceptance'])).toBe("the owner's acceptance")
    expect(policyWords([])).toBe('nothing')
  })

  it("never saves a policy over another person's unseen: theirs said first, then sent from it on the project's revision", () => {
    // Ticked from acceptance, delivery added; nobody else changed it: sent on the revision the page holds.
    expect(policySave(['acceptance', 'delivery'], ['acceptance'], { closeRequires: ['acceptance'], rev: 4 })).toEqual({ send: ['acceptance', 'delivery'], expectedRev: 4 })
    // Another person's edit of the project's other fields moved the revision only: sent on the new one, the policy being unchanged.
    expect(policySave(['acceptance', 'delivery'], ['acceptance'], { closeRequires: ['acceptance'], rev: 6 })).toEqual({ send: ['acceptance', 'delivery'], expectedRev: 6 })
    // Another person's policy (no requirements) came meanwhile: theirs, nothing sent.
    expect(policySave(['acceptance', 'delivery'], ['acceptance'], { closeRequires: [], rev: 5 })).toEqual({ theirs: [] })
    // Once said, the draft is made from theirs: the next Save replaces it, knowingly.
    expect(policySave(['acceptance', 'delivery'], [], { closeRequires: [], rev: 5 })).toEqual({ send: ['acceptance', 'delivery'], expectedRev: 5 })
    // The draft is the project's policy already: nothing to send, whoever made it so.
    expect(policySave(['delivery'], ['acceptance'], { closeRequires: ['delivery'], rev: 7 })).toBeNull()
  })

  it('keeps a tick made while its Save was on its way once the saved policy comes (review round 2 on PR-11)', () => {
    // Ticked from acceptance, delivery added and sent; delivery unticked while the save is on its way: the saved policy is the base now,
    // and the draft is kept as ticked, so Save is offered for it again.
    expect(policyOnChange(['acceptance'], ['acceptance'], ['acceptance', 'delivery'], ['acceptance', 'delivery'])).toBe('saved')
    // The same draft, a read of the project's other fields coming first: the draft follows the policy it already is, the save still awaited.
    expect(policyOnChange(['acceptance'], ['acceptance'], ['acceptance', 'delivery'], ['acceptance'])).toBe('follow')
    // Nothing ticked since it was sent: saved, the draft the policy.
    expect(policyOnChange(['acceptance', 'delivery'], ['acceptance'], ['acceptance', 'delivery'], ['acceptance', 'delivery'])).toBe('saved')
    // Another person's policy while the save is on its way, something else ticked here: kept on its base, so Save says theirs.
    expect(policyOnChange(['acceptance', 'delivery'], ['acceptance'], ['acceptance', 'delivery'], [])).toBe('keep')
    // No save on its way: an untouched draft follows another person's policy, a ticked one keeps its base.
    expect(policyOnChange(['acceptance'], ['acceptance'], null, [])).toBe('follow')
    expect(policyOnChange(['acceptance', 'delivery'], ['acceptance'], null, [])).toBe('keep')
    expect(policyOnChange(['delivery'], ['acceptance'], null, ['delivery'])).toBe('follow')
  })
})

describe('an Accept that waited its turn', () => {
  it('is sent only for the owner and the criteria shown when it was clicked, and says what changed', () => {
    const seen = { owner: 'Alice', acceptance: 'It works.' }
    expect(acceptChangedWords(seen, { owner: 'Alice', acceptance: 'It works.' })).toBe('')
    expect(acceptChangedWords(seen, { owner: 'Bob', acceptance: 'It works.' })).toBe(
      'The owner changed to Bob before it was recorded, so nothing was accepted: read the ticket as it is now, and accept again if it holds.',
    )
    expect(acceptChangedWords(seen, { owner: 'Alice', acceptance: 'It works, fast.' })).toBe(
      'The acceptance criteria changed before it was recorded, so nothing was accepted: read the ticket as it is now, and accept again if it holds.',
    )
    expect(acceptChangedWords(seen, { owner: '', acceptance: 'It works, fast.' })).toBe(
      'The owner was taken off and the acceptance criteria changed before it was recorded, so nothing was accepted: read the ticket as it is now, and accept again if it holds.',
    )
  })
})

describe("a link's label", () => {
  it('says a GitHub pull request, issue and release in short, and any other URL by its host and path', () => {
    expect(linkLabel('https://github.com/rocksolidlabs/conductor/pull/411')).toBe('rocksolidlabs/conductor#411')
    expect(linkLabel('https://github.com/rocksolidlabs/conductor/issues/12#issuecomment-1')).toBe('rocksolidlabs/conductor#12')
    expect(linkLabel('https://github.com/rocksolidlabs/conductor/releases/tag/v0.7.0-rc.18')).toBe('rocksolidlabs/conductor v0.7.0-rc.18')
    expect(linkLabel('https://github.com/rocksolidlabs/conductor')).toBe('github.com/rocksolidlabs/conductor')
    expect(linkLabel('https://example.com/')).toBe('example.com')
    expect(linkLabel('http://example.com:8080/a/b/')).toBe('example.com:8080/a/b')
    expect(linkLabel('not a url')).toBe('not a url')
    // A tag whose escapes are not UTF-8 is a URL the store keeps: the label shows it as written, never throws.
    expect(linkLabel('https://github.com/o/r/releases/tag/%E0%A4')).toBe('o/r %E0%A4')
    expect(linkLabel('https://github.com/acme/api/releases/tag/%FF')).toBe('acme/api %FF')
    expect(linkLabel('https://github.com/acme/api/releases/tag/v1%2B2')).toBe('acme/api v1+2')
  })
})

describe('the board and the roadmap', () => {
  const tickets = [
    { key: 'A-1', status: 'done', horizon: 'now' },
    { key: 'A-2', status: 'backlog', horizon: '' },
    { key: 'A-3', status: 'in_progress', horizon: 'now' },
    { key: 'A-4', status: 'backlog', horizon: 'later' },
    { key: 'A-5', status: 'triage', horizon: 'someday' },
  ]
  it("make a board's columns, one per status in its order, each with its tickets in the list's order", () => {
    const cols = boardColumns(tickets)
    expect(cols.map((c) => c.status)).toEqual(TICKET_STATUSES)
    expect(cols.map((c) => c.tickets.map((t) => t.key))).toEqual([['A-2', 'A-4'], [], ['A-3'], [], ['A-1'], []])
    expect(cols[2]!.word.label).toBe('In progress')
  })
  it("make a roadmap's columns: Now, Next, Later, then Not planned folded", () => {
    const cols = roadmapColumns(tickets)
    expect(cols.map((c) => [c.word.label, c.folded, c.tickets.map((t) => t.key)])).toEqual([
      ['Now', false, ['A-1', 'A-3']],
      ['Next', false, []],
      ['Later', false, ['A-4']],
      ['Not planned', true, ['A-2']],
    ])
  })
})

describe("the list's keyboard model", () => {
  const keys = ['CND-1', 'CND-2', 'CND-3']
  it('moves J down and K up, wrapping at the ends', () => {
    expect(listFocusStep(keys, 'CND-1', 1)).toBe('CND-2')
    expect(listFocusStep(keys, 'CND-3', 1)).toBe('CND-1')
    expect(listFocusStep(keys, 'CND-1', -1)).toBe('CND-3')
    expect(listFocusStep(keys, 'CND-2', -1)).toBe('CND-1')
  })
  it('starts from the first going down and the last going up, and from a row gone the same; none in an empty list', () => {
    expect(listFocusStep(keys, null, 1)).toBe('CND-1')
    expect(listFocusStep(keys, null, -1)).toBe('CND-3')
    expect(listFocusStep(keys, 'CND-9', 1)).toBe('CND-1')
    expect(listFocusStep([], 'CND-1', 1)).toBeNull()
  })
  it('toggles a row in and out of the selection with x, and nothing for no row', () => {
    const one = listToggle(new Set(), 'CND-2')
    expect([...one]).toEqual(['CND-2'])
    const two = listToggle(one, 'CND-3')
    expect([...two].sort()).toEqual(['CND-2', 'CND-3'])
    expect([...listToggle(two, 'CND-2')]).toEqual(['CND-3'])
    expect([...listToggle(two, null)].sort()).toEqual(['CND-2', 'CND-3'])
    // The selection given is never changed in place.
    expect([...one]).toEqual(['CND-2'])
  })
  it('hands the focus on when its row is gone: to the next still there, else the one before, else nowhere', () => {
    const before = ['CND-1', 'CND-2', 'CND-3', 'CND-4']
    expect(listRefocus(before, before, 'CND-2')).toBe('CND-2')
    expect(listRefocus(before, ['CND-1', 'CND-3', 'CND-4'], 'CND-2')).toBe('CND-3')
    expect(listRefocus(before, ['CND-1', 'CND-4'], 'CND-2')).toBe('CND-4')
    expect(listRefocus(before, ['CND-1', 'CND-2', 'CND-3'], 'CND-4')).toBe('CND-3')
    expect(listRefocus(before, ['CND-9'], 'CND-2')).toBeNull()
    expect(listRefocus(before, before, null)).toBeNull()
    // A row that moved stays focused where it went.
    expect(listRefocus(before, ['CND-2', 'CND-1'], 'CND-2')).toBe('CND-2')
  })
  it('keeps the selection to the rows still there', () => {
    expect([...listKeepSelected(new Set(['CND-1', 'CND-3']), ['CND-3', 'CND-4'])]).toEqual(['CND-3'])
  })
})

describe("a ticket's history", () => {
  let seq = 0
  const row = (p: Partial<DispatchChange>): DispatchChange => ({ seq: ++seq, entity: 'ticket', entityId: 't1', ticketId: 't1', op: 'set', value: '', at: '2026-10-30T10:00:00Z', actor: 'admin', by: 'Nate', opId: 'op1', n: 0, ...p })
  const names = { labels: new Map([['l1', 'api']]), milestones: new Map([['m1', 'v1.0']]), agents: new Map([['claude', 'Claude Code']]) }
  it('says each field set, added or removed in words', () => {
    expect(changeWords(row({ op: 'create', value: '{}' }))).toBe('made the ticket')
    expect(changeWords(row({ field: 'title', value: 'Fix the login' }))).toBe('set the title to "Fix the login"')
    expect(changeWords(row({ field: 'description', value: 'x' }))).toBe('wrote the description')
    expect(changeWords(row({ field: 'description', value: 'y', prior: 'x' }))).toBe('edited the description')
    expect(changeWords(row({ field: 'acceptance', value: '', prior: 'x' }))).toBe('cleared the acceptance criteria')
    expect(changeWords(row({ field: 'status', value: 'in_progress', prior: 'backlog' }))).toBe('moved it from Backlog to In progress')
    expect(changeWords(row({ field: 'priority', value: '3', prior: '0' }))).toBe('set the priority to High')
    expect(changeWords(row({ field: 'horizon', value: '' }))).toBe('set the horizon to Not planned')
    expect(changeWords(row({ field: 'owner', value: 'Alice' }))).toBe('made Alice the owner')
    expect(changeWords(row({ field: 'owner', value: '', prior: 'Alice' }))).toBe('cleared the owner')
    expect(changeWords(row({ op: 'add', field: 'assignees', value: 'person:Bob' }), names)).toBe('assigned Bob')
    expect(changeWords(row({ op: 'remove', field: 'assignees', value: 'agent:claude' }), names)).toBe('unassigned Claude Code')
    expect(changeWords(row({ op: 'add', field: 'labels', value: 'l1' }), names)).toBe('added the label api')
    expect(changeWords(row({ op: 'remove', field: 'labels', value: 'l9' }), names)).toBe('removed the label a label since deleted')
    expect(changeWords(row({ field: 'milestone_id', value: 'm1' }), names)).toBe('set the milestone to v1.0')
    expect(changeWords(row({ op: 'add', field: 'links', value: '{"kind":"delivery"}' }))).toBe('added a delivery link')
    expect(changeWords(row({ op: 'add', field: 'links', value: `{"kind":"delivery","candidate":"team-0000aaaa/build/2@${'a'.repeat(40)}"}` }))).toBe('added a delivery link naming build#2')
    expect(changeWords(row({ field: 'deleted_at', value: '2026-10-30T10:00:00Z' }))).toBe('deleted it')
    expect(changeWords(row({ field: 'accepted_for', value: 'Alice' }))).toBe('accepted it for Alice')
    expect(changeWords(row({ op: 'reconcile', field: 'parent_id', value: '' }))).toBe("cleared the parent (a merge's repair)")
    expect(changeWords(row({ entity: 'comment', entityId: 'c1', op: 'create', value: '{}' }))).toBe('commented')
  })
  it('makes one entry per operation, oldest first, leaving out the rows that say nothing more than their operation', () => {
    const changes = [
      row({ op: 'create', value: '{}', opId: 'a', n: 0 }),
      row({ field: 'title', value: 'Fix the login', opId: 'a', n: 1 }),
      row({ field: 'priority', value: '4', prior: '0', opId: 'a', n: 2 }),
      row({ field: 'status', value: 'done', prior: 'backlog', opId: 'b', by: 'Priya' }),
      row({ field: 'closed_at', value: '2026-10-30T11:00:00Z', opId: 'b', by: 'Priya', n: 1 }),
      row({ entity: 'comment', entityId: 'c1', op: 'create', value: '{}', opId: 'c', by: '', actor: 'agent:lead' }),
      row({ entity: 'comment', entityId: 'c1', field: 'body', value: 'hi', opId: 'c', by: '', actor: 'agent:lead', n: 1 }),
      row({ entity: 'comment', entityId: 'c1', field: 'edited_at', value: '2026-10-30T12:00:00Z', opId: 'd', n: 1 }),
    ]
    const entries = historyEntries(changes)
    expect(entries.map((e) => [e.who, e.lines])).toEqual([
      ['Nate', ['made the ticket', 'set the title to "Fix the login"', 'set the priority to Urgent']],
      ['Priya', ['moved it from Backlog to Done']],
      ['agent:lead', ['commented']],
    ])
    expect(entries[1]!.seq).toBe(changes[4]!.seq)
  })
  it('says an acceptance once, one made again for the same owner among them, and says one taken back', () => {
    const at = (h: number) => `2026-10-30T1${h}:00:00.000000000Z`
    const criteria = (c: string) => c.repeat(64)
    // The rows AcceptTicket and UnacceptTicket record: one per field that changes, in the order accepted_at, accepted_by, accepted_for,
    // accepted_criteria, accepted_note.
    const changes = [
      // Accepted for Alice: every field from nothing, the note none.
      row({ field: 'accepted_at', value: at(0), opId: 'a1' }),
      row({ field: 'accepted_by', value: 'Nate', opId: 'a1', n: 1 }),
      row({ field: 'accepted_for', value: 'Alice', opId: 'a1', n: 2 }),
      row({ field: 'accepted_criteria', value: criteria('1'), opId: 'a1', n: 3 }),
      // Accepted again once the criteria changed, for the same owner, by the same person, with no note: its time and the criteria alone.
      row({ field: 'accepted_at', value: at(1), prior: at(0), opId: 'a2' }),
      row({ field: 'accepted_criteria', value: criteria('2'), prior: criteria('1'), opId: 'a2', n: 1 }),
      // Accepted again with nothing else changed: its time alone.
      row({ field: 'accepted_at', value: at(2), prior: at(1), opId: 'a3' }),
      // Accepted again with a note: its time and the note.
      row({ field: 'accepted_at', value: at(3), prior: at(2), opId: 'a4' }),
      row({ field: 'accepted_note', value: 'Checked on staging', opId: 'a4', n: 1 }),
      // Accepted again for Bob, the owner since: the owner's row says it, and the time's says nothing more.
      row({ field: 'accepted_at', value: at(4), prior: at(3), opId: 'a5' }),
      row({ field: 'accepted_for', value: 'Bob', prior: 'Alice', opId: 'a5', n: 1 }),
      // Taken back: every field that held something, cleared.
      row({ field: 'accepted_at', value: '', prior: at(4), opId: 'a6' }),
      row({ field: 'accepted_by', value: '', prior: 'Nate', opId: 'a6', n: 1 }),
      row({ field: 'accepted_for', value: '', prior: 'Bob', opId: 'a6', n: 2 }),
      row({ field: 'accepted_criteria', value: '', prior: criteria('2'), opId: 'a6', n: 3 }),
      row({ field: 'accepted_note', value: '', prior: 'Checked on staging', opId: 'a6', n: 4 }),
      // Accepted after it was taken back: from nothing again, for its owner.
      row({ field: 'accepted_at', value: at(5), opId: 'a7' }),
      row({ field: 'accepted_by', value: 'Nate', opId: 'a7', n: 1 }),
      row({ field: 'accepted_for', value: 'Bob', opId: 'a7', n: 2 }),
      row({ field: 'accepted_criteria', value: criteria('2'), opId: 'a7', n: 3 }),
    ]
    expect(historyEntries(changes).map((e) => e.lines)).toEqual([
      ['accepted it for Alice'],
      ['accepted it again'],
      ['accepted it again'],
      ['accepted it again', 'noted "Checked on staging"'],
      ['accepted it for Bob'],
      ['took the acceptance back'],
      ['accepted it for Bob'],
    ])
  })

  // DS2: a ticket's plan revisions and launches are rows of its history; each says what it did, never "made the ticket".
  it("says a plan revision's proposal, approval, supersession and withdrawal, and a launch with how its run's start came out", () => {
    const plans = { plans: new Map([['p1', 1], ['p2', 2]]) }
    const plan = (p: Partial<DispatchChange>) => row({ entity: 'plan', entityId: 'p2', ...p })
    expect(changeWords(plan({ op: 'create', value: '{}' }), plans)).toBe('proposed plan revision 2')
    expect(changeWords(plan({ op: 'create', value: '{}', entityId: 'p9' }), plans)).toBe('proposed a plan revision')
    expect(changeWords(plan({ field: 'state', value: 'approved', prior: 'proposed' }), plans)).toBe('approved plan revision 2')
    // An approval made again writes no state row (the state is approved already): its time's row, with the time before, says it.
    expect(changeWords(plan({ field: 'approved_at', value: '2026-11-01T11:00:00Z', prior: '2026-11-01T10:00:00Z' }), plans)).toBe('approved plan revision 2 again')
    expect(changeWords(plan({ field: 'approved_at', value: '2026-11-01T10:00:00Z' }), plans)).toBe('approved plan revision 2')
    expect(changeWords(plan({ field: 'state', value: 'superseded', prior: 'approved', entityId: 'p1' }), plans)).toBe('superseded plan revision 1')
    expect(changeWords(plan({ field: 'state', value: 'withdrawn', prior: 'approved' }), plans)).toBe('withdrew plan revision 2')
    expect(changeWords(plan({ field: 'approved_note', value: 'the smaller one' }), plans)).toBe('noted "the smaller one"')
    for (const field of ['approved_by', 'approved_rev', 'approved_intent', 'withdrawn_at', 'withdrawn_by']) expect(changeWords(plan({ field, value: 'x' }), plans)).toBe('')
    const launch = (p: Partial<DispatchChange>) => row({ entity: 'launch', entityId: 'l1', ...p })
    expect(changeWords(launch({ op: 'create', value: JSON.stringify({ runId: 'build-0123abcd', planId: 'p2' }) }), plans)).toBe('launched the run build-0123abcd from plan revision 2')
    expect(changeWords(launch({ op: 'create', value: 'not json' }), plans)).toBe('launched the run from a plan revision')
    expect(changeWords(launch({ field: 'state', value: 'started', actor: 'server' }), plans)).toBe('its run started')
    expect(changeWords(launch({ field: 'state', value: 'failed', actor: 'server' }), plans)).toBe('')
    expect(changeWords(launch({ field: 'why', value: 'the agent is not installed', actor: 'server' }), plans)).toBe('the launch failed: the agent is not installed')
    for (const field of ['agents', 'started_at']) expect(changeWords(launch({ field, value: 'x' }), plans)).toBe('')
  })

  it("makes an approval one entry: the revision approved, the one it superseded, its note", () => {
    const plans = { plans: new Map([['p1', 1], ['p2', 2]]) }
    const at = { opId: 'appr', actor: 'admin', by: 'Nate' }
    const entries = historyEntries(
      [
        row({ ...at, entity: 'plan', entityId: 'p2', field: 'approved_at', value: '2026-11-01T10:00:00Z' }),
        row({ ...at, entity: 'plan', entityId: 'p2', field: 'approved_note', value: 'go' }),
        row({ ...at, entity: 'plan', entityId: 'p2', field: 'state', value: 'approved', prior: 'proposed' }),
        row({ ...at, entity: 'plan', entityId: 'p1', field: 'state', value: 'superseded', prior: 'approved' }),
      ],
      plans,
    )
    expect(entries.map((e) => e.lines)).toEqual([['noted "go"', 'approved plan revision 2', 'superseded plan revision 1']])
  })

  it('says an approval made again from the rows ApprovePlan writes for it: its time, the ticket revision and intent it covers, no state', () => {
    const plans = { plans: new Map([['p2', 2]]) }
    const op = (opId: string) => ({ opId, actor: 'admin', by: 'Nate', entity: 'plan' as const, entityId: 'p2' })
    // The order setScalars writes them in: approved_at, approved_by, approved_note, approved_rev, approved_intent, state; each only when it changed.
    const entries = historyEntries(
      [
        row({ ...op('a1'), field: 'approved_at', value: '2026-11-01T10:00:00Z' }),
        row({ ...op('a1'), field: 'approved_by', value: 'Nate', n: 1 }),
        row({ ...op('a1'), field: 'approved_rev', value: '3', n: 2 }),
        row({ ...op('a1'), field: 'approved_intent', value: 'aaaa', n: 3 }),
        row({ ...op('a1'), field: 'state', value: 'approved', prior: 'proposed', n: 4 }),
        // The title edited, then approved again: no state row, nor approved_by's (the same person).
        row({ ...op('a2'), field: 'approved_at', value: '2026-11-01T11:00:00Z', prior: '2026-11-01T10:00:00Z' }),
        row({ ...op('a2'), field: 'approved_rev', value: '4', prior: '3', n: 1 }),
        row({ ...op('a2'), field: 'approved_intent', value: 'bbbb', prior: 'aaaa', n: 2 }),
        // Approved again with a note.
        row({ ...op('a3'), field: 'approved_at', value: '2026-11-01T12:00:00Z', prior: '2026-11-01T11:00:00Z' }),
        row({ ...op('a3'), field: 'approved_note', value: 'still small', n: 1 }),
        row({ ...op('a3'), field: 'approved_rev', value: '5', prior: '4', n: 2 }),
      ],
      plans,
    )
    expect(entries.map((e) => e.lines)).toEqual([['approved plan revision 2'], ['approved plan revision 2 again'], ['approved plan revision 2 again', 'noted "still small"']])
  })
})

// PR-9: the board's and the roadmap's moves, the list's letters and their pickers, the rows an edit's answer patches, the roadmap's
// milestones.

/** A ticket as a list shows it, at revision 1 and in the backlog unless told otherwise. */
function row(key: string, p: Partial<TicketSummary> = {}): TicketSummary {
  return { id: key.toLowerCase(), projectId: 'p1', number: Number(key.split('-')[1]), key, title: `Ticket ${key}`, status: 'backlog', priority: 0, horizon: '', owner: '', assignees: [], labels: [], milestoneId: '', parentId: '', blocked: false, createdAt: '2026-10-30T10:00:00Z', updatedAt: '2026-10-30T10:00:00Z', rev: 1, ...p }
}

describe("the board's and the roadmap's moves", () => {
  it('move a card a column right or left on the board, never past either end, and never from a status the board lacks', () => {
    expect(boardStep('backlog', 1)).toBe('ready')
    expect(boardStep('in_progress', 1)).toBe('in_review')
    expect(boardStep('in_review', -1)).toBe('in_progress')
    expect(boardStep('backlog', -1)).toBeNull()
    expect(boardStep('cancelled', 1)).toBeNull()
    expect(boardStep('triage', 1)).toBeNull()
  })
  it('move a card through Now, Next, Later and Not planned on the roadmap, never past either end', () => {
    expect(roadmapStep('now', 1)).toBe('next')
    expect(roadmapStep('next', 1)).toBe('later')
    expect(roadmapStep('later', 1)).toBe('')
    expect(roadmapStep('next', -1)).toBe('now')
    expect(roadmapStep('', -1)).toBe('later')
    expect(roadmapStep('now', -1)).toBeNull()
    expect(roadmapStep('', 1)).toBeNull()
    expect(roadmapStep('someday', 1)).toBeNull()
  })
  it("show a moved card in its new column until the store answers, and every other as it is; with no move the rows as they are", () => {
    const rows = [row('A-1'), row('A-2', { horizon: 'now' })]
    const moved = withMoves(rows, new Map([['A-1', { status: 'in_review' as const }], ['A-2', { horizon: 'next' as const }]]))
    expect(moved.map((t) => [t.key, t.status, t.horizon])).toEqual([
      ['A-1', 'in_review', ''],
      ['A-2', 'backlog', 'next'],
    ])
    // Its entry gone (refused), the card is back where the store holds it.
    expect(withMoves(rows, new Map()).map((t) => t.status)).toEqual(['backlog', 'backlog'])
    expect(rows[0]!.status).toBe('backlog')
  })
  it('make the edit a move asks for, and none when the card is in that column already', () => {
    expect(moveEdit(row('A-1'), { status: 'in_review' })).toEqual({ set: { status: 'in_review' } })
    expect(moveEdit(row('A-1'), { status: 'backlog' })).toBeNull()
    expect(moveEdit(row('A-1', { horizon: 'now' }), { horizon: 'next' })).toEqual({ set: { horizon: 'next' } })
    expect(moveEdit(row('A-1', { horizon: 'now' }), { horizon: '' })).toEqual({ set: { horizon: '' } })
    expect(moveEdit(row('A-1'), { horizon: '' })).toBeNull()
    expect(moveEdit(row('A-1'), {})).toBeNull()
  })
})

describe("the rows an edit's answer patches", () => {
  it("make a list's summary of a ticket in full, its closed and deleted times only when set", () => {
    const full = { ...row('A-3', { status: 'done', rev: 4 }), description: 'long', acceptance: '', source: 'native', createdBy: 'Nate', closedAt: '2026-10-30T11:00:00Z', children: { tickets: [], total: 0 }, relations: [], relationsTotal: 0, links: [], attachments: [], comments: [], close: { requires: [], met: { acceptance: false, delivery: false }, missing: [] } } as unknown as DispatchTicket
    const s = ticketSummaryOf(full)
    expect(s).toEqual({ ...row('A-3', { status: 'done', rev: 4 }), closedAt: '2026-10-30T11:00:00Z' })
    expect(s).not.toHaveProperty('description')
    expect(s).not.toHaveProperty('deletedAt')
  })
  it('replace a row by an answer at its revision or later, never by an older one', () => {
    const rows = [row('A-1', { rev: 3 }), row('A-2', { rev: 2 })]
    expect(patchRows(rows, row('A-1', { status: 'ready', rev: 4 }))[0]!.status).toBe('ready')
    expect(patchRows(rows, row('A-1', { status: 'ready', rev: 2 }))[0]!.status).toBe('backlog')
    expect(patchRows(rows, row('A-9', { rev: 9 }))).toEqual(rows)
  })
  it("keep an answer newer than a list read served after it, until a read has the same revision or a later one", () => {
    const known = new Map([['A-1', row('A-1', { status: 'in_review', rev: 5 })]])
    expect(newestRows([row('A-1', { rev: 4 }), row('A-2')], known).map((t) => [t.key, t.status, t.rev])).toEqual([
      ['A-1', 'in_review', 5],
      ['A-2', 'backlog', 1],
    ])
    expect(newestRows([row('A-1', { status: 'done', rev: 6 })], known)[0]!.status).toBe('done')
    expect(newestRows([row('A-1', { status: 'ready', rev: 5 })], known)[0]!.status).toBe('ready')
  })
})

describe("the list's letters and their pickers (Owner decisions, 10)", () => {
  it('give each letter its field: S status, P priority, A assignees, L labels, M milestone, O owner', () => {
    expect(PICK_KEYS).toEqual({ s: 'status', p: 'priority', a: 'assignees', l: 'labels', m: 'milestone', o: 'owner' })
  })
  it('tick what every ticket picked over holds: a scalar shared by all, a set\'s members held by all', () => {
    const a = row('A-1', { status: 'ready', priority: 2, assignees: ['person:Ann', 'agent:claude'], labels: ['l1', 'l2'], milestoneId: 'm1', owner: 'Ann' })
    const b = row('A-2', { status: 'ready', priority: 3, assignees: ['person:Ann'], labels: ['l2'], milestoneId: '', owner: 'Ann' })
    expect(pickHeld('status', [a, b])).toEqual(['ready'])
    expect(pickHeld('priority', [a, b])).toEqual([])
    expect(pickHeld('owner', [a, b])).toEqual(['Ann'])
    expect(pickHeld('milestone', [a, b])).toEqual([])
    expect(pickHeld('milestone', [b])).toEqual([''])
    expect(pickHeld('assignees', [a, b])).toEqual(['person:Ann'])
    expect(pickHeld('labels', [a, b])).toEqual(['l2'])
    expect(pickHeld('labels', [])).toEqual([])
  })
  it("add a set's member some ticket lacks, and take away one every ticket holds", () => {
    const a = row('A-1', { labels: ['l1'] })
    const b = row('A-2', { labels: [] })
    expect(pickAdds('labels', [a, b], 'l1')).toBe(true)
    expect(pickAdds('labels', [a], 'l1')).toBe(false)
    expect(pickAdds('assignees', [a], 'person:Ann')).toBe(true)
  })
  it("make each ticket's edit for a pick, none for one holding it already", () => {
    const t = row('A-1', { status: 'ready', priority: 1, labels: ['l1'], assignees: ['person:Ann'], milestoneId: 'm1', owner: 'Ann' })
    expect(pickEdit('status', t, 'in_review')).toEqual({ set: { status: 'in_review' } })
    expect(pickEdit('status', t, 'ready')).toBeNull()
    expect(pickEdit('priority', t, 4)).toEqual({ set: { priority: 4 } })
    expect(pickEdit('priority', t, 1)).toBeNull()
    expect(pickEdit('milestone', t, '')).toEqual({ set: { milestone: '' } })
    expect(pickEdit('milestone', t, 'm1')).toBeNull()
    expect(pickEdit('owner', t, 'Bob')).toEqual({ set: { owner: 'Bob' } })
    expect(pickEdit('owner', t, '')).toEqual({ set: { owner: '' } })
    expect(pickEdit('owner', t, 'Ann')).toBeNull()
    expect(pickEdit('labels', t, 'l2', true)).toEqual({ add: { labels: ['l2'] } })
    expect(pickEdit('labels', t, 'l1', true)).toBeNull()
    expect(pickEdit('labels', t, 'l1', false)).toEqual({ remove: { labels: ['l1'] } })
    expect(pickEdit('labels', t, 'l2', false)).toBeNull()
    expect(pickEdit('assignees', t, 'agent:codex', true)).toEqual({ add: { assignees: ['agent:codex'] } })
    expect(pickEdit('assignees', t, 'person:Ann', false)).toEqual({ remove: { assignees: ['person:Ann'] } })
  })
  it('say the tickets a pick acts on: the key of one, the count of several', () => {
    expect(targetWords(['A-1'])).toBe('A-1')
    expect(targetWords(['A-1', 'A-2', 'A-3'])).toBe('3 tickets')
  })
})

describe("the roadmap's milestones", () => {
  const m = (id: string, p: { target?: string; state?: string; name?: string } = {}) => ({ id, name: p.name ?? id, target: p.target ?? '', state: p.state ?? 'open' })
  it('come open first by their target, the soonest first and one with no target after, then the closed', () => {
    const order = milestoneProgress([m('closed', { state: 'closed', target: '2026-01-01' }), m('none'), m('late', { target: '2026-12-01' }), m('soon', { target: '2026-11-01' })], [])
    expect(order.map((p) => p.milestone.id)).toEqual(['soon', 'late', 'none', 'closed'])
  })
  it('count the tickets shown in each and those done, the cancelled in neither', () => {
    const tickets = [row('A-1', { milestoneId: 'm1', status: 'done' }), row('A-2', { milestoneId: 'm1', status: 'in_progress' }), row('A-3', { milestoneId: 'm1', status: 'cancelled' }), row('A-4', { milestoneId: 'm2', status: 'done' }), row('A-5')]
    expect(milestoneProgress([m('m1'), m('m2'), m('m3')], tickets).map((p) => [p.milestone.id, p.done, p.total])).toEqual([
      ['m1', 1, 2],
      ['m2', 1, 1],
      ['m3', 0, 0],
    ])
  })
})

describe("the palette's Tickets cache", () => {
  it('reads every page by its cursor: 450 projects over three pages of 200, the last ones among them', async () => {
    const all = Array.from({ length: 450 }, (_, i) => `P${i}`)
    const asked: string[] = []
    const read = async (cursor: string) => {
      asked.push(cursor)
      const from = cursor ? Number(cursor) : 0
      const items = all.slice(from, from + 200)
      return { items, next: from + 200 < all.length ? String(from + 200) : undefined }
    }
    const got = await allPages(read)
    expect(got).toHaveLength(450)
    expect(got.at(-1)).toBe('P449')
    expect(asked).toEqual(['', '200', '400'])
  })

  it('stops at the most asked for, reading no page past it', async () => {
    let reads = 0
    const got = await allPages(async (cursor) => {
      reads++
      const from = cursor ? Number(cursor) : 0
      return { items: Array.from({ length: 200 }, (_, i) => from + i), next: String(from + 200) }
    }, 500)
    expect(got).toHaveLength(500)
    expect(reads).toBe(3)
  })

  it('lists a ticket not done, cancelled or deleted', () => {
    expect(paletteListed(row('A-1', { status: 'in_progress' }))).toBe(true)
    expect(paletteListed(row('A-1', { status: 'done' }))).toBe(false)
    expect(paletteListed(row('A-1', { status: 'cancelled' }))).toBe(false)
    expect(paletteListed(row('A-1', { deletedAt: '2026-10-30T11:00:00Z' }))).toBe(false)
  })

  it("keeps an edit's ticket over a read served before its commit and answered after it: the older row never comes back", () => {
    const patched = new Map([['A-1', row('A-1', { status: 'ready', rev: 2 })]])
    const { rows, keep } = withPatched([row('A-1', { rev: 1 }), row('A-2')], patched)
    expect(rows.map((t) => [t.key, t.status, t.rev])).toEqual([
      ['A-1', 'ready', 2],
      ['A-2', 'backlog', 1],
    ])
    // Still needed: a later read may be older still.
    expect([...keep.keys()]).toEqual(['A-1'])
  })

  it("drops a row an edit's answer closed, when the read is older than that answer", () => {
    const patched = new Map([['A-1', row('A-1', { status: 'done', rev: 3 })]])
    expect(withPatched([row('A-1', { rev: 2 }), row('A-2')], patched).rows.map((t) => t.key)).toEqual(['A-2'])
  })

  it('takes a read that carries the ticket at the revision of the edit or later, and forgets the edit then', () => {
    const patched = new Map([['A-1', row('A-1', { status: 'ready', rev: 2 })]])
    const { rows, keep } = withPatched([row('A-1', { status: 'in_review', rev: 3 })], patched)
    expect(rows[0]!.status).toBe('in_review')
    expect(keep.size).toBe(0)
  })
})

describe('a name typed into the owner and assignee pickers', () => {
  it('offers the person when only an agent has the name typed: agent:claude never stands for the person Claude', () => {
    expect(typedPick('assignees', 'Claude', ['person:Nate', 'agent:claude'])).toBe('person:Claude')
  })

  it('offers nothing new when a person has the name, whatever its case', () => {
    expect(typedPick('assignees', 'nate', ['person:Nate', 'agent:claude'])).toBeNull()
    expect(typedPick('owner', 'NATE', ['', 'Nate'])).toBeNull()
  })

  it('offers the owner typed, trimmed and at most 60 characters, and nothing for a blank', () => {
    expect(typedPick('owner', '  Priya ', ['', 'Nate'])).toBe('Priya')
    expect(typedPick('owner', 'x'.repeat(70), [])).toBe('x'.repeat(60))
    expect(typedPick('assignees', '   ', [])).toBeNull()
  })
})

describe("a delivery's candidate and its verification (DS2, PR-12)", () => {
  const commit = 'c'.repeat(40)
  const ref = `team-0000aaaa/build/2@${commit}`
  const rc = (p: Partial<LinkVerified['receipts'][number]> = {}) => ({ phase: 'check' as const, id: 'test', k: 1, where: 'native', exit: 0, stable: true, ...p })
  const verified = (p: Partial<LinkVerified> = {}): LinkVerified => ({ at: '2026-11-03T10:00:00Z', passed: true, attempt: 2, receipts: [rc(), rc({ id: 'lint' })], ...p })

  it('reads and writes a candidate as the store keeps it, and names it the store\'s way', () => {
    expect(parseCandidate(ref)).toEqual({ run: 'team-0000aaaa', task: 'build', attempt: 2, commit })
    expect(candidateRef({ run: 'team-0000aaaa', task: 'build', attempt: 2, commit })).toBe(ref)
    expect(candidateName(ref)).toBe('build#2')
    expect(parseCandidate(`team-0000aaaa/build/2@${'c'.repeat(64)}`)?.commit).toHaveLength(64)
    for (const bad of ['', 'team-0000aaaa/build/2', `team-0000aaaa/build/0@${commit}`, `build/2@${commit}`, `team-0000aaaa/build/2@${commit.slice(1)}`, `team-0000aaaa/build/2@${'C'.repeat(40)}`]) {
      expect(parseCandidate(bad), bad).toBeNull()
    }
    expect(candidateName('not a candidate')).toBe('not a candidate')
  })

  it("says a delivery verified from the receipt it recorded, or how it fell short, in the store's words", () => {
    expect(verificationWords({ candidate: ref, verified: verified() })).toMatchObject({ state: 'verified', text: 'Verified: build#2 passed 2 checks', tone: 'success', withdrawn: '' })
    expect(verificationWords({ candidate: ref, verified: verified({ receipts: [rc()] }) }).text).toBe('Verified: build#2 passed its check')
    const failed = verified({ passed: false, receipts: [rc(), rc({ id: 'never', exit: 1 })] })
    expect(verificationWords({ candidate: ref, verified: failed })).toMatchObject({ state: 'unverified', text: 'Not verified: build#2 failed the check never (exit 1)', tone: 'warning' })
    expect(verifiedShortfall({ receipts: [] })).toBe('ran no check')
    expect(verifiedShortfall({ receipts: [rc({ phase: 'verify', id: 'lint', exit: undefined })] })).toBe('did not finish the verify step lint')
    expect(verifiedShortfall({ receipts: [rc({ stable: false })] })).toBe('passed the check test but not stably')
    expect(verifiedShortfall({ receipts: [rc()] })).toBe('passed its checks, but its attempt did not succeed')
    expect(verificationWords({ candidate: ref, verified: verified({ passed: false, receipts: [] }) }).text).toBe('Not verified: build#2 ran no check')
    // A delivery naming no candidate says nothing of verification; one whose receipt cannot be read says so.
    expect(verificationWords({}).state).toBe('none')
    expect(verificationWords({ candidate: ref }).text).toBe("Not verified: build#2's verification receipt cannot be read")
    // A withdrawal later leaves the receipt as it was, and the row says it.
    expect(verificationWords({ candidate: ref, verified: verified() }, true)).toMatchObject({ state: 'verified', withdrawn: "build#2's result was withdrawn after this was recorded; the receipt stays true of its commit." })
  })

  it("puts verified on the close card: the passing delivery's candidate named, else the server's words why not", () => {
    const delivery = (id: string, p: Partial<TicketLink> = {}): TicketLink => ({ id, kind: 'delivery', ref: id, url: `https://h/${id}`, note: '', createdAt: '', createdBy: '', ...p })
    const base = { owner: 'Alice', status: 'in_review' as const, acceptedAt: '', links: [] as TicketLink[] }
    const met = closeCard({
      ...base,
      links: [delivery('a', { candidate: ref, verified: verified() })],
      close: { requires: ['delivery', 'verified'], met: { acceptance: false, delivery: true, verified: true }, missing: [] },
    })
    expect(met.summary).toBe('To close: a delivery link · a verified delivery')
    expect(met.items.map((i) => [i.requirement, i.met, i.state])).toEqual([
      ['delivery', true, 'Delivered: one link'],
      ['verified', true, 'Verified: build#2 passed 2 checks'],
    ])
    const unmet = closeCard({
      ...base,
      links: [delivery('a', { candidate: ref, verified: verified({ passed: false, receipts: [rc({ id: 'never', exit: 1 })] }) })],
      close: { requires: ['verified'], met: { acceptance: false, delivery: true, verified: false }, missing: ['verified'], verifiedWhy: 'the delivered candidate build#2 failed the check never (exit 1)' },
    })
    expect(unmet.items[0]).toMatchObject({ requirement: 'verified', met: false, state: 'The delivered candidate build#2 failed the check never (exit 1)' })
    // A server before PR-9 answers no verified at all: unmet, said as no candidate.
    const old = closeCard({ ...base, close: { requires: ['verified'], met: { acceptance: false, delivery: false }, missing: ['verified'] } })
    expect(old.items[0]).toMatchObject({ met: false, state: 'No delivery names a candidate' })
    expect(closeRefusalWords(['acceptance', 'verified'])).toBe("It needs the owner's acceptance and a verified delivery to be done.")
    expect(policyWords(['verified', 'acceptance'])).toBe("the owner's acceptance and a verified delivery")
  })

  const receipt = (p: Partial<CandidateReceipt> = {}): CandidateReceipt => ({ phase: 'check', id: 'test', k: 1, where: 'native', state: 'finished', exit: 0, timedOut: false, killed: false, stable: true, startedAt: '', logBytes: 0, logDropped: false, ...p })
  const cand = (attempt: number, p: Partial<RunCandidate> = {}): RunCandidate => ({ task: 'build', kind: 'work', attempt, commit: String(attempt).repeat(40), state: 'succeeded', standing: false, withdrawn: false, receipts: [receipt()], ...p })
  const tasks = (...ts: TaskCandidates[]) => ts
  const work = (task: string, ...cs: RunCandidate[]): TaskCandidates => ({ task, kind: 'work', candidates: cs.map((c) => ({ ...c, task })), more: 0 })

  it("offers each run's standing work results, with their receipts in short, never a review's, a withdrawn one or one twice", () => {
    const runs = [
      { run: 'team-0000bbbb', label: 'CND-12 again', tasks: tasks(work('build', cand(3, { standing: true, receipts: [receipt(), receipt({ id: 'lint' })] }), cand(2), cand(1)), { task: 'review', kind: 'review', candidates: [cand(1, { kind: 'review', task: 'review', standing: true, verdict: 'approved' })], more: 0 }) },
      { run: 'team-0000aaaa', label: '', tasks: tasks(work('build', cand(3, { standing: true, state: 'failed', acceptedAnyway: { actor: 'admin', at: 'x', command: 'c1' }, receipts: [receipt({ id: 'never', exit: 1 })] })), work('docs', cand(1, { standing: true, withdrawn: true }))) },
      { run: 'team-0000bbbb', label: 'CND-12 again', tasks: tasks(work('build', cand(3, { standing: true }))) },
    ]
    const opts = candidateOptions(runs)
    expect(opts.map((o) => o.value)).toEqual([`team-0000bbbb/build/3@${'3'.repeat(40)}`, `team-0000aaaa/build/3@${'3'.repeat(40)}`])
    expect(opts.map((o) => [o.label, o.description])).toEqual([
      ['build#3 · 3333333', 'CND-12 again: 2 checks passed'],
      ['build#3 · 3333333', 'team-0000aaaa: 1 of 1 failed: check never (exit 1), accepted anyway'],
    ])
    expect(candidateSummary(cand(1, { receipts: [] }))).toBe('no check: it cannot be verified')
    expect(candidateSummary(cand(1, { receipts: [receipt()] }))).toBe('its check passed')
    expect(candidateSummary(cand(1, { receipts: [receipt(), receipt({ id: 'tidy', stable: false })] }))).toBe('1 of 2 failed: check tidy (changed the tree)')
  })

  it("sums a candidate's receipts by the server's rule for a verified delivery: a leftover child killed passes, a signal fails, the attempt must have succeeded", () => {
    // A check whose leftover child was killed after its process exited 0: passed, as the server records it verified.
    expect(candidateSummary(cand(1, { receipts: [receipt({ killed: true })] }))).toBe('its check passed')
    // A signal fails the receipt, whatever the exit says.
    expect(candidateSummary(cand(1, { receipts: [receipt(), receipt({ id: 'slow', exit: undefined, signal: 'killed' })] }))).toBe('1 of 2 failed: check slow (signal killed)')
    expect(candidateSummary(cand(1, { receipts: [receipt({ signal: 'terminated' })] }))).toBe('1 of 1 failed: check test (signal terminated)')
    expect(candidateSummary(cand(1, { receipts: [receipt({ id: 'slow', exit: undefined, timedOut: true })] }))).toBe('1 of 1 failed: check slow (timed out)')
    expect(candidateSummary(cand(1, { receipts: [receipt({ state: 'interrupted', exit: undefined })] }))).toBe('1 of 1 failed: check test (interrupted)')
    // Every check passed, but the attempt did not succeed: the server records it not verified.
    expect(candidateSummary(cand(1, { state: 'failed' }))).toBe('its check passed, but its attempt failed: it cannot be verified')
    expect(candidateSummary(cand(1, { state: 'uncertain', receipts: [receipt(), receipt({ id: 'lint' })] }))).toBe('2 checks passed, but its attempt ended uncertain: it cannot be verified')
    expect(candidateSummary(cand(1, { state: 'cancelled', acceptedAnyway: { actor: 'admin', at: 'x', command: 'c1' } }))).toBe(
      'its check passed, but its attempt was cancelled: it cannot be verified, accepted anyway',
    )
  })

  it('tells a delivered candidate withdrawn since from its run\'s candidates read now', () => {
    const byRun = new Map([['team-0000aaaa', tasks(work('build', cand(2, { withdrawn: true }), cand(1)))]])
    expect(candidateWithdrawn(`team-0000aaaa/build/2@${'2'.repeat(40)}`, byRun)).toBe(true)
    expect(candidateWithdrawn(`team-0000aaaa/build/1@${'1'.repeat(40)}`, byRun)).toBe(false)
    expect(candidateWithdrawn(`team-0000cccc/build/2@${'2'.repeat(40)}`, byRun)).toBe(false)
    expect(candidateWithdrawn(undefined, byRun)).toBe(false)
  })

  it('tells a delivered candidate withdrawn when it is older than the six its task lists, from the task\'s withdrawn', () => {
    // Attempts 8 to 3 listed, none withdrawn; attempt 2 was, before them: the task's withdrawn says so.
    const listed = [8, 7, 6, 5, 4, 3].map((n) => cand(n))
    const byRun = new Map([['team-0000aaaa', tasks({ ...work('build', ...listed), more: 2, withdrawn: 2 })]])
    expect(candidateWithdrawn(`team-0000aaaa/build/2@${'2'.repeat(40)}`, byRun)).toBe(true)
    expect(candidateWithdrawn(`team-0000aaaa/build/1@${'1'.repeat(40)}`, byRun)).toBe(true)
    expect(candidateWithdrawn(`team-0000aaaa/build/3@${'3'.repeat(40)}`, byRun)).toBe(false)
    // A task with nothing withdrawn: an attempt past the listed ones is not.
    const none = new Map([['team-0000aaaa', tasks({ ...work('build', ...listed), more: 2, withdrawn: 0 })]])
    expect(candidateWithdrawn(`team-0000aaaa/build/2@${'2'.repeat(40)}`, none)).toBe(false)
  })
})

describe("a ticket's decisions and people (DS2, PR-12)", () => {
  const plan = (n: number, p: Partial<PlanSummary> = {}): PlanSummary => ({ id: `p${n}`, ticketId: 't1', n, crewId: 'team', crewName: 'the team', base: 'b'.repeat(40), digest: '', bytes: 0, state: 'proposed', createdAt: `2026-11-0${n}T09:00:00Z`, createdBy: 'Nate', fingerprint: '', rev: 1, ...p })
  const close = { requires: [] as never[], met: { acceptance: true, delivery: false }, missing: [] as never[] }

  it('lists the approvals with their notes, the acceptance, the Accept anyway receipts and the decision records, newest first', () => {
    const rows = ticketDecisions({
      plans: [
        plan(2, { state: 'approved', approvedAt: '2026-11-02T10:00:00Z', approvedBy: 'Nate', approvedNote: 'Ship the smaller fix.' }),
        plan(1, { state: 'superseded', approvedAt: '2026-11-01T10:00:00Z', approvedBy: 'Priya' }),
        plan(3),
      ],
      ticket: {
        acceptedAt: '2026-11-04T10:00:00Z',
        acceptedBy: 'Alice',
        acceptedFor: 'Alice',
        acceptedNote: 'It holds.',
        close,
        links: [
          { id: 'd1', kind: 'decision', ref: 'dr-1', url: '', note: 'from the other replica', createdAt: '2026-11-01T08:00:00Z', createdBy: 'Sam' },
          { id: 'd2', kind: 'decision', ref: 'dr-2', url: '', note: '', createdAt: '2026-11-01T08:00:00Z', createdBy: 'Sam', deletedAt: 'x' },
          { id: 'l1', kind: 'delivery', ref: 'u', url: 'u', note: '', createdAt: '2026-11-05T08:00:00Z', createdBy: 'Sam' },
        ],
      },
      runs: [{ run: 'team-0000aaaa', label: 'CND-12', tasks: [{ task: 'build', kind: 'work', more: 0, candidates: [{ task: 'build', kind: 'work', attempt: 3, commit: 'c'.repeat(40), state: 'failed', standing: true, withdrawn: false, acceptedAnyway: { actor: 'Nate', at: '2026-11-03T10:00:00Z', command: 'c1' }, receipts: [] }] }] }],
    })
    expect(rows.map((r) => [r.kind, r.title, r.by, r.state, r.note])).toEqual([
      ['acceptance', 'Accepted for Alice', 'Alice', 'holds', 'It holds.'],
      ['accepted-anyway', 'Accepted build#3 anyway', 'Nate', 'CND-12: no check: it cannot be verified', ''],
      ['approval', 'Approved plan r2 (the team)', 'Nate', 'holds', 'Ship the smaller fix.'],
      ['approval', 'Approved plan r1 (the team)', 'Priya', 'superseded by a later approval', ''],
      ['record', 'A decision record', 'Sam', 'dr-1', 'from the other replica'],
    ])
    expect(rows[1]!.run).toBe('team-0000aaaa')
    // A stale approval says why; an acceptance no longer met says the server's words.
    const stale = ticketDecisions({
      plans: [plan(1, { state: 'approved', approvedAt: 'x', stale: { words: 'the description changed since revision 1 was approved', remedy: 'approve' } })],
      ticket: { acceptedAt: 'y', acceptedFor: 'Alice', close: { ...close, met: { acceptance: false, delivery: false }, why: 'accepted for Alice; the owner is now Bob' }, links: [] },
      runs: [],
    })
    expect(stale.map((r) => [r.state, r.tone])).toEqual([
      ['accepted for Alice; the owner is now Bob', 'warning'],
      ['the description changed since revision 1 was approved', 'warning'],
    ])
  })

  it('names each person once with every role, people before agents, and the comments of live comments alone', () => {
    const people = ticketPeople({
      ticket: {
        owner: 'Alice',
        assignees: ['person:Bob', 'agent:claude', 'Alice'],
        comments: [
          { id: 'c1', ticketId: 't1', n: 1, author: 'admin', by: 'Nate', body: '', createdAt: '', rev: 1 },
          { id: 'c2', ticketId: 't1', n: 2, author: 'agent:builder', by: '', body: '', createdAt: '', rev: 1 },
          { id: 'c3', ticketId: 't1', n: 3, author: 'admin', by: 'Gone', body: '', createdAt: '', deletedAt: 'x', rev: 1 },
        ],
      },
      plans: [plan(1, { approvedAt: 'x', approvedBy: 'Nate' }), plan(2)],
      launches: [{ createdBy: 'Priya' }, { createdBy: 'Nate' }],
      agentName: (id) => (id === 'claude' ? 'Claude Code' : undefined),
    })
    expect(people.map((p) => [p.kind, p.name, p.roles.join(' ')])).toEqual([
      ['person', 'Alice', 'owner assignee'],
      ['person', 'Bob', 'assignee'],
      ['person', 'Nate', 'approver launcher commenter'],
      ['person', 'Priya', 'launcher'],
      ['agent', 'Claude Code', 'assignee'],
      ['agent', 'builder', 'commenter'],
    ])
  })
})
