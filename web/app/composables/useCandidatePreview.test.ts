import { describe, expect, it } from 'vitest'
import { ApiError } from './useApi'
import { checkoutOfAttempt, openCheckouts, previewRefusal } from './useCandidatePreview'

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
