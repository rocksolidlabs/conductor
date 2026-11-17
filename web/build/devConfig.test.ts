import { describe, expect, it } from 'vitest'
import { DEFAULT_DEV_API, devApi, devPreviewBridgeSrc } from './devConfig'

describe("the dev server's API target", () => {
  it('is the named loopback server, or the default one', () => {
    expect(devApi(undefined)).toBe(DEFAULT_DEV_API)
    expect(devApi('')).toBe('127.0.0.1:8080')
    for (const v of ['127.0.0.1:41234', 'localhost:3000', '[::1]:65535', '127.0.0.1:1']) expect(devApi(v)).toBe(v)
  })
  it('refuses anything but a loopback host and port', () => {
    for (const v of ['example.com:8080', '192.168.1.5:8080', '127.0.0.1', '127.0.0.1:0', '127.0.0.1:65536', '127.0.0.1:080', 'http://127.0.0.1:8080', '127.0.0.1:8080/api', ' 127.0.0.1:8080', '127.0.0.2:8080', 'localhost.example:80']) {
      expect(() => devApi(v), v).toThrow(/CONDUCTOR_DEV_API must be a loopback host and port/)
    }
  })
})

describe('the bridge the dev build loads', () => {
  it("is a loopback Conductor server's bridge", () => {
    expect(devPreviewBridgeSrc('http://127.0.0.1:4312/preview-bridge.js')).toBe('http://127.0.0.1:4312/preview-bridge.js')
    expect(devPreviewBridgeSrc('http://localhost:4312/preview-bridge.js')).toBe('http://localhost:4312/preview-bridge.js')
    expect(devPreviewBridgeSrc('http://[::1]:4312/preview-bridge.js')).toBe('http://[::1]:4312/preview-bridge.js')
  })
  it('is none when unset, or anything else', () => {
    for (const v of [undefined, '', 'https://example.com/preview-bridge.js', 'http://192.168.1.5:4312/preview-bridge.js', 'http://127.0.0.1:4312/other.js', 'http://127.0.0.1:4312/preview-bridge.js?x=1', 'http://127.0.0.1:4312/preview-bridge.js#x', 'http://me@127.0.0.1:4312/preview-bridge.js', 'javascript:alert(1)', 'data:text/javascript,1', 'HTTP://127.0.0.1:4312/preview-bridge.js', 'not a url']) {
      expect(devPreviewBridgeSrc(v), String(v)).toBe('')
    }
  })
})
