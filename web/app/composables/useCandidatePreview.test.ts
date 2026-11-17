import { describe, expect, it } from 'vitest'
import { ApiError } from './useApi'
import { OPENING_WAIT_MS, checkoutOfAttempt, openCheckouts, previewRefusal, sendCommand } from './useCandidatePreview'

// The words of a refused preview command (docs/protocol.md, the preview commands): the server's own, and for two checkouts open
// already the ones open, read from the reply beside its error.

describe('previewRefusal', () => {
  it("names the checkouts open when the server has two already", () => {
    const body = { error: { code: 'previews_busy', message: 'two previews are open' }, receipt: { refusal: 'previews_busy' }, open: [{ run: 'r', task: 'build', attempt: 2, seq: 1 }, { run: 'r', task: 'test', attempt: 1, seq: 3 }] }
    expect(previewRefusal(new ApiError(429, 'previews_busy', 'two previews are open', body.error, body))).toBe("Two candidates' previews are open already (build attempt 2 and test attempt 1): close one first.")
  })
  it("says any other refusal in the server's words", () => {
    expect(previewRefusal(new ApiError(409, 'not_candidate', 'attempt 1 of build submitted no commit'))).toBe('attempt 1 of build submitted no commit')
  })
})

describe('the checkouts open, to open or close', () => {
  it('are those a previews_busy names, for any other refusal none', () => {
    const open = [{ run: 'r', task: 'build', attempt: 2, seq: 1 }, { run: 's', task: 'test', attempt: 1, seq: 3 }]
    const body = { error: { code: 'previews_busy', message: 'two previews are open' }, open: [...open, { run: 7 }] }
    expect(openCheckouts(new ApiError(429, 'previews_busy', 'two previews are open', body.error, body))).toEqual(open)
    expect(openCheckouts(new ApiError(409, 'preview_open', "the candidate's preview is open already"))).toEqual([])
  })

  it("are a candidate's, the live run's current attempt or, for an older one or a run not held, the task route's read of it", () => {
    const reads = [{ n: 1, previewing: { seq: 4, state: 'ready' } }, { n: 2 }]
    expect(checkoutOfAttempt(1, { n: 2 }, reads)).toEqual({ seq: 4, state: 'ready' })
    expect(checkoutOfAttempt(2, { n: 2, previewing: { seq: 5, state: 'resolved' } }, reads)).toEqual({ seq: 5, state: 'resolved' })
    expect(checkoutOfAttempt(1, undefined, reads)).toEqual({ seq: 4, state: 'ready' })
    expect(checkoutOfAttempt(2, { n: 2 }, reads)).toBeNull()
    expect(checkoutOfAttempt(1, undefined, [{ n: 1, previewing: { seq: 4, state: 'ending' } }])).toBeNull()
  })
})

// A command asked again is the same command: the same body, its command id. A reply lost to the network is asked for once more; an
// open the server says is still under way (409 preview_opening) is asked for again a moment apart until it answers with its seq, a
// bounded number of times; every other refusal is the server's own, at once.
describe('a command sent', () => {
  const opening = () => new ApiError(409, 'preview_opening', "the candidate's preview is still opening")
  it('is asked again while its open is under way, and answers with the seq when the open has one', async () => {
    const waits: number[] = []
    let asked = 0
    const out = await sendCommand(async () => {
      asked++
      if (asked === 1) throw new ApiError(0, 'network', 'the reply was lost')
      if (asked < 4) throw opening()
      return { seq: 3 }
    }, async (ms) => void waits.push(ms))
    expect(out).toEqual({ seq: 3 })
    expect(asked).toBe(4)
    expect(waits).toEqual([OPENING_WAIT_MS, OPENING_WAIT_MS])
  })
  it('gives up a still-opening open after its tries, in the server\'s words, and asks a lost reply only once more', async () => {
    let asked = 0
    await expect(sendCommand(async () => { asked++; throw opening() }, async () => {}, 2)).rejects.toMatchObject({ code: 'preview_opening' })
    expect(asked).toBe(3)
    asked = 0
    await expect(sendCommand(async () => { asked++; throw new ApiError(0, 'network', 'lost') }, async () => {})).rejects.toMatchObject({ status: 0 })
    expect(asked).toBe(2)
  })
  it('throws any other refusal at once', async () => {
    let asked = 0
    await expect(sendCommand(async () => { asked++; throw new ApiError(409, 'preview_open', 'open already') }, async () => {})).rejects.toMatchObject({ code: 'preview_open' })
    expect(asked).toBe(1)
  })
})
