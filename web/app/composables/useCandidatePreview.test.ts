import { describe, expect, it } from 'vitest'
import { ApiError } from './useApi'
import { previewRefusal } from './useCandidatePreview'

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
