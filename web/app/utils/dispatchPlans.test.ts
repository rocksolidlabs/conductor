import { describe, expect, it } from 'vitest'
import type { DispatchChange, LaunchSummary, PlanBody, PlanSummary } from '~/composables/useDispatch'
import {
  approvable,
  approvedRevision,
  argvLine,
  defaultLabel,
  expectedChecks,
  historyLaunches,
  launchBlockers,
  launchOutcome,
  parseReference,
  planReasons,
  planRemedy,
  planStateWord,
  referenceText,
  resolvedArgv,
  revisionTag,
  yoloWords,
} from './dispatchPlans'

// The Plan section's words and rules (docs/plans/dispatch-evidence.md, "web/app", utils/dispatchPlans.ts).

function rev(p: Partial<PlanSummary> = {}): PlanSummary {
  return {
    id: 'a'.repeat(16),
    ticketId: 't1',
    n: 3,
    crewId: 'build-crew',
    crewName: 'Build crew',
    base: '0123456789abcdef0123456789abcdef01234567',
    digest: 'd'.repeat(64),
    bytes: 100,
    state: 'approved',
    createdAt: '2026-11-01T10:00:00Z',
    createdBy: 'Nate',
    fingerprint: 'f',
    rev: 2,
    ...p,
  }
}

const ticket = { key: 'CND-12', status: 'in_progress' as const, deletedAt: undefined }

describe("a revision's words", () => {
  it('names each state, an approved one whose approval no longer holds apart', () => {
    expect(planStateWord(rev()).label).toBe('approved')
    expect(planStateWord(rev({ stale: { words: 'the description changed since revision 3 was approved', remedy: 'approve' } })).label).toBe('approval stale')
    expect(planStateWord(rev({ state: 'proposed' })).label).toBe('proposed')
    expect(planStateWord(rev({ state: 'superseded' })).label).toBe('superseded')
    expect(planStateWord(rev({ state: 'withdrawn' })).label).toBe('withdrawn')
    expect(revisionTag(3)).toBe('r3')
  })

  it('offers Approve again for an intent edit alone', () => {
    const p = rev({ stale: { words: 'the description changed since revision 3 was approved', remedy: 'approve' } })
    expect(planReasons(p)).toEqual([{ kind: 'stale', words: 'the description changed since revision 3 was approved', remedy: 'approve', blocks: true }])
    expect(planRemedy(p)).toBe('approve')
    expect(approvable(p)).toBe(true)
  })

  it('offers Revise, never Approve again, for a comment handed over edited or gone', () => {
    const p = rev({ stale: { words: 'comment 4 was edited since revision 3 was made', remedy: 'revise' } })
    expect(planRemedy(p)).toBe('revise')
    expect(approvable(p)).toBe(false)
  })

  it('offers Revise, never Approve again, for an agent that resolves otherwise, even beside an intent edit', () => {
    const p = rev({ agentsChanged: true, stale: { words: 'the title changed since revision 3 was approved', remedy: 'approve' } })
    expect(planReasons(p).map((r) => [r.kind, r.remedy])).toEqual([
      ['stale', 'approve'],
      ['agents', 'revise'],
    ])
    expect(planRemedy(p)).toBe('revise')
    expect(approvable(p)).toBe(false)
    expect(approvable(rev({ state: 'proposed', agentsChanged: true }))).toBe(false)
  })

  it('says a changed crew, with Revise, without stopping a launch: the revision keeps the crew it was made from', () => {
    const p = rev({ crewChanged: true })
    expect(planReasons(p)).toEqual([expect.objectContaining({ kind: 'crew', remedy: 'revise', blocks: false })])
    expect(planRemedy(p)).toBeNull()
    expect(launchBlockers(p, { ticket, project: 'CND', archived: false })).toEqual([])
  })

  it('holds for an approval that holds, and approves a proposed or a superseded revision', () => {
    expect(planRemedy(rev())).toBeNull()
    expect(approvable(rev())).toBe(false)
    expect(approvable(rev({ state: 'proposed' }))).toBe(true)
    expect(approvable(rev({ state: 'superseded' }))).toBe(true)
    expect(approvable(rev({ state: 'withdrawn' }))).toBe(false)
    expect(planReasons(rev({ state: 'withdrawn', crewChanged: true, agentsChanged: true }))).toEqual([])
  })
})

describe('the expected checks', () => {
  const body = (manifest: PlanBody['crew']['manifest'], manifestFile?: PlanBody['manifestFile']): Pick<PlanBody, 'crew' | 'manifestFile'> => ({
    crew: {
      id: 'c',
      name: 'C',
      tasks: [
        { id: 'build', member: 'builder', kind: 'work', checks: [{ id: 'test', argv: ['go', 'test', './...'] }, { id: 'vet', argv: ['go', 'vet'], timeoutSeconds: 60 }] },
        { id: 'review', member: 'reviewer', kind: 'review', needs: ['build'] },
      ],
      manifest,
    },
    manifestFile,
  })

  it("are the work tasks' checks in order, then the crew's own manifest's verify steps", () => {
    expect(expectedChecks(body({ verify: [{ id: 'lint', argv: ['make', 'lint'] }] }))).toEqual([
      { phase: 'check', task: 'build', id: 'test', argv: ['go', 'test', './...'], timeoutSeconds: undefined },
      { phase: 'check', task: 'build', id: 'vet', argv: ['go', 'vet'], timeoutSeconds: 60 },
      { phase: 'verify', id: 'lint', argv: ['make', 'lint'], timeoutSeconds: undefined },
    ])
  })

  it("take a file-form manifest's verify steps from the file as read at the base, never the crew's", () => {
    const b = body({ file: true, verify: [{ id: 'ignored', argv: ['x'] }] }, { path: 'conductor.manifest.json', blob: 'b'.repeat(40), manifest: { verify: [{ id: 'e2e', argv: ['make', 'e2e'] }] } })
    expect(expectedChecks(b).map((c) => c.id)).toEqual(['test', 'vet', 'e2e'])
    expect(expectedChecks(body(undefined)).map((c) => c.id)).toEqual(['test', 'vet'])
  })
})

describe('the command as resolved', () => {
  it('is the catalog command, the start arguments, the yolo arguments, then the member’s', () => {
    expect(resolvedArgv({ command: ['aider', '--model', 'sonnet'], startArgs: ['--session-id', '{id}'], yoloArgs: ['--yes'] }, ['--verbose'])).toEqual(['aider', '--model', 'sonnet', '--session-id', '{id}', '--yes', '--verbose'])
    expect(resolvedArgv(undefined, ['-x'])).toEqual(['-x'])
    expect(argvLine(['go', 'test', 'a b', '', 'it"s'])).toBe('go test "a b" "" "it\\"s"')
  })

  it('says the yolo choice plainly', () => {
    expect(yoloWords(true).label).toBe('Yolo on')
    expect(yoloWords(true).words).toContain('without their permission prompts')
    expect(yoloWords(false).label).toBe('Yolo off')
    expect(yoloWords(undefined).label).toBe('Yolo off')
  })
})

describe('why Launch is not offered', () => {
  it('says no approved revision', () => {
    expect(launchBlockers(null, { ticket, project: 'CND', archived: false })).toEqual(['No plan revision is approved: approve one to launch'])
    expect(launchBlockers(rev({ state: 'proposed' }), { ticket, project: 'CND', archived: false })).toEqual(['No plan revision is approved: approve one to launch'])
  })

  it('says a stale approval with its remedy', () => {
    expect(launchBlockers(rev({ stale: { words: 'the title changed since revision 3 was approved', remedy: 'approve' } }), { ticket, project: 'CND', archived: false })).toEqual([
      'the title changed since revision 3 was approved: approve it again or revise it',
    ])
    expect(launchBlockers(rev({ stale: { words: 'comment 4 was deleted since revision 3 was made', remedy: 'revise' } }), { ticket, project: 'CND', archived: false })).toEqual([
      'comment 4 was deleted since revision 3 was made: revise it',
    ])
    expect(launchBlockers(rev({ agentsChanged: true }), { ticket, project: 'CND', archived: false })[0]).toMatch(/resolves otherwise .*: revise it$/)
  })

  it('says the ticket closed or deleted, and the project archived', () => {
    expect(launchBlockers(rev(), { ticket: { ...ticket, status: 'done' }, project: 'CND', archived: false })).toEqual(['CND-12 is done: reopen it to launch'])
    expect(launchBlockers(rev(), { ticket: { ...ticket, status: 'cancelled' }, project: 'CND', archived: false })).toEqual(['CND-12 is cancelled: reopen it to launch'])
    expect(launchBlockers(rev(), { ticket: { ...ticket, deletedAt: '2026-11-01T00:00:00Z' }, project: 'CND', archived: false })).toEqual(['CND-12 is deleted: restore it to launch'])
    expect(launchBlockers(rev(), { ticket, project: 'CND', archived: true })).toEqual(['CND is archived: unarchive it to launch'])
  })

  it("says managed runs off and no ledger, as the server's refusals said them", () => {
    const server = { managedOff: 'crews with tasks are off on this server (crews.managed)', noLedger: 'the ledger could not be opened; see the server’s log' }
    expect(launchBlockers(rev(), { ticket, project: 'CND', archived: false, server })).toEqual([server.managedOff, server.noLedger])
    expect(launchBlockers(rev(), { ticket, project: 'CND', archived: false, server: {} })).toEqual([])
  })

  it('names the newest approved revision as the one to launch, where a merge left two', () => {
    const older = rev({ id: '1'.repeat(16), n: 1, createdAt: '2026-11-01T09:00:00Z' })
    const newer = rev({ id: '2'.repeat(16), n: 2, createdAt: '2026-11-01T11:00:00Z' })
    expect(approvedRevision([older, newer, rev({ id: '3'.repeat(16), state: 'proposed', createdAt: '2026-11-01T12:00:00Z' })])?.n).toBe(2)
    expect(approvedRevision([rev({ state: 'superseded' })])).toBeNull()
  })
})

describe("the reference field's parser", () => {
  it('reads a path, a line, a range and a note', () => {
    expect(parseReference('internal/api/session.go')).toEqual({ ref: { path: 'internal/api/session.go' } })
    expect(parseReference('internal/api/session.go:12')).toEqual({ ref: { path: 'internal/api/session.go', start: 12 } })
    expect(parseReference('internal/api/session.go:12-30')).toEqual({ ref: { path: 'internal/api/session.go', start: 12, end: 30 } })
    expect(parseReference(' internal/api/session.go:12-30=the loop, = and all ')).toEqual({ ref: { path: 'internal/api/session.go', start: 12, end: 30, note: 'the loop, = and all' } })
    expect(parseReference('docs/=why')).toEqual({ ref: { path: 'docs', note: 'why' } })
    expect(parseReference('./web/app')).toEqual({ ref: { path: 'web/app' } })
    expect(parseReference('a:b.go:7')).toEqual({ ref: { path: 'a:b.go', start: 7 } })
    expect(parseReference('a.go:7-7')).toEqual({ ref: { path: 'a.go', start: 7 } })
  })

  it('refuses what is no reference, in words', () => {
    expect(parseReference('')).toEqual({ error: 'A reference names a path' })
    expect(parseReference(':12')).toEqual({ error: 'A reference names a path before its lines' })
    expect('error' in parseReference('/etc/passwd')).toBe(true)
    expect('error' in parseReference('C:\\x')).toBe(true)
    expect('error' in parseReference('../secrets')).toBe(true)
    expect('error' in parseReference('a/../../b')).toBe(true)
    expect(parseReference('a.go:0')).toEqual({ error: 'a.go: lines are numbered from 1' })
    expect(parseReference('a.go:30-12')).toEqual({ error: 'a.go: the range 30-12 ends before it starts' })
    expect('error' in parseReference(`a.go=${'x'.repeat(201)}`)).toBe(true)
    expect('ref' in parseReference(`a.go=${'é'.repeat(200)}`)).toBe(true)
    expect('error' in parseReference('x'.repeat(1025))).toBe(true)
  })

  it('writes a reference back as the field reads it', () => {
    for (const text of ['a.go', 'a.go:3', 'a.go:3-9', 'a.go:3-9=why', 'docs=why']) {
      const p = parseReference(text)
      expect('ref' in p && referenceText(p.ref)).toBe(text)
    }
  })
})

describe('the default label', () => {
  it("is the key and the title, whole when it fits in 60 characters", () => {
    expect(defaultLabel('CND-12', 'Fix the login loop')).toBe('CND-12 Fix the login loop')
    const exact = `CND-12 ${'a'.repeat(53)}`
    expect(defaultLabel('CND-12', 'a'.repeat(53))).toBe(exact)
  })

  it('is cut at a word, never inside the key, by characters, not bytes', () => {
    const title = 'Make the login loop stop when the session token expires mid request again'
    const label = defaultLabel('CND-12', title)
    expect(Array.from(label).length).toBeLessThanOrEqual(60)
    expect(label).toBe('CND-12 Make the login loop stop when the session token')
    expect(defaultLabel('CND-12', 'x'.repeat(80))).toBe(`CND-12 ${'x'.repeat(53)}`)
    expect(Array.from(defaultLabel('CND-12', 'é'.repeat(80))).length).toBe(60)
  })
})

describe("a launch's outcome", () => {
  const launch = (p: Partial<LaunchSummary>): LaunchSummary => ({
    id: 'l'.repeat(16),
    ticketId: 't1',
    ticketKey: 'CND-12',
    planId: 'a'.repeat(16),
    planN: 3,
    runId: 'build-crew-0123abcd',
    label: 'CND-12 Fix the login loop',
    ticketRev: 7,
    intent: 'i',
    criteria: 'c',
    packageSha256: 'p',
    state: 'started',
    agents: [],
    createdAt: '2026-11-01T10:00:00Z',
    createdBy: 'Nate',
    fingerprint: 'f',
    rev: 2,
    ...p,
  })
  const receipt = { opId: 'op', intent: 'ticket.launch', status: 'done' as const, id: 'l'.repeat(16) }

  it('is started with its run, a repeat said so', () => {
    const o = launchOutcome({ reply: { receipt, launch: launch({}), run: null } })
    expect(o).toEqual({ state: 'started', launch: launch({}), run: null, repeat: false })
    expect(launchOutcome({ reply: { receipt: { ...receipt, repeat: true }, launch: launch({}), run: null } })).toMatchObject({ state: 'started', repeat: true })
  })

  it('is failed with why, from a 409 launch_failed carrying the launch', () => {
    const l = launch({ state: 'failed', why: 'the agent worker is not installed on the server' })
    expect(launchOutcome({ failure: { status: 409, code: 'launch_failed', message: l.why!, body: { error: {}, receipt, launch: l } } })).toEqual({ state: 'failed', words: l.why, launch: l })
  })

  it('is pending from a 503 ledger_unavailable carrying the launch, and refused from one carrying none', () => {
    const l = launch({ state: 'pending' })
    expect(launchOutcome({ failure: { status: 503, code: 'ledger_unavailable', message: 'the launch is still being started: send it again', body: { receipt, launch: l } } })).toEqual({
      state: 'pending',
      words: 'the launch is still being started: send it again',
      launch: l,
    })
    expect(launchOutcome({ failure: { status: 503, code: 'ledger_unavailable', message: 'the server is taking over the ledger', body: { error: {} } } })).toEqual({
      state: 'refused',
      words: 'the server is taking over the ledger',
      code: 'ledger_unavailable',
      receipt: undefined,
    })
  })

  it("is refused in the receipt's words when the store kept one, else the server's", () => {
    const failed = { opId: 'op', intent: 'ticket.launch', status: 'failed' as const, refusal: 'stale_plan', error: 'the description changed since revision 3 was approved: approve it again or revise it' }
    expect(launchOutcome({ failure: { status: 409, code: 'stale_plan', message: 'x', body: { error: {}, receipt: failed } } })).toEqual({ state: 'refused', words: failed.error, code: 'stale_plan', receipt: failed })
    expect(launchOutcome({ failure: { status: 409, code: 'managed_off', message: 'crews with tasks are off on this server (crews.managed)' } })).toMatchObject({ state: 'refused', code: 'managed_off' })
  })
})

describe('the launches the history names', () => {
  const row = (p: Partial<DispatchChange>): DispatchChange => ({ seq: 1, entity: 'ticket', entityId: 't1', op: 'set', value: '', at: '2026-11-01T10:00:00Z', actor: 'admin', opId: 'o', n: 0, ...p })

  it("are the launches' create rows, newest first, with what never changes of them", () => {
    const changes = [
      row({ seq: 1, entity: 'launch', entityId: 'l1', op: 'create', value: JSON.stringify({ id: 'l1', runId: 'c-00000001', planId: 'p1', label: 'one', createdAt: '2026-11-01T10:00:00Z', createdBy: 'Nate' }) }),
      row({ seq: 2, entity: 'launch', entityId: 'l1', field: 'state', value: 'started', actor: 'server' }),
      row({ seq: 3, entity: 'plan', entityId: 'p1', op: 'create', value: '{}' }),
      row({ seq: 4, entity: 'launch', entityId: 'l2', op: 'create', value: JSON.stringify({ id: 'l2', runId: 'c-00000002', planId: 'p1', label: 'two', createdAt: '2026-11-01T11:00:00Z', createdBy: 'Priya' }) }),
      row({ seq: 5, entity: 'launch', entityId: 'l3', op: 'create', value: 'not json', at: '2026-11-01T09:00:00Z', by: 'Sam' }),
    ]
    expect(historyLaunches(changes)).toEqual([
      { id: 'l2', runId: 'c-00000002', planId: 'p1', label: 'two', createdAt: '2026-11-01T11:00:00Z', createdBy: 'Priya' },
      { id: 'l1', runId: 'c-00000001', planId: 'p1', label: 'one', createdAt: '2026-11-01T10:00:00Z', createdBy: 'Nate' },
      { id: 'l3', runId: '', planId: '', label: '', createdAt: '2026-11-01T09:00:00Z', createdBy: 'Sam' },
    ])
  })
})
