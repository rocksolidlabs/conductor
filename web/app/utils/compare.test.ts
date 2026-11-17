import { describe, expect, it } from 'vitest'
import type { RunTask } from '~/composables/useSessions'
import {
  NOT_OBSERVED,
  SAME_PATCHES,
  candidateCommitWords,
  candidateMark,
  cellOpens,
  cellReceipt,
  cellsInColumns,
  cellWords,
  comparisonLink,
  comparisonRuns,
  depsWords,
  independenceWords,
  lanesOf,
  notMeasured,
  notMeasuredLine,
  notObservedWords,
  notRunWords,
  parseCheckRef,
  runCompare,
  diffSizeWords,
  tasksByLane,
  verdictWords,
  verifyFilesWords,
  type CheckCell,
  type ComparedCandidate,
} from './compare'
import { markLook } from './status'

// The comparison's words (Program item CP1, docs/plans/compare-crew.md, PR-4): each verdict as the server gave it, a cell's outcome, the
// sizes, the dependency, test-file and independence words with what is not observed, the lanes of a run. The launch statement's
// words are lane B's (compareLaunch.test.ts).

const cell = (o: Partial<CheckCell> = {}): CheckCell => ({
  task: 'approach-a',
  attempt: 1,
  k: 1,
  state: 'finished',
  exit: 0,
  signal: '',
  timedOut: false,
  durationMs: 4200,
  where: 'native',
  manifest: '',
  stable: true,
  receipt: 'check:e2e-crew-0a1b2c3d/approach-a/1/unit/1',
  logSha256: 'ab'.repeat(32),
  ...o,
})

const candidate = (o: Partial<ComparedCandidate> = {}): ComparedCandidate => ({
  task: 'approach-a',
  member: 'a',
  agent: 'worker-cli',
  args: [],
  approach: '',
  state: 'ready',
  run: 'e2e-crew-0a1b2c3d',
  attempt: 1,
  commit: '4b825dc642cb6eb9a060e54bf8d69288fbee4904',
  start: '1'.repeat(40),
  selectable: true,
  attempts: 1,
  acceptedAnyway: false,
  shared: [],
  equivalent: [],
  manifestChanged: false,
  ...o,
})

describe('a check row', () => {
  it('says each verdict as the server gave it, with its reasons', () => {
    expect(verdictWords({ verdict: 'comparable', why: [] })).toEqual({ label: 'comparable', lead: '', why: [] })
    expect(verdictWords({ verdict: 'not_comparable', why: ["approach-b's commit changes conductor.manifest.json: its checkout ran another manifest"] })).toEqual({
      label: 'not comparable',
      lead: 'because',
      why: ["approach-b's commit changes conductor.manifest.json: its checkout ran another manifest"],
    })
    const unknown = verdictWords({ verdict: 'unknown', why: ['comparable only if their test changes are fair: approach-a changed internal/cache/cache_test.go, a test file and 1 more; approach-b changed none'] })
    expect(unknown.label).toBe('comparable only if their test changes are fair')
    expect(unknown.why).toEqual(['approach-a changed internal/cache/cache_test.go, a test file and 1 more', 'approach-b changed none'])
    expect(verdictWords({ verdict: 'unknown', why: ['another reason'] }).why).toEqual(['another reason'])
    // The server's null reasons are none.
    expect(verdictWords({ verdict: 'comparable', why: null }).why).toEqual([])
  })

  it('never makes a verdict of its own: a word it does not know is said as sent', () => {
    expect(verdictWords({ verdict: 'mostly', why: [] }).label).toBe('mostly')
    expect(verdictWords({ verdict: '', why: [] }).label).toBe('no verdict given')
  })
})

describe('a cell', () => {
  it('says passed, failed with its exit or signal, timed out and changed the tree, with its duration and where it ran', () => {
    expect(cellWords(cell())).toMatchObject({ word: 'passed', detail: '4.2 s · native', state: 'passed', mark: { key: 'done' } })
    expect(cellWords(cell({ exit: 1 }))).toMatchObject({ word: 'failed with exit 1', state: 'failed', mark: { key: 'failed' } })
    expect(cellWords(cell({ exit: null, signal: 'SIGKILL' })).word).toBe('failed with signal SIGKILL')
    expect(cellWords(cell({ exit: null, timedOut: true })).word).toBe('timed out')
    expect(cellWords(cell({ stable: false })).word).toBe('changed the tree it checked')
    expect(cellWords(cell({ state: 'interrupted', exit: null }))).toMatchObject({ word: 'interrupted', mark: { key: 'cancelled' } })
    expect(cellWords(cell({ state: 'running', exit: null, durationMs: 0 }))).toMatchObject({ word: 'running', detail: 'native', mark: { key: 'working', word: 'checking' } })
  })

  it('says why a cell did not run', () => {
    expect(cellWords(cell({ state: 'not_run', notRun: 'earlier_failed', receipt: '' }))).toMatchObject({ word: 'not run: an earlier check failed', detail: '', state: 'not_run', mark: { key: 'waiting' } })
    expect(notRunWords('no_such_step')).toBe('not run: its manifest has no such step')
    expect(notRunWords('pending')).toBe('not run yet: its checks are under way')
    expect(notRunWords('ended')).toBe('not run: its attempt ended before this check')
    expect(notRunWords(undefined)).toBe('not run')
    expect(cellWords(cell({ state: 'cancelled', exit: null }))).toMatchObject({ word: 'cancelled', mark: { key: 'cancelled' } })
  })

  it("draws a task's own marks, never a green one: a passed check is neutral ink", () => {
    expect(markLook(cellWords(cell()).mark.key).tone).toBe('neutral')
    expect(markLook(cellWords(cell({ exit: 2 })).mark.key).tone).toBe('error')
    expect(markLook(cellWords(cell({ state: 'not_run' })).mark.key).tone).toBe('neutral')
  })

  it('keeps each cell under its column: a candidate with no cell draws one with no result', () => {
    const cells = cellsInColumns({ cells: [cell({ task: 'approach-b' })] }, [candidate({ task: 'approach-a', commit: '' }), candidate({ task: 'approach-b' })])
    expect(cells.map((c) => c.task)).toEqual(['approach-a', 'approach-b'])
    expect(cellWords(cells[0]!)).toMatchObject({ word: 'no result to check', state: 'none', mark: { key: 'waiting' } })
    expect(cellOpens(cells[0]!)).toBe(false)
    expect(cellOpens(cells[1]!)).toBe(true)
    expect(cellReceipt({ phase: 'check', id: 'unit' }, cells[0]!, candidate())).toBeNull()
  })

  it("opens the receipt its ref names, and none for a cell that did not run", () => {
    expect(parseCheckRef('check:e2e-crew-0a1b2c3d/approach-a/2/unit/3')).toEqual({ run: 'e2e-crew-0a1b2c3d', task: 'approach-a', attempt: 2, id: 'unit', k: 3 })
    expect(parseCheckRef('candidate:x/y/1')).toBeNull()
    const r = cellReceipt({ phase: 'check', id: 'unit' }, cell({ exit: 1 }), candidate())
    expect(r).toMatchObject({ run: 'e2e-crew-0a1b2c3d', task: 'approach-a', attempt: 1, commit: candidate().commit, receipt: { phase: 'check', id: 'unit', k: 1, exit: 1, state: 'finished', logSha256: 'ab'.repeat(32) } })
    expect(cellReceipt({ phase: 'verify', id: 'lint' }, cell({ receipt: '' }), candidate({ run: 'e2e-crew-11112222' }))?.run).toBe('e2e-crew-11112222')
    expect(cellReceipt({ phase: 'check', id: 'unit' }, cell({ state: 'not_run' }), candidate())).toBeNull()
  })
})

describe('a candidate', () => {
  it("says its state in the status language: a failed lane's commit is not selectable", () => {
    expect(candidateMark(candidate({ state: 'running', commit: '' }))).toMatchObject({ key: 'working', word: 'running' })
    expect(candidateMark(candidate())).toMatchObject({ key: 'done', word: 'ready' })
    expect(candidateMark(candidate({ state: 'failed', selectable: false }))).toMatchObject({ key: 'failed', word: 'failed: not selectable' })
    expect(candidateMark(candidate({ state: 'failed', commit: '', selectable: false })).word).toBe('failed')
    expect(candidateMark(candidate({ state: 'cancelled' })).key).toBe('cancelled')
    expect(candidateCommitWords(candidate({ state: 'failed', selectable: false }))).toBe('last submitted commit · failed: not selectable')
    expect(candidateCommitWords(candidate({ commit: '', state: 'running' }))).toBe('no result yet')
    expect(candidateCommitWords(candidate({ acceptedAnyway: true }))).toBe('standing result · accepted anyway')
    // A lane still worked whose last submitted commit is shown (its checks under way, or a further attempt): not failed.
    expect(candidateCommitWords(candidate({ state: 'running', selectable: false }))).toBe('last submitted commit · not a result yet: its checks or a further attempt are under way')
    expect(candidateCommitWords(candidate({ state: 'cancelled', selectable: false }))).toBe('last submitted commit · cancelled: not selectable')
  })

  it('says what was not measured of a candidate, never a change it did not see', () => {
    const size = { files: 1, added: 1, removed: 0, filesAdded: 0, filesDeleted: 0, list: [], total: 1 }
    // Measured whole: every row holds what was measured, no dependency or test file changed among it.
    expect(notMeasured(candidate({ size }))).toEqual({ size: '', deps: '', verify: '', independence: '' })
    // Git could not be read for it: nothing of it is said, its independence neither.
    const late = 'not measured: the measure passed its 20 s budget'
    expect(notMeasured(candidate({ unavailable: late }))).toEqual({ size: late, deps: late, verify: late, independence: late })
    // A size read before a later git call failed stands; the parts not read are said so.
    expect(notMeasured(candidate({ size, unavailable: 'git cat-file: exit status 128' }))).toEqual({ size: '', deps: 'git cat-file: exit status 128', verify: 'git cat-file: exit status 128', independence: 'git cat-file: exit status 128' })
    // A diff past its bound: no dependency or test file read, its commits still compared.
    const big = { ...size, notMeasured: 'the diff passes 4 MiB: not measured' }
    expect(notMeasured(candidate({ size: big }))).toEqual({ size: big.notMeasured, deps: big.notMeasured, verify: big.notMeasured, independence: '' })
    expect(notMeasured(candidate({ commit: '' })).deps).toBe('it has no commit')
    expect(notMeasuredLine('it has no commit')).toBe('Not measured: it has no commit.')
    expect(notMeasuredLine(late)).toBe('Not measured: the measure passed its 20 s budget.')
    expect(notMeasuredLine('the diff passes 4 MiB: not measured')).toBe('The diff passes 4 MiB: not measured.')
  })
})

describe('the measured rows', () => {
  it('says the size: files changed, lines added and removed, files added and deleted, and what the list leaves out', () => {
    const w = diffSizeWords({ files: 12, added: 340, removed: 90, filesAdded: 2, filesDeleted: 1, list: [{ path: 'a.go', status: 'M', added: 1, removed: 0, binary: false }], total: 12 })
    expect(w).toEqual({ lines: '12 files changed, +340 −90', files: '2 added, 1 deleted', more: 'and 11 more files not listed' })
    expect(diffSizeWords({ files: 1, added: 0, removed: 0, filesAdded: 0, filesDeleted: 0, list: [{ path: 'a', status: 'M', added: 0, removed: 0, binary: true }], total: 1 })).toEqual({ lines: '1 file changed, +0 −0', files: '', more: '' })
    expect(diffSizeWords({ files: 0, added: 0, removed: 0, filesAdded: 0, filesDeleted: 0, list: [], total: 0, notMeasured: 'the diff passes 4 MiB: not measured' })?.lines).toBe('the diff passes 4 MiB: not measured')
    expect(diffSizeWords(undefined)).toBeNull()
  })

  it('says the dependency changes: the files, and each module added, removed or moved', () => {
    expect(depsWords(undefined).summary).toBe('No dependency file changed')
    expect(depsWords({ files: [], modules: [], total: 0 }).summary).toBe('No dependency file changed')
    const w = depsWords({
      files: ['go.mod', 'go.sum'],
      modules: [
        { file: 'go.mod', name: 'golang.org/x/text', from: '', to: 'v0.3.0' },
        { file: 'go.mod', name: 'github.com/old/dep', from: 'v1.0.0', to: '' },
        { file: 'web/package.json', name: 'vue', from: '3.4.0', to: '3.5.1' },
      ],
      total: 3,
      modulesTotal: 5,
    })
    expect(w.summary).toBe('3 dependency files changed')
    expect(w.modules.map((m) => m.words)).toEqual(['added golang.org/x/text v0.3.0', 'removed github.com/old/dep (was v1.0.0)', 'moved vue from 3.4.0 to 3.5.1'])
    expect(w.more).toBe('and 1 more file and 2 more modules not listed')
    expect(depsWords({ files: ['go.mod'], modules: [], total: 1, modulesTotal: 0 }).more).toBe('')
  })

  it("says each candidate's test and build files, and none", () => {
    expect(verifyFilesWords('approach-b', undefined)).toEqual({ summary: 'approach-b changed no test or build file', files: [], more: '' })
    const w = verifyFilesWords('approach-a', { files: [{ path: 'internal/cache/cache_test.go', kind: 'test' }, { path: 'Makefile', kind: 'build' }], total: 3 })
    expect(w.summary).toBe('approach-a changed 3 test or build files')
    expect(w.files).toEqual(['internal/cache/cache_test.go, a test file', 'Makefile, a build file'])
    expect(w.more).toBe('and 1 more not listed')
  })

  it('says independence as observed, with what it cannot see beside it', () => {
    expect(independenceWords(candidate())).toEqual(['no shared commit and no same patch'])
    expect(independenceWords(candidate({ shared: null, equivalent: null }))).toEqual(['no shared commit and no same patch'])
    expect(independenceWords(candidate({ shared: [{ with: 'approach-b', count: 2, commits: ['a'.repeat(40), 'b'.repeat(40)] }] }))).toEqual(['shares 2 commits with approach-b'])
    expect(independenceWords(candidate({ equivalent: [{ with: 'approach-b', count: 1, commits: ['c'.repeat(40)] }] }))).toEqual(["1 commit carries the same change as one of approach-b's"])
    expect(independenceWords(candidate({ shared: [{ with: 'approach-c', count: 0, commits: [], note: 'more than 10,000' }] }))).toEqual(['shares more than 10,000 commits with approach-c'])
    // The same-patch check's own words are the row's, said once beside it, not on each lane.
    expect(independenceWords(candidate({ equivalent: [{ with: 'approach-b', count: 2, commits: [], note: SAME_PATCHES }] }))).toEqual(["2 commits carry the same change as one of approach-b's"])
    expect(independenceWords(candidate({ equivalent: [{ with: 'approach-c', count: 0, commits: [], note: 'not checked: more than 200 commits' }] }))).toEqual(['same patches as approach-c: not checked: more than 200 commits'])
    expect(NOT_OBSERVED).toBe("a read of another lane's folder, or of what an agent keeps for itself under its HOME, is not observed")
    expect(SAME_PATCHES).toBe('same patches: checked; a change rewritten by hand is not detected')
    expect(notObservedWords()).toBe("Same patches: checked; a change rewritten by hand is not detected; a read of another lane's folder, or of what an agent keeps for itself under its HOME, is not observed.")
    expect(notObservedWords(' A read of another candidate\'s folder is not observed. ')).toBe("Same patches: checked. A read of another candidate's folder is not observed.")
  })
})

describe("a comparison's run", () => {
  const task = (id: string, kind: 'work' | 'review' = 'work', needs?: string[]): RunTask => ({ id, member: 'm', kind, state: 'ready', attempts: 0, ...(needs ? { needs } : {}) })
  const tasks = [task('approach-a'), task('approach-b'), task('review-approach-b', 'review', ['approach-b']), task('review-approach-a', 'review', ['approach-a']), task('notes')]

  it("reads its lanes from the run's compare part, each candidate's review the task whose one need it is", () => {
    expect(lanesOf({ tasks, compare: { launched: 'r', candidates: ['approach-a', 'approach-b'] } })).toEqual([
      { task: 'approach-a', review: 'review-approach-a' },
      { task: 'approach-b', review: 'review-approach-b' },
    ])
    expect(lanesOf({ tasks, compare: { candidates: [{ task: 'approach-b', approach: 'drop the cache' }] } })).toEqual([{ task: 'approach-b', review: 'review-approach-b', approach: 'drop the cache' }])
  })

  it("reads them from the crew's block when the run says none, and none for a run of no comparison", () => {
    expect(lanesOf({ tasks }, { candidates: [{ task: 'approach-a', approach: 'key the cache by locale' }] })).toEqual([{ task: 'approach-a', review: 'review-approach-a', approach: 'key the cache by locale' }])
    expect(lanesOf({ tasks })).toEqual([])
    expect(lanesOf({ tasks, compare: { candidates: [] } })).toEqual([])
    expect(runCompare({ compare: 'x' })).toBeUndefined()
  })

  it('orders the tasks by lane, candidate then review, then the tasks of no lane', () => {
    const { groups, rest } = tasksByLane(tasks, lanesOf({ tasks, compare: { candidates: ['approach-a', 'approach-b'] } }))
    expect(groups.map((g) => g.tasks.map((t) => t.id))).toEqual([
      ['approach-a', 'review-approach-a'],
      ['approach-b', 'review-approach-b'],
    ])
    expect(rest.map((t) => t.id)).toEqual(['notes'])
  })

  it("links to the ticket's compare view, and nowhere without a ticket", () => {
    expect(comparisonLink('CND-12', 'e2e-crew-0a1b2c3d')).toEqual({ path: '/dispatch/CND/12', query: { view: 'compare', run: 'e2e-crew-0a1b2c3d' } })
    expect(comparisonLink(undefined, 'r')).toBeNull()
    expect(comparisonLink('not a key', 'r')).toBeNull()
  })

  it('reads again on a change of the run asked, the launched run or a candidate run', () => {
    expect(comparisonRuns('b', null)).toEqual(['b'])
    expect(comparisonRuns('b', { run: 'a', candidates: [candidate({ run: 'a' }), candidate({ run: 'b' })] })).toEqual(['b', 'a'])
  })
})
