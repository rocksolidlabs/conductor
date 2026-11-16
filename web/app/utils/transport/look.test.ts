import { describe, expect, it } from 'vitest'
import { transportLook } from './look'

describe('transportLook', () => {
  it('draws a healthy connection as its icon alone, its name and round trip as its label', () => {
    expect(transportLook('ws', 'open', 12)).toEqual({ icon: 'i-lucide-cable', words: 'WebSocket · 12 ms', color: 'neutral', showWords: false })
    expect(transportLook('webrtc', 'open', null)).toMatchObject({ words: 'WebRTC direct', color: 'neutral', showWords: false })
    expect(transportLook('relay', 'open', 40)).toMatchObject({ icon: 'i-lucide-route', words: 'Relay · 40 ms', showWords: false })
  })

  it('keeps the words in view while the connection is not healthy', () => {
    expect(transportLook('ws', 'connecting', null)).toMatchObject({ words: 'connecting', color: 'warning', showWords: true })
    expect(transportLook('webrtc', 'signaling', null)).toMatchObject({ words: 'negotiating WebRTC', showWords: true })
    expect(transportLook('ws', 'closed', 12)).toMatchObject({ words: 'disconnected', color: 'error', showWords: true })
    expect(transportLook('ws', 'idle', null)).toMatchObject({ words: 'idle', showWords: true })
  })

  it('keeps the words of an open connection where the badge is not compact (the Yard, the join page)', () => {
    expect(transportLook('ws', 'open', 12, false)).toMatchObject({ words: 'WebSocket · 12 ms', showWords: true })
  })
})
