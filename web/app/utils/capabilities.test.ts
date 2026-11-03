import { describe, expect, it } from 'vitest'
import { anyStarting, asCapabilities, extensionsWords, has, NO_EXTENSIONS, type Capabilities } from './capabilities'

describe('extensionsWords', () => {
  it('says the open-source feature set for none', () => {
    expect(extensionsWords({ providers: [] })).toBe('None: the open-source feature set')
    expect(NO_EXTENSIONS).toBe('None: the open-source feature set')
  })

  it("names each provider by kind and name, in the server's order", () => {
    const caps: Capabilities = {
      providers: [
        { kind: 'identity', name: 'cloud', state: 'ready' },
        { kind: 'notifications', name: 'push', state: 'ready' },
        { kind: 'media', name: 'cloud', state: 'ready' },
      ],
    }
    expect(extensionsWords(caps)).toBe('identity: cloud · notifications: push · media: cloud')
  })

  it('marks a provider whose start failed, and is tried again, as starting', () => {
    const caps: Capabilities = {
      providers: [
        { kind: 'identity', name: 'cloud', state: 'starting' },
        { kind: 'notifications', name: 'push', state: 'ready' },
      ],
    }
    expect(extensionsWords(caps)).toBe('identity: cloud (starting) · notifications: push')
  })
})

describe('has', () => {
  const caps: Capabilities = {
    providers: [
      { kind: 'identity', name: 'cloud', state: 'starting' },
      { kind: 'notifications', name: 'push', state: 'ready' },
    ],
  }

  it('is true for a kind with a provider ready', () => {
    expect(has(caps, 'notifications')).toBe(true)
  })

  it('is false for a kind still starting, a kind not there and no reply', () => {
    expect(has(caps, 'identity')).toBe(false)
    expect(has(caps, 'context')).toBe(false)
    expect(has({ providers: [] }, 'identity')).toBe(false)
    expect(has(null, 'identity')).toBe(false)
    expect(has(undefined, 'identity')).toBe(false)
  })

  it('is true once any of a kind is ready', () => {
    expect(has({ providers: [{ kind: 'context', name: 'a', state: 'starting' }, { kind: 'context', name: 'b', state: 'ready' }] }, 'context')).toBe(true)
  })
})

describe('anyStarting', () => {
  it('is true while a provider is starting, false for none and for no reply', () => {
    expect(anyStarting({ providers: [{ kind: 'sync', name: 'cloud', state: 'starting' }] })).toBe(true)
    expect(anyStarting({ providers: [{ kind: 'sync', name: 'cloud', state: 'ready' }] })).toBe(false)
    expect(anyStarting({ providers: [] })).toBe(false)
    expect(anyStarting(null)).toBe(false)
  })
})

describe('asCapabilities', () => {
  it('keeps the rows as the server sends them, a kind added later among them', () => {
    expect(asCapabilities({ providers: [{ kind: 'runtime', name: 'boxcar', state: 'ready' }] })).toEqual({ providers: [{ kind: 'runtime', name: 'boxcar', state: 'ready' }] })
  })

  it('takes a state it does not know for starting, and drops a row it cannot show', () => {
    expect(
      asCapabilities({
        providers: [{ kind: 'identity', name: 'cloud', state: 'warming' }, { kind: 'identity' }, null, 'x', { kind: '', name: 'a', state: 'ready' }],
      }),
    ).toEqual({ providers: [{ kind: 'identity', name: 'cloud', state: 'starting' }] })
  })

  it('reads a body with no list as none', () => {
    expect(asCapabilities({})).toEqual({ providers: [] })
    expect(asCapabilities(null)).toEqual({ providers: [] })
  })
})
