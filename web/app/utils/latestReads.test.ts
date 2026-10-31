import { describe, expect, it } from 'vitest'
import { latestReads, ownedBy, type Owned } from './latestReads'

/** A reply the test hands back when it chooses: its promise, and resolve and reject. */
function reply<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

describe('latestReads', () => {
  it('keeps the last read started when an earlier one replies after it (the prerequisites read at two revisions)', async () => {
    const reads = latestReads()
    const applied: string[] = []
    const failed: unknown[] = []
    const older = reply<string>()
    const newer = reply<string>()
    const a = reads.read(() => older.promise, (v) => applied.push(v), (e) => failed.push(e))
    const b = reads.read(() => newer.promise, (v) => applied.push(v), (e) => failed.push(e))
    // The newer read (the receipt finished) replies first, the older (it was running) last.
    newer.resolve('finished')
    await b
    older.resolve('running')
    await a
    expect(applied).toEqual(['finished'])
    expect(failed).toEqual([])
  })
  it("drops an older read's error as it drops its value, and still says the last read's", async () => {
    const reads = latestReads()
    const applied: string[] = []
    const failed: string[] = []
    const older = reply<string>()
    const newer = reply<string>()
    const a = reads.read(() => older.promise, (v) => applied.push(v), (e) => failed.push((e as Error).message))
    const b = reads.read(() => newer.promise, (v) => applied.push(v), (e) => failed.push((e as Error).message))
    newer.resolve('finished')
    older.reject(new Error('older read failed'))
    await Promise.all([a, b])
    expect(applied).toEqual(['finished'])
    expect(failed).toEqual([])
    const last = reads.read(() => Promise.reject(new Error('the ledger is unavailable')), (v) => applied.push(v), (e) => failed.push((e as Error).message))
    await last
    expect(failed).toEqual(['the ledger is unavailable'])
  })
  it('applies a read in order when nothing overlaps, and none under way after drop', async () => {
    const reads = latestReads()
    const applied: string[] = []
    await reads.read(() => Promise.resolve('one'), (v) => applied.push(v), () => {})
    await reads.read(() => Promise.resolve('two'), (v) => applied.push(v), () => {})
    const late = reply<string>()
    const c = reads.read(() => late.promise, (v) => applied.push(v), () => {})
    reads.drop()
    late.resolve('three')
    await c
    expect(applied).toEqual(['one', 'two'])
  })
})

describe('ownedBy', () => {
  // The run page's attempts on a switch from task a to task b (Codex round 2 on #502): a's attempts stay held while b's read is
  // under way and after it failed, and none of them shows under b, so no receipt of a's builds a log request on b's route.
  it("shows none of one task's attempts under another, while the other's read is delayed and after it failed", async () => {
    const reads = latestReads()
    let held: Owned<string[]> | null = null
    let error: Owned<string> | null = null
    const read = (owner: string, reply: Promise<string[]>) =>
      reads.read(
        () => reply,
        (v) => {
          held = { owner, value: v }
          error = null
        },
        (e) => (error = { owner, value: (e as Error).message }),
      )
    const shown = (owner: string) => ({ attempts: ownedBy(held, owner, [] as string[]), error: ownedBy(error, owner, '') })
    await read('a', Promise.resolve(['a.1 setup:deps:1']))
    expect(shown('a')).toEqual({ attempts: ['a.1 setup:deps:1'], error: '' })
    const b = reply<string[]>()
    const pending = read('b', b.promise)
    expect(shown('b'), 'while b is read').toEqual({ attempts: [], error: '' })
    b.reject(new Error('the ledger is unavailable'))
    await pending
    expect(shown('b'), 'after b failed').toEqual({ attempts: [], error: 'the ledger is unavailable' })
    // Back on a, its attempts show again; b's error is not a's.
    expect(shown('a')).toEqual({ attempts: ['a.1 setup:deps:1'], error: '' })
    const later = reply<string[]>()
    const again = read('b', later.promise)
    later.resolve(['b.1 setup:deps:1'])
    await again
    expect(shown('b')).toEqual({ attempts: ['b.1 setup:deps:1'], error: '' })
    expect(shown('a').attempts).toEqual([])
  })
  it('gives none for nothing held', () => {
    expect(ownedBy<number[]>(null, 'a', [])).toEqual([])
  })
})
