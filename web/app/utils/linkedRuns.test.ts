import { describe, expect, it } from 'vitest'
import { effectScope, nextTick, reactive, ref } from 'vue'
import type { RunInfo } from '~/composables/useSessions'
import { linkedRunRecords } from './linkedRuns'

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
})
