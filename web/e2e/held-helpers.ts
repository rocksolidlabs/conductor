import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Browser, BrowserContext, Locator, Page } from '@playwright/test'
import { type Api, expect, test, type Session } from './fixtures'

// A turn held for the terminal (held.spec.ts, and the decision page's spec, which shows one): a guest's line typed into the
// session through a control link and the link revoked before its Enter. Moved here from held.spec.ts as it was, so that two specs
// make one the same way; `baseURL` is the server the guest joins when it is not the suite's (a spec's own server).

export const HELD = 'Text from a link that was let go was typed and not entered: type in the terminal to continue'

/** The stub's files for the agent session of s: each read as it is now, '' before it is there. */
export function stubFiles(home: string, s: Session) {
  const path = (ext: string) => join(home, '.stub-sessions', `${s.agentSession?.id ?? ''}${ext}`)
  const file = (ext: string) => () => (existsSync(path(ext)) ? readFileSync(path(ext), 'utf8') : '')
  /** The whole lines of a file the stub appends to (a last one still being written left out). */
  const records = (ext: string) => {
    const log = file(ext)()
    return log.slice(0, log.lastIndexOf('\n') + 1).split('\n').slice(0, -1)
  }
  /** The last whole record of a file the stub appends one to as what it says changes: what is now ('' before any). */
  const now = (ext: string) => () => records(ext).at(-1) ?? ''
  const typed = file('.txt')
  return {
    /** Whether the stub reads as a TUI (its line's file is there). */
    tui: () => existsSync(path('.line')),
    /** Ends the stub's spin at its next dot: its screen goes quiet, and a line entered meanwhile is answered. */
    stopSpin: () => writeFileSync(path('.spin'), 'stop\n'),
    /** The stub's transcript: the lines entered, one a line. */
    typedLines: typed,
    /** Whether the lines entered hold line, the whole of one. */
    entered: (line: string) => typed().split('\n').includes(line),
    /** The line typed into the stub and not yet entered. */
    pending: now('.line'),
    /** A mark in the line's records, to look past: how many there are now. */
    lineMark: () => records('.line').length,
    /** Whether the line has held anything since mark (an Enter since may have taken it). */
    typedSince: (mark: number) => records('.line').slice(mark).some((r) => r !== ''),
    /** How many mouse reports the stub has read. */
    mice: () => Number(now('.mouse')() || '0'),
    /** "spinning" while the stub draws, "done" once it is. */
    spin: () => file('.spin')().trim(),
    /** The Enters the stub has taken, oldest first: when (ms since the epoch, on this machine's clock) and the line's text. */
    enters: () =>
      records('.entered').map((l) => {
        const sp = l.indexOf(' ')
        return { at: Number(l.slice(0, sp)) / 1000, line: l.slice(sp + 1) }
      }),
  }
}

/** What holdOne returns: the session, the owner's view's reply bar and terminal, the guest's context to close, the stub's files. */
export type Held = { s: Session; bar: Locator; terminal: Locator; guestCtx: BrowserContext; stub: ReturnType<typeof stubFiles> }

/**
 * How holdOne's guest types its line: as a reply to the question with no choices (`Which name?`) from its reply bar, with the
 * owner's reply when set; or, with chat, while a question with choices (`Which way?`: Left `l`, Right `r`) shows, as a line its
 * chat sends to the agent.
 */
export type HoldHow = { reply?: string; chat?: boolean; baseURL?: string }

/**
 * holdOne brings a session to the held turn: a guest joins through a control link, the stub reads as the agents' TUIs do (mouse
 * tracking on) and draws without answering until the test stops it, the guest's reply is typed into it and its link revoked
 * before its Enter. With reply, the owner replies from its own view just before the revoke, a reply that waits for the session's
 * turn.
 *
 * The revoke has to come between the text and its Enter, which Conductor writes once it has looked at the screen: a quarter
 * of a second and at most two more while the stub draws. A machine too busy to revoke in that time sees the Enter first: when
 * the stub had taken the guest's Enter before the revoke was asked for (its log's time against the test's, one clock), that
 * session is put aside and the setup done again, three times at most; an Enter taken any later fails the test, for it may have
 * been written after the revoke. Once held, every check is made.
 */
export async function holdOne(page: Page, api: Api, browser: Browser, home: string, how: HoldHow = {}): Promise<Held> {
  for (let attempt = 1; attempt <= 3; attempt++) {
    const held = await tryHold(page, api, browser, home, how)
    if (held) return held
    test.info().annotations.push({ type: 'setup again', description: `try ${attempt}: the guest's Enter was taken before the revoke was asked for` })
  }
  throw new Error("three tries, and each time the guest's Enter was taken before the revoke was asked for: the machine is too slow for the setup")
}

/** tryHold is one try of holdOne: null when the guest's Enter was taken before the revoke was asked for (the session stopped). */
async function tryHold(page: Page, api: Api, browser: Browser, home: string, { reply, chat, baseURL }: HoldHow): Promise<Held | null> {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'held line' })
  const stub = stubFiles(home, s)
  const guestCtx = await browser.newContext({ viewport: { width: 1280, height: 800 }, ...(baseURL ? { baseURL } : {}) })
  const { token, link } = await api.ok<{ token: string; link: { id: string } }>('POST', `/api/sessions/${s.id}/links`, { role: 'control' })

  // A guest joins through the link.
  await guestCtx.addInitScript(() => localStorage.setItem('conductor.displayName', 'Priya'))
  const guest = await guestCtx.newPage()
  await guest.goto(`/join/${token}`)
  await guest.getByRole('button', { name: 'Join session' }).click()
  await expect(guest.locator('[data-transport-state]').first()).toHaveAttribute('data-transport-state', 'open', { timeout: 15_000 })

  // The owner's view of the session, once its connection is open (keys typed before are dropped). The stub reads as Claude
  // Code's and Codex's TUIs do, mouse tracking on, and draws, answering nothing, as an agent at work does, until the test
  // stops it (at most 90 s): a submission's Enter waits for a quiet screen as long as Conductor's look does, and no longer.
  await page.goto(`/sessions/${encodeURIComponent(s.id)}`)
  await expect(page.locator('[data-transport-state]').first()).toHaveAttribute('data-transport-state', 'open', { timeout: 30_000 })
  const terminal = page.locator('.terminal-host .xterm-helper-textarea').first()
  await terminal.focus()
  await page.keyboard.type('stub tui on')
  await page.keyboard.press('Enter')
  await expect.poll(stub.tui, { timeout: 30_000, message: 'the stub reads as a TUI' }).toBe(true)
  await page.keyboard.type('stub spin 90')
  await page.keyboard.press('Enter')
  await expect.poll(stub.spin, { timeout: 10_000, message: 'the stub draws' }).toBe('spinning')

  // A question with no choices: the reply bars offer a reply. The owner's, with reply, is written now and sent once the
  // guest's text is typed. With chat, a question with choices, and the guest's line goes from its chat to the agent.
  const bar = page.locator('[data-quick-reply]')
  const guestBar = guest.locator('[data-quick-reply]')
  const guestChat = guest.locator('[data-chat-aside] [data-chat]')
  let send: Locator
  if (chat) {
    await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: 'Which way?', options: [{ label: 'Left', input: 'l' }, { label: 'Right', input: 'r' }] })
    await expect(guestBar).toContainText('Which way?', { timeout: 15_000 })
    await guestChat.locator('[data-chat-input] textarea, textarea[data-chat-input]').first().fill('guest line')
    send = guestChat.locator('[data-chat-to-agent]')
  } else {
    await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: 'Which name?' })
    if (reply) await bar.locator('input').fill(reply)
    await expect(guestBar).toBeVisible({ timeout: 15_000 })
    await guestBar.locator('input').fill('guest line')
    send = guestBar.getByRole('button', { name: 'Send' })
  }
  const mark = stub.lineMark()
  const guestEnter = () => stub.enters().find((e) => e.line === 'guest line')
  await send.click()
  // Its text is typed: Conductor writes it at once, so the stub's first byte of it says so (or, a test too slow to see the
  // line, the stub's taking its Enter since). The link is revoked before its Enter, nothing slow in between.
  await expect
    .poll(() => stub.typedSince(mark) || guestEnter() !== undefined, { timeout: 10_000, intervals: [20], message: 'the guest line is typed' })
    .toBe(true)
  if (reply && guestEnter() === undefined) {
    // Bounded: the guest's Enter, come meanwhile, answers the question and takes the bar away; the rule below decides then
    // (held, the reply's box is empty: it was sent).
    await bar
      .locator('input')
      .press('Enter', { timeout: 2_000 })
      .catch(() => {})
  }
  const revokeAsked = Date.now()
  await api.ok('DELETE', `/api/sessions/${s.id}/links/${link.id}`)
  // Held, or the guest's Enter was taken.
  const outcome = async () =>
    (await page.locator('[data-quick-reply][data-held="true"]').count()) > 0 ? 'held' : guestEnter() ? 'entered' : 'waiting'
  await expect.poll(outcome, { timeout: 10_000, message: 'the turn is held' }).not.toBe('waiting')
  if ((await outcome()) !== 'held') {
    const at = guestEnter()!.at
    expect(at, "the guest's Enter was taken after the revoke was asked for: it may have been written after it").toBeLessThan(revokeAsked)
    await guestCtx.close()
    await api.stopSession(s.id)
    return null
  }
  await expect.poll(stub.pending, { timeout: 10_000, message: 'the guest line is typed whole' }).toBe('guest line')
  return { s, bar, terminal, guestCtx, stub }
}

