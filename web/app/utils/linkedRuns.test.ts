import { afterEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, reactive, ref } from 'vue'
import type { RunInfo } from '~/composables/useSessions'
import { RETRY_CAP_MS, RETRY_FIRST_MS, linkedRunRecords } from './linkedRuns'
import { RunStore } from './runs'

// The records of a ticket's linked runs (DispatchRuns.vue): read for a run the live store does not hold, and read again when the store
// drops one while the page is open (review round 1 on PR-11: the section stayed at "Reading…" with its task rows gone).

function run(id: string, record = false): RunInfo {
  return { id, record } as unknown as RunInfo
}

/** A live store whose runs the test holds and drops, and whose reads it answers by hand. */
function store() {
  const held = reactive(new Map<string, RunInfo>())
  const reads: Array<{ id: string; answer: (r: RunInfo | null) => void; fail: () => void }> = []
  const live = {
    runOf: (id: string) => held.get(id),
    refreshRun: (id: string) =>
      new Promise<RunInfo | null>((resolve, reject) => {
        reads.push({ id, answer: resolve, fail: () => reject(new Error('Cannot reach the server')) })
      }),
  }
  return { held, reads, live }
}

/** Lets a read's answer and the watch after it run. */
async function settle() {
  for (let i = 0; i < 3; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

describe("a ticket's linked runs", () => {
  it('reads no record of a run the live store holds, and reads one when the store drops it', async () => {
    const s = store()
    s.held.set('crew-aaaa0001', run('crew-aaaa0001'))
    const scope = effectScope()
    const r = scope.run(() => linkedRunRecords(() => ['crew-aaaa0001'], s.live))!
    await settle()
    expect(s.reads).toHaveLength(0)
    expect(r.runOf('crew-aaaa0001')?.record).toBe(false)

    // The engine forgets the run (past its limit, or after a restart's snapshot): its record is read, and shown.
    s.held.delete('crew-aaaa0001')
    await settle()
    expect(s.reads.map((x) => x.id)).toEqual(['crew-aaaa0001'])
    expect(r.runOf('crew-aaaa0001')).toBeUndefined()
    s.reads[0]!.answer(run('crew-aaaa0001', true))
    await settle()
    expect(r.runOf('crew-aaaa0001')?.record).toBe(true)
    scope.stop()
  })

  it('reads again a run whose first read answered live, once the store drops it; a run held again drops its stale record', async () => {
    const s = store()
    const scope = effectScope()
    const r = scope.run(() => linkedRunRecords(() => ['crew-bbbb0001'], s.live))!
    await settle()
    expect(s.reads).toHaveLength(1)
    // The server held it: the store has it now, and it is no record.
    s.held.set('crew-bbbb0001', run('crew-bbbb0001'))
    s.reads[0]!.answer(run('crew-bbbb0001'))
    await settle()
    expect(r.records.has('crew-bbbb0001')).toBe(false)

    s.held.delete('crew-bbbb0001')
    await settle()
    expect(s.reads).toHaveLength(2)
    s.reads[1]!.answer(run('crew-bbbb0001', true))
    await settle()
    expect(r.runOf('crew-bbbb0001')?.record).toBe(true)

    // Continued after a restart: the store holds it again, and the record goes, so a later drop reads it afresh.
    s.held.set('crew-bbbb0001', run('crew-bbbb0001'))
    await settle()
    expect(r.records.has('crew-bbbb0001')).toBe(false)
    s.held.delete('crew-bbbb0001')
    await settle()
    expect(s.reads).toHaveLength(3)
    scope.stop()
  })

  it('reads each run once while its read is on its way, keeps null for an id the server knows nothing of, and reads a run linked later', async () => {
    const s = store()
    const ids = ref(['crew-cccc0001'])
    const scope = effectScope()
    const r = scope.run(() => linkedRunRecords(() => ids.value, s.live))!
    await settle()
    // Another run's change while the read is on its way asks nothing more.
    s.held.set('crew-other001', run('crew-other001'))
    s.held.delete('crew-other001')
    await settle()
    expect(s.reads).toHaveLength(1)
    s.reads[0]!.answer(null)
    await settle()
    expect(r.runOf('crew-cccc0001')).toBeNull()

    ids.value = ['crew-cccc0001', 'crew-cccc0002']
    await settle()
    expect(s.reads.map((x) => x.id)).toEqual(['crew-cccc0001', 'crew-cccc0002'])
    s.reads[1]!.fail()
    await settle()
    expect(r.runOf('crew-cccc0002')).toBeUndefined()
    scope.stop()
  })

  // Review round 2 on PR-11: a read that settles with its run still unresolved (a failure, or a live answer the store dropped as older than
  // a removal made while it was on its way) changes nothing the watch sees, so it is made again after a capped backoff.
  describe('a read that leaves its run unresolved', () => {
    afterEach(() => {
      vi.useRealTimers()
    })

    it('is made again after a failure, the wait doubling up to its cap, and stops once answered', async () => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      const s = store()
      const scope = effectScope()
      const r = scope.run(() => linkedRunRecords(() => ['crew-dddd0001'], s.live))!
      await settle()
      let waited = 0
      for (let n = 1; n <= 7; n++) {
        expect(s.reads).toHaveLength(n)
        s.reads[n - 1]!.fail()
        await settle()
        expect(r.runOf('crew-dddd0001')).toBeUndefined()
        const wait = Math.min(RETRY_CAP_MS, RETRY_FIRST_MS * 2 ** (n - 1))
        waited += wait
        await vi.advanceTimersByTimeAsync(wait - 1)
        expect(s.reads).toHaveLength(n)
        await vi.advanceTimersByTimeAsync(1)
      }
      expect(waited).toBe(1000 + 2000 + 4000 + 8000 + 16000 + 30000 + 30000)
      expect(s.reads).toHaveLength(8)
      s.reads[7]!.answer(run('crew-dddd0001', true))
      await settle()
      expect(r.runOf('crew-dddd0001')?.record).toBe(true)
      await vi.advanceTimersByTimeAsync(RETRY_CAP_MS * 2)
      expect(s.reads).toHaveLength(8)
      scope.stop()
    })

    it('is made again when the real store drops its live answer as older than a removal made while it was on its way', async () => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      const answers: Array<(r: RunInfo | null) => void> = []
      const version = ref(0)
      const rs = new RunStore(
        { get: () => new Promise<RunInfo | null>((resolve) => answers.push(resolve)), list: async () => [] },
        () => version.value++,
      )
      const live = { runOf: (id: string) => (version.value, rs.runs.get(id)), refreshRun: (id: string) => rs.read(id) }
      const scope = effectScope()
      const r = scope.run(() => linkedRunRecords(() => ['crew-eeee0001'], live))!
      await settle()
      expect(answers).toHaveLength(1)
      // A snapshot that does not hold it lands while the read is on its way; the read's live answer is then the older and is dropped.
      rs.remove('crew-eeee0001')
      answers[0]!(run('crew-eeee0001'))
      await settle()
      expect(rs.runs.has('crew-eeee0001')).toBe(false)
      expect(r.runOf('crew-eeee0001')).toBeUndefined()
      await vi.advanceTimersByTimeAsync(RETRY_FIRST_MS)
      expect(answers).toHaveLength(2)
      answers[1]!(run('crew-eeee0001', true))
      await settle()
      expect(r.runOf('crew-eeee0001')?.record).toBe(true)
      scope.stop()
    })

    it('waits no more once the section is gone, the run is held again or unlinked', async () => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      const s = store()
      const ids = ref(['crew-ffff0001', 'crew-ffff0002', 'crew-ffff0003'])
      const scope = effectScope()
      scope.run(() => linkedRunRecords(() => ids.value, s.live))
      await settle()
      expect(s.reads).toHaveLength(3)
      for (const x of s.reads) x.fail()
      await settle()
      // One is held by the live store again, one unlinked: neither is read once its wait ends.
      s.held.set('crew-ffff0001', run('crew-ffff0001'))
      ids.value = ['crew-ffff0001', 'crew-ffff0003']
      await settle()
      await vi.advanceTimersByTimeAsync(RETRY_FIRST_MS)
      expect(s.reads.map((x) => x.id)).toEqual(['crew-ffff0001', 'crew-ffff0002', 'crew-ffff0003', 'crew-ffff0003'])
      s.reads[3]!.fail()
      await settle()
      scope.stop()
      expect(vi.getTimerCount()).toBe(0)
      await vi.advanceTimersByTimeAsync(RETRY_CAP_MS * 2)
      expect(s.reads).toHaveLength(4)
    })
  })
})
