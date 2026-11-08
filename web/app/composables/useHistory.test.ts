import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from './useApi'
import { RESUME_PENDING_EVERY_MS, useHistory } from './useHistory'
import { CommandRefused } from '../utils/tasks'

// A resume from a record (Program item HS1, PR-6) is a command with a receipt: a reply lost to the network is asked for once more
// with the same command id, never a second one, and a second loss is the error; a refusal carries its receipt; a resume asked again
// is a fresh id. useHistory reads useApi as a Nuxt auto-import: it is stubbed here.

const request = vi.fn()

beforeEach(() => {
  request.mockReset()
  vi.stubGlobal('useApi', () => ({ request }))
})
afterEach(() => vi.unstubAllGlobals())

const lost = () => new ApiError(0, 'network', 'Cannot reach the server')
const started = { session: { id: 'fedcba9876543210' }, resumed: true, receipt: { commandId: 'x', state: 'started', session: 'fedcba9876543210', at: '' } }

describe('resumeRecord', () => {
  it('sends a lost reply again with the same command id, once', async () => {
    request.mockRejectedValueOnce(lost()).mockResolvedValueOnce(started)
    const r = await useHistory().resumeRecord('0123456789abcdef')
    expect(r.session?.id).toBe('fedcba9876543210')
    expect(request).toHaveBeenCalledTimes(2)
    const [a, b] = request.mock.calls.map((c) => c[1].body)
    expect(a.commandId).toMatch(/^[0-9a-f]{16}$/)
    expect(b).toEqual(a)
    expect(request).toHaveBeenNthCalledWith(1, '/api/history/sessions/0123456789abcdef/resume', expect.objectContaining({ method: 'POST' }))
  })

  it('is the error after a second loss, with no third send and never a second id', async () => {
    request.mockRejectedValueOnce(lost()).mockRejectedValueOnce(lost())
    await expect(useHistory().resumeRecord('0123456789abcdef')).rejects.toMatchObject({ status: 0 })
    expect(request).toHaveBeenCalledTimes(2)
    expect(new Set(request.mock.calls.map((c) => c[1].body.commandId)).size).toBe(1)
  })

  it('reads a refusal from its receipt, its code kept, and sends it once', async () => {
    const receipt = { commandId: 'x', state: 'failed', code: 'env_not_kept', error: 'its launch had API_URL', at: '' }
    request.mockRejectedValueOnce(new ApiError(409, 'env_not_kept', 'its launch had API_URL', {}, { error: { code: 'env_not_kept', message: 'its launch had API_URL' }, receipt }))
    const err = await useHistory().resumeRecord('0123456789abcdef').catch((e) => e)
    expect(err).toBeInstanceOf(CommandRefused)
    expect((err as CommandRefused).message).toBe('its launch had API_URL')
    expect((err as CommandRefused<typeof receipt>).receipt.code).toBe('env_not_kept')
    expect(request).toHaveBeenCalledTimes(1)
  })

  it('asks a pending resume again with the same id until its receipt settles, then answers the session it started', async () => {
    vi.useFakeTimers()
    try {
      const pending = () => new ApiError(409, 'resume_pending', 'this resume is being started; ask again', {}, { error: { code: 'resume_pending', message: 'this resume is being started; ask again' }, receipt: { commandId: 'x', state: 'pending', at: '' } })
      request.mockRejectedValueOnce(lost()).mockRejectedValueOnce(pending()).mockRejectedValueOnce(pending()).mockResolvedValueOnce({ receipt: started.receipt, session: started.session })
      const p = useHistory().resumeRecord('0123456789abcdef')
      await vi.advanceTimersByTimeAsync(2 * RESUME_PENDING_EVERY_MS)
      const r = await p
      expect(r.session?.id).toBe('fedcba9876543210')
      expect(request).toHaveBeenCalledTimes(4)
      expect(new Set(request.mock.calls.map((c) => c[1].body.commandId)).size).toBe(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('asks a pending resume no more once the page that asked it is gone, the send made kept', async () => {
    vi.useFakeTimers()
    try {
      const pending = () => new ApiError(409, 'resume_pending', 'this resume is being started; ask again', {}, { error: { code: 'resume_pending', message: 'this resume is being started; ask again' }, receipt: { commandId: 'x', state: 'pending', at: '' } })
      request.mockRejectedValue(pending())
      const life = new AbortController()
      const p = useHistory()
        .resumeRecord('0123456789abcdef', { signal: life.signal })
        .catch((e) => e)
      await vi.advanceTimersByTimeAsync(RESUME_PENDING_EVERY_MS / 2)
      expect(request).toHaveBeenCalledTimes(1)
      // The page leaves while it waits to ask again: the wait ends at once, nothing more is sent, and the resume ends with the reason.
      life.abort(new DOMException('The page was left', 'AbortError'))
      const err = await p
      expect((err as DOMException).name).toBe('AbortError')
      await vi.advanceTimersByTimeAsync(5 * RESUME_PENDING_EVERY_MS)
      expect(request).toHaveBeenCalledTimes(1)
      // Its flags went with it, never the signal.
      expect(request.mock.calls[0]![1].body).toEqual({ commandId: request.mock.calls[0]![1].body.commandId })
    } finally {
      vi.useRealTimers()
    }
  })

  it('answers an uncertain receipt as a CommandRefused with its state, asked once', async () => {
    const receipt = { commandId: 'x', state: 'uncertain', error: 'the server stopped while starting the session', at: '' }
    request.mockRejectedValueOnce(new ApiError(409, 'resume_uncertain', receipt.error, {}, { error: { code: 'resume_uncertain', message: receipt.error }, receipt }))
    const err = await useHistory().resumeRecord('0123456789abcdef').catch((e) => e)
    expect((err as CommandRefused<typeof receipt>).receipt.state).toBe('uncertain')
    expect(request).toHaveBeenCalledTimes(1)
  })

  it('mints a fresh id for each resume asked, with the flags it is asked with', async () => {
    request.mockResolvedValue(started)
    const h = useHistory()
    await h.resumeRecord('0123456789abcdef')
    await h.resumeRecord('0123456789abcdef', { withoutEnv: true, confirmLost: true })
    const [a, b] = request.mock.calls.map((c) => c[1].body)
    expect(a.commandId).not.toBe(b.commandId)
    expect(a).toEqual({ commandId: a.commandId })
    expect(b).toEqual({ commandId: b.commandId, withoutEnv: true, confirmLost: true })
  })
})

describe('the record routes', () => {
  it('reads a record, an entry and a command by its actor, and forgets a record', async () => {
    request.mockResolvedValue({ entry: { key: 'session:a' }, command: { id: 'c1' } })
    const h = useHistory()
    await h.record('0123456789abcdef')
    expect(request).toHaveBeenLastCalledWith('/api/history/sessions/0123456789abcdef')
    expect((await h.entry('session:a')).key).toBe('session:a')
    expect(request).toHaveBeenLastCalledWith('/api/history/entry', { query: { key: 'session:a' } })
    expect((await h.command('todo-1', 'admin', 'c1')).id).toBe('c1')
    expect(request).toHaveBeenLastCalledWith('/api/history/command', { query: { run: 'todo-1', actor: 'admin', id: 'c1' } })
    await h.forgetRecord('0123456789abcdef')
    expect(request).toHaveBeenLastCalledWith('/api/history/sessions/0123456789abcdef', { method: 'DELETE' })
  })
})
