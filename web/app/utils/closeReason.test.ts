import { describe, expect, it } from 'vitest'
import { closedByTheEnd, closeReason, CloseCode } from './protocol'

describe('what a closed connection says', () => {
  it('tells a link that expired from one that was revoked, both closed with 4403', () => {
    expect(closeReason(CloseCode.Forbidden, 'link expired')).toBe('This share link expired')
    expect(closeReason(CloseCode.Forbidden, 'link revoked')).toBe('This share link was revoked')
    expect(closeReason(CloseCode.Forbidden, '')).toBe('This share link was revoked')
  })
  it('names no link for a principal the identity provider no longer admits, closed with 4403 too', () => {
    expect(closeReason(CloseCode.Forbidden, 'access revoked')).toBe('Your access to this session was revoked')
    expect(closedByTheEnd(CloseCode.Forbidden, 'access revoked')).toBe(false)
  })
  it('names the other closes as before', () => {
    expect(closeReason(CloseCode.Unauthorized, '')).toBe('Not authorized for this session')
    expect(closeReason(CloseCode.SessionEnded, 'host disconnected')).toBe('host disconnected')
    expect(closeReason(CloseCode.SessionEnded, '')).toBe('Session ended')
    expect(closeReason(1006, '')).toBe('Connection lost')
    expect(closeReason(4999, '')).toBe('Connection closed (4999)')
  })
})

describe('a close that says the session ended', () => {
  it('is 4410 session ended, which a link viewer of a server or a hosted session gets as the session ends, after its last output', () => {
    expect(closedByTheEnd(CloseCode.SessionEnded, 'session ended')).toBe(true)
    expect(closeReason(CloseCode.SessionEnded, 'session ended')).toBe('session ended')
  })
  it('is neither a host that went away nor a link revoked or expired', () => {
    expect(closedByTheEnd(CloseCode.SessionEnded, 'host disconnected')).toBe(false)
    expect(closedByTheEnd(CloseCode.Forbidden, 'link revoked')).toBe(false)
    expect(closedByTheEnd(CloseCode.Forbidden, 'link expired')).toBe(false)
    expect(closedByTheEnd(1006, '')).toBe(false)
  })
})
