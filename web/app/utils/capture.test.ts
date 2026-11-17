import { describe, expect, it } from 'vitest'
import { captureOfWords, diffSinceSent, isWorkbench, labelWords, parseBridgeMessage, previewPath, retakeAddress, sourceLink } from './capture'
import { MAX_ELEMENT_REPORT, MAX_PREVIEW_TO_WORKBENCH, PREVIEW_MARK } from './protocol'
import { testElement, testPage } from './testdata/preview'

// The capture's pure parts (docs/plans/browser-evidence.md): the one gate a bridge's message passes before the workbench reads it,
// the label's words, what changed since a send, the workbench never framed under another spelling, and a source's link.

const NONCE = 'abcdefghijklmnop0123'
const frame = { name: 'the frame' }
const ORIGIN = 'http://127.0.0.1:41000'
const want = { source: frame, origin: ORIGIN, nonce: NONCE }
const looked = (over: Record<string, unknown> = {}) => ({ conductor: PREVIEW_MARK, t: 'looked', nonce: NONCE, ...testPage(), ...over })
const heard = (data: unknown, over: Partial<{ source: unknown; origin: string; isTrusted: boolean }> = {}) => ({ source: frame, origin: ORIGIN, data, isTrusted: true, ...over })

describe('parseBridgeMessage: only the frame, at the preview origin, with the hello nonce, within its bounds', () => {
  it('takes an answer of the frame at the preview origin carrying the nonce', () => {
    expect(parseBridgeMessage(heard(looked()), want)?.t).toBe('looked')
    const selected = { conductor: PREVIEW_MARK, t: 'selected', nonce: NONCE, element: testElement(), ...testPage() }
    expect(parseBridgeMessage(heard(selected), want)?.t).toBe('selected')
  })

  it('drops a message from another window: a popup, a nested frame, the workbench itself, or none', () => {
    for (const source of [{ name: 'a popup' }, { name: 'a nested frame' }, null, undefined, globalThis]) expect(parseBridgeMessage(heard(looked(), { source }), want)).toBeNull()
    // And when the frame is gone (no window to match), nothing is taken at all.
    expect(parseBridgeMessage(heard(looked(), { source: null }), { ...want, source: null })).toBeNull()
  })

  it('drops the right window at another origin: the frame navigated elsewhere, an opaque origin, another port', () => {
    for (const origin of ['http://127.0.0.1:41001', 'http://localhost:41000', 'https://127.0.0.1:41000', 'null', '']) expect(parseBridgeMessage(heard(looked(), { origin }), want)).toBeNull()
    expect(parseBridgeMessage(heard(looked(), { origin: 'null' }), { ...want, origin: 'null' })).toBeNull()
  })

  it('drops a message without the nonce, with another, or before any hello', () => {
    expect(parseBridgeMessage(heard(looked({ nonce: 'zzzzzzzzzzzzzzzzzzzz' })), want)).toBeNull()
    const { nonce: _, ...bare } = looked()
    expect(parseBridgeMessage(heard(bare), want)).toBeNull()
    expect(parseBridgeMessage(heard(looked()), { ...want, nonce: '' })).toBeNull()
  })

  it('drops an event the browser did not deliver', () => {
    expect(parseBridgeMessage(heard(looked(), { isTrusted: false }), want)).toBeNull()
  })

  it('drops a message past 64 KiB, and one whose report is past 32 KiB', () => {
    const big = { conductor: PREVIEW_MARK, t: 'selected', nonce: NONCE, element: testElement(), ...testPage({ title: 'x'.repeat(10) }) }
    expect(parseBridgeMessage(heard(big), want)).not.toBeNull()
    // Past the message's bound: a described whose fields are each within theirs.
    const past = { ...big, element: testElement({ neighbourhood: 'n'.repeat(24 * 1024), html: 'h'.repeat(12 * 1024) }) }
    expect(JSON.stringify(past.element).length).toBeGreaterThan(MAX_ELEMENT_REPORT)
    expect(parseBridgeMessage(heard(past), want)).toBeNull()
    const huge = { ...looked(), url: 'u'.repeat(MAX_PREVIEW_TO_WORKBENCH) }
    expect(parseBridgeMessage(heard(huge), want)).toBeNull()
  })

  it('drops an unknown type, a message to the bridge, a wrong mark, a member the type does not carry', () => {
    expect(parseBridgeMessage(heard(looked({ t: 'opened' })), want)).toBeNull()
    expect(parseBridgeMessage(heard({ conductor: PREVIEW_MARK, t: 'select', nonce: NONCE }), want)).toBeNull()
    expect(parseBridgeMessage(heard({ conductor: PREVIEW_MARK, t: 'hello', nonce: NONCE }), want)).toBeNull()
    expect(parseBridgeMessage(heard(looked({ conductor: 'preview/2' })), want)).toBeNull()
    expect(parseBridgeMessage(heard(looked({ html: '<img src=x onerror=alert(1)>' })), want)).toBeNull()
    expect(parseBridgeMessage(heard('looked'), want)).toBeNull()
  })
})

describe('labelWords: the label line', () => {
  const where = { task: 'build', attempt: 2 }
  it('names the attempt, the worktree commit and its changed files, and that it changes as the agent works', () => {
    expect(labelWords({ tree: 'worktree', commit: 'a1b2c3d4e5f6', changed: 3 }, where)).toBe('attempt 2 of build · worktree at a1b2c3d with 3 changed files · changes as the agent works')
    expect(labelWords({ tree: 'worktree', commit: 'a1b2c3d4e5f6', changed: 1 }, where)).toBe('attempt 2 of build · worktree at a1b2c3d with 1 changed file · changes as the agent works')
    expect(labelWords({ tree: 'worktree', commit: 'a1b2c3d4e5f6', changed: 0 }, where)).toBe('attempt 2 of build · worktree at a1b2c3d with no changed file · changes as the agent works')
    expect(labelWords({ tree: 'worktree', commit: 'a1b2c3d4e5f6', changed: 0, unread: 'late' }, where)).toBe('attempt 2 of build · worktree at a1b2c3d its changes not read: late · changes as the agent works')
  })
  it("names a candidate's checkout as committed, a link as no revision, and a label not read yet", () => {
    expect(labelWords({ tree: 'preview', commit: 'a1b2c3d4e5f6', changed: 0 }, where)).toBe('candidate a1b2c3d of build · attempt 2 · as committed')
    expect(labelWords(null, null)).toBe('a link · no revision')
    expect(labelWords(null, where)).toBe('reading the revision…')
  })
})

describe('diffSinceSent: what changed since a send froze the capture', () => {
  const el = (selector: string, x: number) => ({ kind: 'element' as const, note: '', rect: { x, y: 0, width: 10, height: 10 }, element: { selector, box: { x, y: 0, width: 10, height: 10 } } })
  const region = (x: number, note = '') => ({ kind: 'region' as const, note, rect: { x, y: 5, width: 20, height: 20 } })
  const sent = { rev: 2, annotations: [el('#a', 0), el('#b', 20), region(40, 'here')] }
  it('says nothing for the revision sent', () => {
    expect(diffSinceSent(sent, sent)).toEqual([])
  })
  it("names a note changed by the annotation's place as sent", () => {
    expect(diffSinceSent({ rev: 3, annotations: [el('#a', 0), el('#b', 20), region(40, 'there')] }, sent)).toEqual(["annotation 3's note"])
  })
  it('names a region added, an annotation removed and one moved', () => {
    expect(diffSinceSent({ rev: 3, annotations: [...sent.annotations, region(90)] }, sent)).toEqual(['a region added'])
    expect(diffSinceSent({ rev: 3, annotations: [el('#a', 0), region(40, 'here')] }, sent)).toEqual(['annotation 2 removed'])
    expect(diffSinceSent({ rev: 3, annotations: [el('#b', 20), el('#a', 0), region(40, 'here')] }, sent)).toEqual(['annotation 2 moved', 'annotation 1 moved'])
  })
})

describe('isWorkbench: the pane never frames the workbench, under any spelling', () => {
  it('sees its own origin, a loopback name at its port, and an address it names', () => {
    const wb = 'http://127.0.0.1:8080'
    for (const u of ['http://127.0.0.1:8080/', 'http://127.0.0.1:8080/other/path?x#y', 'http://localhost:8080/', 'http://[::1]:8080/', 'http://127.1.2.3:8080/', 'http://LOCALHOST:8080/']) expect(isWorkbench(u, wb), u).toBe(true)
    expect(isWorkbench('https://127.0.0.1:8443/', wb, ['https://127.0.0.1:8443'])).toBe(true)
    // The IPv4 loopback mapped into IPv6, as a URL writes it and as it is typed.
    expect(isWorkbench('http://[::ffff:7f00:1]:8080/', wb)).toBe(true)
    expect(isWorkbench('http://[::ffff:127.0.0.1]:8080/', wb)).toBe(true)
    expect(isWorkbench('http://[::ffff:7f00:1]:41000/', wb)).toBe(false)
  })
  it('frames a preview at another port, and an address that does not parse is no workbench', () => {
    expect(isWorkbench('http://127.0.0.1:41000/', 'http://127.0.0.1:8080')).toBe(false)
    expect(isWorkbench('http://localhost:41000/', 'http://localhost:8080')).toBe(false)
    expect(isWorkbench('not a url', 'http://127.0.0.1:8080')).toBe(false)
  })
})

describe('sourceLink: a link only for a found, relative path in the tree', () => {
  it('links a found relative path at its line', () => {
    expect(sourceLink({ path: 'web/app/components/SaveBar.vue', found: true, line: 12 })).toEqual({ path: 'web/app/components/SaveBar.vue', line: 12 })
  })
  it('links nothing outside the tree, not found, absolute, climbing, with a scheme or a control character', () => {
    for (const s of [
      { path: 'a.vue', found: false },
      { path: 'a.vue', found: true, outside: true },
      { path: '/etc/passwd', found: true },
      { path: '../a.vue', found: true },
      { path: 'a/../../b.vue', found: true },
      { path: 'javascript:alert(1)', found: true },
      { path: 'a\\b.vue', found: true },
      { path: 'a\nb.vue', found: true },
      { path: '', found: true },
    ])
      expect(sourceLink(s), s.path).toBeNull()
  })
})

describe('previewPath', () => {
  it('is the preview page with the attempt, and its seq for a checkout', () => {
    expect(previewPath({ run: 'r-12345678', task: 'build', attempt: 2 })).toBe('/preview?run=r-12345678&task=build&attempt=2')
    expect(previewPath({ run: 'r-12345678', task: 'build', attempt: 2, seq: 3 })).toBe('/preview?run=r-12345678&task=build&attempt=2&seq=3')
  })
})

describe('retakeAddress: Capture again opens the same page on this preview, never elsewhere', () => {
  it("keeps the capture's path and query on the preview's origin, with the preview's own fragment", () => {
    expect(retakeAddress('http://127.0.0.1:41000/settings?tab=2#token=${CONDUCTOR_TREE_SECRET}', 'http://127.0.0.1:42000/#token=abc')).toBe('http://127.0.0.1:42000/settings?tab=2#token=abc')
  })
  it("never takes the capture's origin, and frames nothing for an address that does not parse", () => {
    expect(retakeAddress('https://elsewhere.example:443/x', 'http://127.0.0.1:42000/')).toBe('http://127.0.0.1:42000/x')
    expect(retakeAddress('javascript:alert(1)', 'http://127.0.0.1:42000/')).toBe('')
    // A path that reads as another host is refused, not followed there.
    expect(retakeAddress('http://127.0.0.1:41000//elsewhere.example/x', 'http://127.0.0.1:42000/')).toBe('')
    expect(retakeAddress('not a url', 'http://127.0.0.1:42000/')).toBe('')
    expect(retakeAddress('http://127.0.0.1:41000/', 'file:///etc/passwd')).toBe('')
  })
})

describe('captureOfWords', () => {
  it('says what a sent capture is of: an attempt’s worktree, a candidate’s checkout, a link’s page', () => {
    const commit = '0123456789abcdef0123456789abcdef01234567'
    expect(captureOfWords({ kind: 'attempt', task: 'build', attempt: 1, tree: 'worktree', commit, changed: 2 })).toEqual({ lead: 'build attempt 1 · worktree at', commit: '0123456', tail: '· 2 changed' })
    expect(captureOfWords({ kind: 'candidate', task: 'build', attempt: 2, tree: 'preview', commit, changed: 0 })).toEqual({ lead: 'candidate', commit: '0123456', tail: 'of build · attempt 2 · as committed' })
    expect(captureOfWords({ kind: 'url', changed: 0 })).toEqual({ lead: 'a link', commit: '', tail: '' })
  })
})
