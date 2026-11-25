import { describe, expect, it } from 'vitest'
import { hostOf, reuseHeld, shareDescription, shareReach, shareRefusal } from './share'

const offline = { mode: 'off' as const, mapped: false }

describe('shareReach', () => {
  it('a link minted at the switchyard works from anywhere', () => {
    const r = shareReach({ url: 'https://switchyard.example.net/join/abc', remote: true }, offline)
    expect(r.kind).toBe('remote')
    expect(r.title).toBe('Works from anywhere')
    expect(r.text).toContain('switchyard.example.net, which mints and checks the link')
    expect(r.text).toContain("through the switchyard's relay when no direct path forms")
  })
  it('a link made here while a switchyard is configured says why the session is not there, then what it reaches', () => {
    const r = shareReach({ url: 'http://127.0.0.1:8080/join/abc', rendezvous: { server: 'https://switchyard.example.net', error: 'connection refused' } }, offline)
    expect(r.kind).toBe('unpublished')
    expect(r.title).toBe('Not published to the switchyard')
    expect(r.text).toContain('switchyard.example.net: connection refused')
    expect(r.text).toContain('works where this machine is reachable')
    expect(r.level).toBe('local')
    // A chat on a switchyard older than the server: the server's sentence, once (docs/plans/chat-or-terminal.md, design 13).
    const chat = shareReach({ url: 'http://127.0.0.1:8080/join/abc', rendezvous: { server: 'https://switchyard.example.net', error: 'Chats are not published: the switchyard is older than this server.' } }, offline)
    expect(chat.text).toContain('switchyard.example.net: Chats are not published: the switchyard is older than this server. This link works')
  })
  it('a local link keeps the reach report, and a crew link says it is made here', () => {
    const r = shareReach({ url: 'http://192.168.1.5:8080/join/abc' }, offline)
    expect(r.kind).toBe('local')
    expect(r.title).toBe('Works on your network only')
    expect(shareReach({ url: 'http://192.168.1.5:8080/join/abc' }, offline, true).text).toBe(shareReach({ url: 'http://192.168.1.5:8080/join/abc' }, offline).text)
    expect(shareReach({ url: 'https://switchyard.example.net/join/abc', remote: true }, offline, true).text).toMatch(/opens every agent of the crew there/)
    expect(shareReach({ url: 'https://home.example.net/join/abc' }, null).title).toBe('Reachable from outside your network')
  })
  it('reuses the link made for this target while it is listed, unless the switchyard ended the publication since', () => {
    const made = { linkId: 'l1' }
    const quiet = { republish: false, published: true }
    expect(reuseHeld(made, true, [{ id: 'l1' }], quiet)).toBe(true)
    expect(reuseHeld(made, true, [{ id: 'l1' }], { republish: true, published: false })).toBe(false)
    expect(reuseHeld(made, true, [{ id: 'l1', revoked: true }], quiet)).toBe(false)
    expect(reuseHeld(made, true, [{ id: 'l2' }], quiet)).toBe(false)
    expect(reuseHeld(made, false, [{ id: 'l1' }], quiet)).toBe(false)
    expect(reuseHeld(null, true, [{ id: 'l1' }], quiet)).toBe(false)
  })
  it('makes a link anew in place of one made here while the session was not at the switchyard, once it is', () => {
    const fallback = { linkId: 'l1', rendezvous: { server: 'https://switchyard.example.net', error: 'connection refused' } }
    expect(reuseHeld(fallback, true, [{ id: 'l1' }], { republish: false, published: false })).toBe(true)
    expect(reuseHeld(fallback, true, [{ id: 'l1' }], { republish: false, published: true })).toBe(false)
  })
  it('hostOf', () => {
    expect(hostOf('https://switchyard.example.net/join/x')).toBe('switchyard.example.net')
    expect(hostOf('nonsense')).toBe('nonsense')
  })
})

describe('shareRefusal', () => {
  it('a run kept only as its record: why no link is made, and where its selection is answered', () => {
    const r = shareRefusal('run_record')!
    expect(r.title).toBe('No link for this run')
    expect(r.text).toContain('The server keeps this run only as its record')
    expect(r.text).toContain('A link opens only a run the server holds, so none was made.')
    expect(r.text).toContain('answered on Needs you, not through a link')
    expect(r.text).not.toMatch(/copied|no such run/i)
  })
  it('any other failure is no refusal of this kind: the dialog shows it as the error it is', () => {
    for (const code of ['not_found', 'too_many_links', 'switchyard_too_old', 'rendezvous_unavailable', '']) expect(shareRefusal(code)).toBeNull()
  })
})

describe('shareDescription', () => {
  it('says the link is copied only once one was made and the copy took', () => {
    expect(shareDescription({ run: true, copied: true, shown: true, refused: false })).toBe('The link is copied: it opens every agent of the crew, one added later too. Revoking disconnects whoever used it.')
    expect(shareDescription({ run: false, copied: true, shown: true, refused: false })).toBe('The link is copied. Revoking disconnects whoever used it.')
  })
  it('while a link is made, or when making or copying it failed, says what a link opens and nothing of a copy', () => {
    for (const run of [true, false]) expect(shareDescription({ run, copied: false, shown: true, refused: false })).not.toMatch(/copied/i)
    expect(shareDescription({ run: true, copied: false, shown: false, refused: false })).toBe('A link opens every agent of the crew, one added later too. Revoking disconnects whoever used it.')
  })
  it('a link copied and then revoked is shown no more, and the line says nothing of a copy', () => {
    for (const run of [true, false]) expect(shareDescription({ run, copied: true, shown: false, refused: false })).not.toMatch(/copied/i)
    expect(shareDescription({ run: true, copied: true, shown: false, refused: false })).toBe('A link opens every agent of the crew, one added later too. Revoking disconnects whoever used it.')
  })
  it('refused, says no link was made, whatever was copied before', () => {
    expect(shareDescription({ run: true, copied: false, shown: false, refused: true })).toBe('No link was made for this run.')
    expect(shareDescription({ run: true, copied: true, shown: true, refused: true })).toBe('No link was made for this run.')
    expect(shareDescription({ run: false, copied: false, shown: false, refused: true })).toBe('No link was made.')
  })
})
