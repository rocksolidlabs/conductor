import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { BRIEF_SWITCH_DESCRIPTION } from './brakemanAlert'
import { PENDING_ELSEWHERE } from './brakemanCards'
import { SHARE_UNCOPIED } from './brakemanShare'
import { ROUTE_KEYS } from './events'
import { NO_TAB_WORDS } from './screenshot'

// Where Conductor is used is an app (the desktop app, the phone), never a browser: the words a person reads outside templates say
// "this device", "on every device" or "from anywhere" (UX3 R86; designRules.test.ts counts the templates). A real browser keeps its
// name (Chromium below); the routing's key `browser` is a key, not a word.

/** A source file of web/app, as text. */
const source = (path: string) => readFileSync(fileURLToPath(new URL(`../${path}`, import.meta.url)), 'utf8')

describe('the words say this device, never this browser', () => {
  it("name the routing's destination This device", () => {
    const dest = ROUTE_KEYS.find((k) => k.key === 'browser')!
    expect(dest).toMatchObject({ label: 'This device', summary: 'Notification on this device', hint: 'A notification on this device and the chime, as switched on under Alerts' })
    for (const k of ROUTE_KEYS) expect(`${k.label} ${k.summary} ${k.hint}`).not.toMatch(/browser/i)
  })

  it("say this device in the brief switch, a make held elsewhere, a copy refused and a capture with no picture", () => {
    expect(BRIEF_SWITCH_DESCRIPTION).toMatch(/^A notification on this device and the chime/)
    expect(PENDING_ELSEWHERE).toBe("Being made: it finishes when the device that asked for it asks again, or at the server's next start.")
    expect(SHARE_UNCOPIED).toBe('The page could not copy: select the text below and copy it by hand.')
    expect(NO_TAB_WORDS).toBe('a picture cropped to the frame cannot be taken here (the desktop app and Chromium can take one), so the capture has no screenshot')
    for (const w of [BRIEF_SWITCH_DESCRIPTION, PENDING_ELSEWHERE, SHARE_UNCOPIED]) expect(w).not.toMatch(/browser/i)
    expect(NO_TAB_WORDS).not.toMatch(/this browser/i)
  })

  it('say this device in the palette, the inbox, Brakeman, the preview and the paste invite', () => {
    const palette = source('composables/usePaletteRows.ts')
    expect(palette).toContain("label: 'Notifications on this device'")
    expect(palette).toContain("browser: 'an alert on this device'")
    expect(palette).not.toContain("'Browser notifications'")
    expect(palette).not.toContain("'a browser alert'")
    expect(source('utils/inbox.ts')).toContain("taken: the owner's reply to this device's request said so")
    expect(source('utils/inbox.ts')).not.toContain("taken: the owner's reply to this browser's request")
    expect(source('composables/useBrakeman.ts')).not.toContain('This browser holds')
    expect(source('composables/usePreviewLabel.ts')).toContain("'If no tab opened, it was held back: press Open preview again.'")
    expect(source('composables/usePreviewLabel.ts')).not.toContain('the browser held it back')
    expect(source('utils/transport/paste.ts')).toContain("'WebRTC is not available here'")
    expect(source('utils/transport/paste.ts')).not.toContain('this browser has no WebRTC')
  })
})
