import { execFileSync, spawn } from 'node:child_process'
import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { expect, test, type Session } from './fixtures'

// The Neovim keymap (design round 12, F8): off by default; the Keys button
// turns it on and the choice is kept per browser; with it on, the real
// Neovim on the session's machine holds the file: keys go to it, its mode
// and command line show, :w writes the file there (a file event by the
// person, so Touched follows), :q closes the tab. Needs `nvim` on PATH where
// the e2e server runs (CI installs it).
const hasNvim = (() => {
  try {
    execFileSync('sh', ['-c', 'command -v nvim'], { stdio: 'ignore' })
    return true
  } catch {
    return false
  }
})()
test.skip(!hasNvim, 'nvim is not on PATH: the Neovim keymap needs the real Neovim (CI installs it)')

test('the Neovim keymap is off until its button, then keys reach the real Neovim, :w writes and :q closes', async ({ page, api, state }) => {
  const cwd = join(state.root, 'files-vim')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'alpha\nbeta\ngamma\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'vim', cwd })
  const openNotes = async () => {
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    await pane.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    await expect(page.locator('[data-editor-area] .monaco-editor')).toBeVisible({ timeout: 30_000 })
    return pane
  }
  try {
    await page.goto(`/sessions/${s.id}`)
    const pane = await openNotes()
    const area = page.locator('[data-editor-area]')
    const lines = area.locator('.monaco-editor .view-lines')
    // Off by default: Monaco's keys, no status line, no key reaches Neovim.
    await expect(area.locator('[data-editor-keymap]')).toHaveAttribute('data-editor-keymap', 'default')
    await expect(area.locator('[data-nvim-status]')).toHaveCount(0)
    await expect(lines).toContainText('alpha')
    // The button turns it on; Neovim reports normal mode.
    await area.locator('[data-editor-keymap]').click()
    await expect(area.locator('[data-editor-keymap]')).toHaveAttribute('data-editor-keymap', 'nvim')
    const status = area.locator('[data-nvim-status]')
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    // dd removes the first line in Neovim and the editor follows.
    await lines.click()
    await page.keyboard.type('dd')
    await expect(lines).not.toContainText('alpha', { timeout: 15_000 })
    await expect(lines).toContainText('beta')
    // Insert mode shows; the typed text lands; Escape leaves it.
    await page.keyboard.type('i')
    await expect(status.locator('[data-nvim-mode-words]')).toHaveText('-- INSERT --', { timeout: 15_000 })
    await page.keyboard.type('hello ')
    await page.keyboard.press('Escape')
    await expect(status.locator('[data-nvim-mode-words]')).toHaveText('', { timeout: 15_000 })
    await expect(lines).toContainText('hello beta')
    // :w shows on the command line, writes the file on the machine and says so; the write is a file event by the person.
    await page.keyboard.type(':w')
    await expect(status.locator('[data-nvim-cmdline]')).toHaveText(':w')
    await page.keyboard.press('Enter')
    await expect(status.locator('[data-nvim-message]')).toContainText('written', { timeout: 15_000 })
    expect(readFileSync(join(cwd, 'notes.txt'), 'utf8')).toBe('hello beta\ngamma\n')
    await page.locator('[data-inspector] [data-rail-item="activity"]').click()
    await page.locator('[data-inspector] [data-activity-filter="files"]').click()
    await expect(page.locator('[data-inspector] [data-files-touched] [data-touched-op="write"]').first()).toContainText('nvim', { timeout: 15_000 })
    // :q closes the tab.
    await lines.click()
    await page.keyboard.type(':q')
    await page.keyboard.press('Enter')
    await expect(page.locator('[data-editor-tab]')).toHaveCount(0, { timeout: 15_000 })
    // The choice is kept across a reload; the button turns it off again.
    writeFileSync(join(cwd, 'notes.txt'), 'café ok\n')
    await page.reload()
    await openNotes()
    await expect(area.locator('[data-editor-keymap]')).toHaveAttribute('data-editor-keymap', 'nvim')
    await expect(area.locator('[data-nvim-status]')).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    // Neovim counts a line's bytes, the editor its UTF-16 units: past the é the cursor is where Neovim says (o is byte 7, column 6).
    await lines.click()
    await page.keyboard.type('0')
    await page.keyboard.type('f')
    await page.keyboard.type('o')
    await expect(area.locator('[data-editor-pos]')).toHaveText(/^Ln 1, Col 6$/, { timeout: 15_000 })
    await area.locator('[data-editor-keymap]').click()
    await expect(area.locator('[data-editor-keymap]')).toHaveAttribute('data-editor-keymap', 'default')
    await expect(area.locator('[data-nvim-status]')).toHaveCount(0)
  } finally {
    await api.stopSession(s.id)
  }
})

// The owner, 2026-10-13: the command line took `:%s/foo` and nothing after it. Neovim previews a substitute as its replacement
// begins (inccommand), and the change it reports for the preview carries no changedtick, which the bridge could not read: it lost
// Neovim. Now every key of the command line shows, the substitution lands as Neovim has it, Esc on a half-typed one leaves the text
// as it was (the preview is never the page's text, nor a change to save), and u undoes the substitution.
test('the command line takes a substitute: :%s/foo/bar/g lands, Esc on a half-typed one changes nothing, u undoes it', async ({ page, api, state }) => {
  const cwd = join(state.root, 'files-vim-subst')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'foo one\nfoo two foo\nbaz\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'vim-subst', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    await pane.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    const area = page.locator('[data-editor-area]')
    await expect(area.locator('.monaco-editor')).toBeVisible({ timeout: 30_000 })
    await area.locator('[data-editor-keymap]').click()
    const status = area.locator('[data-nvim-status]')
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    const lines = area.locator('.monaco-editor .view-lines')
    const cmdline = status.locator('[data-nvim-cmdline]')
    const dirtyDot = area.locator('[data-editor-tab-active] [data-editor-dirty]')
    await lines.click()
    // Half-typed, past the replacement's start (where the preview begins): every key shows; Esc leaves the text as it was.
    await page.keyboard.type(':%s/foo/x')
    await expect(cmdline).toHaveText(':%s/foo/x', { timeout: 15_000 })
    // The preview's change comes before the command line it is for: it would show by now.
    await expect(lines).toContainText('foo two foo')
    await expect(lines).not.toContainText('x one')
    await page.keyboard.press('Escape')
    await expect(cmdline).toHaveCount(0, { timeout: 15_000 })
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/)
    await expect(lines).toContainText('foo one')
    await expect(lines).toContainText('foo two foo')
    await expect(dirtyDot).toHaveCount(0)
    // The whole substitute: every key shows, Enter runs it, the text reads as Neovim has it.
    await page.keyboard.type(':%s/foo/bar/g')
    await expect(cmdline).toHaveText(':%s/foo/bar/g', { timeout: 15_000 })
    await page.keyboard.press('Enter')
    await expect(lines).toContainText('bar two bar', { timeout: 15_000 })
    await expect(lines).toContainText('bar one')
    await expect(lines).not.toContainText('foo')
    await expect(status.locator('[data-nvim-message]')).toContainText('3 substitutions on 2 lines')
    await expect(dirtyDot).toBeVisible()
    // u undoes it, and the substitution again, then :w writes it.
    await page.keyboard.type('u')
    await expect(lines).toContainText('foo two foo', { timeout: 15_000 })
    await expect(lines).not.toContainText('bar')
    await page.keyboard.type(':%s/foo/bar/g')
    await expect(cmdline).toHaveText(':%s/foo/bar/g', { timeout: 15_000 })
    await page.keyboard.press('Enter')
    await expect(lines).toContainText('bar two bar', { timeout: 15_000 })
    await page.keyboard.type(':w')
    await page.keyboard.press('Enter')
    await expect.poll(() => readFileSync(join(cwd, 'notes.txt'), 'utf8'), { timeout: 15_000 }).toBe('bar one\nbar two bar\nbaz\n')
    await expect(dirtyDot).toHaveCount(0, { timeout: 15_000 })
  } finally {
    await api.stopSession(s.id)
  }
})

// The page's own single keys are / (the filter) and ? (the shortcuts): in the editor under the Neovim keymap they are Neovim's, a search
// each. A substitute's c flag asks at each match on the status line, takes its answers as keys, and the question goes once answered.
test('in the Neovim keymap / and ? search, and :s///gc asks on the status line until answered', async ({ page, api, state }) => {
  const cwd = join(state.root, 'files-vim-search')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'top\nfoo a\nmid\nfoo a\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'vim-search', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    await pane.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    const area = page.locator('[data-editor-area]')
    await expect(area.locator('.monaco-editor')).toBeVisible({ timeout: 30_000 })
    await area.locator('[data-editor-keymap]').click()
    const status = area.locator('[data-nvim-status]')
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    const lines = area.locator('.monaco-editor .view-lines')
    const cmdline = status.locator('[data-nvim-cmdline]')
    const pos = area.locator('[data-editor-pos]')
    await lines.click()
    await page.keyboard.type('gg')
    await expect(pos).toHaveText('Ln 1, Col 1', { timeout: 15_000 })
    // / searches forward, n again; ? backward. Neither the page's filter nor its shortcuts open.
    await page.keyboard.type('/foo')
    await expect(cmdline).toHaveText('/foo', { timeout: 15_000 })
    await page.keyboard.press('Enter')
    await expect(pos).toHaveText('Ln 2, Col 1', { timeout: 15_000 })
    await page.keyboard.type('n')
    await expect(pos).toHaveText('Ln 4, Col 1', { timeout: 15_000 })
    await page.keyboard.type('?foo')
    await expect(cmdline).toHaveText('?foo', { timeout: 15_000 })
    await page.keyboard.press('Enter')
    await expect(pos).toHaveText('Ln 2, Col 1', { timeout: 15_000 })
    await expect(page.getByRole('dialog')).toHaveCount(0)
    // :%s/a/b/gc asks at each match; y, n, then the question goes.
    await page.keyboard.type(':%s/a/b/gc')
    await expect(cmdline).toHaveText(':%s/a/b/gc', { timeout: 15_000 })
    await page.keyboard.press('Enter')
    const message = status.locator('[data-nvim-message]')
    await expect(message).toContainText('replace with b', { timeout: 15_000 })
    await page.keyboard.type('y')
    await page.keyboard.type('n')
    await expect(lines).toContainText('foo b', { timeout: 15_000 })
    await expect(message).toHaveCount(0, { timeout: 15_000 })
    await expect(lines).toContainText('foo a')
    // :g/foo/d deletes both lines; u brings them back.
    await page.keyboard.type(':g/foo/d')
    await expect(cmdline).toHaveText(':g/foo/d', { timeout: 15_000 })
    await page.keyboard.press('Enter')
    await expect(lines).not.toContainText('foo', { timeout: 15_000 })
    await expect(lines).toContainText('mid')
    await page.keyboard.type('u')
    await expect(lines).toContainText('foo b', { timeout: 15_000 })
    await expect(lines).toContainText('foo a')
  } finally {
    await api.stopSession(s.id)
  }
})

// Neovim lost, not quit: here it dies (killed, once :preserve has written its swap file), and the page is told at once. The tab
// stays with the text Neovim last held, its change not written among it, and says so, across a tab switched and the area folded; a
// Neovim that dies while its tab is not in front is kept the same way. Reopen starts Neovim again, which finds the lost one's swap
// file: Recover brings the change back, :w writes it. Before, the tab closed as on :q, with no word, and its text went with it.
test('a Neovim lost keeps its tab and text, says so, and Reopen recovers its change from the swap file', async ({ page, api, state }) => {
  const cwd = join(state.root, 'files-vim-lost')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'alpha\n')
  writeFileSync(join(cwd, 'other.txt'), 'beta\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'vim-lost', cwd })
  // Neovim dies, at once or after a while.
  const die = "vim.uv.kill(vim.fn.getpid(), 'sigkill')"
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    await pane.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    const area = page.locator('[data-editor-area]')
    await expect(area.locator('.monaco-editor')).toBeVisible({ timeout: 30_000 })
    await area.locator('[data-editor-keymap]').click()
    const status = area.locator('[data-nvim-status]')
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    const lines = area.locator('.monaco-editor .view-lines')
    const lost = area.locator('[data-nvim-lost]')
    const tab = (name: string) => area.locator('[data-editor-tab]', { hasText: name })
    await lines.click()
    await page.keyboard.type('Axx')
    await page.keyboard.press('Escape')
    await expect(lines).toContainText('alphaxx', { timeout: 15_000 })
    await expect(tab('notes.txt').locator('[data-editor-dirty]')).toBeVisible({ timeout: 15_000 })
    await page.keyboard.type(':preserve')
    await page.keyboard.press('Enter')
    await page.keyboard.type(`:lua ${die}`)
    await page.keyboard.press('Enter')
    await expect(lost).toContainText('Neovim ended unexpectedly', { timeout: 15_000 })
    // Copy the text copies what the tab holds, the change not written among it.
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
    await lost.locator('[data-nvim-lost-copy]').click()
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText()), { timeout: 15_000 }).toContain('alphaxx')
    await expect(area.locator('[data-editor-tab]')).toHaveCount(1)
    await expect(lines).toContainText('alphaxx')
    // Another tab in front, then back; the area folded, then not: the text and the word stay.
    await pane.locator(`[data-file-node="${cwd}/other.txt"]`).click()
    await expect(lines).toContainText('beta', { timeout: 30_000 })
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    await expect(lost).toHaveCount(0)
    await tab('notes.txt').click()
    await expect(lost).toContainText('Neovim ended unexpectedly', { timeout: 15_000 })
    await expect(lines).toContainText('alphaxx')
    await expect(tab('notes.txt').locator('[data-editor-dirty]')).toBeVisible()
    await area.locator('[data-editor-fold]').click()
    await area.locator('[data-editor-unfold]').click()
    await expect(lost).toContainText('Neovim ended unexpectedly', { timeout: 15_000 })
    await expect(lines).toContainText('alphaxx')
    // other.txt's Neovim, its change not written, changes its text while its tab is not in front (as a plugin may), then dies: the
    // tab is kept the same way, with that last change.
    await tab('other.txt').click()
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    await lines.click()
    await page.keyboard.type('Ayy')
    await page.keyboard.press('Escape')
    await expect(lines).toContainText('betayy', { timeout: 15_000 })
    await expect(tab('other.txt').locator('[data-editor-dirty]')).toBeVisible({ timeout: 15_000 })
    await page.keyboard.type(`:lua vim.defer_fn(function() vim.api.nvim_buf_set_lines(0, 0, 1, false, {'betayy later'}) vim.cmd('preserve') ${die} end, 1500)`)
    await page.keyboard.press('Enter')
    await tab('notes.txt').click()
    await expect(lost).toContainText('Neovim ended unexpectedly', { timeout: 15_000 })
    await page.waitForTimeout(3000)
    await tab('other.txt').click()
    await expect(lost).toContainText('Neovim ended unexpectedly', { timeout: 15_000 })
    await expect(lines).toContainText('betayy later')
    await lost.locator('[data-nvim-lost-copy]').click()
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText()), { timeout: 15_000 }).toContain('betayy later')
    // Reopen notes.txt: a new Neovim finds the lost one's swap file (it has left by now) and offers its change.
    await tab('notes.txt').click()
    await expect(lost).toBeVisible({ timeout: 15_000 })
    await lost.locator('[data-nvim-reopen]').click()
    await expect(lost).toHaveCount(0)
    const banner = area.locator('[data-editor-swap]')
    await expect(banner).toBeVisible({ timeout: 30_000 })
    await expect(banner).toHaveAttribute('data-swap-running', 'false')
    await banner.locator('[data-swap-choice="recover"]').click()
    await expect(lines).toContainText('alphaxx', { timeout: 15_000 })
    await lines.click()
    await page.keyboard.type(':w')
    await page.keyboard.press('Enter')
    await expect.poll(() => readFileSync(join(cwd, 'notes.txt'), 'utf8'), { timeout: 15_000 }).toBe('alphaxx\n')
    await banner.locator('[data-swap-choice="delete"]').click()
    await expect(banner).toHaveCount(0)
  } finally {
    await api.stopSession(s.id)
  }
})

// A connection that ends ends its Neovims on the machine, their swap files kept, and no word of that comes. The page tells its tabs so
// when it closes: the tab in front and one held behind it, each with a change not written, stay with their text, lost, across the
// Reconnect, Reopen waiting for it; Reopen then finds the swap file, and Recover brings the change back. A Reopen whose open is refused
// leaves the tab as it was, saying so. Before, both kept the ids of Neovims that were gone: their keys and Save went nowhere, and no
// banner offered Reopen; and a Reopen let the lost text go before its open, which could fail.
test('a connection that ends keeps its Neovim tabs with their text, lost; after Reconnect, Reopen recovers', async ({ page, api, state }) => {
  await page.addInitScript(() => {
    // The page's sockets, to end one; and failOpens: that many nvim_open requests answered refused, as a server would, never sent.
    const w = window as unknown as { sockets?: WebSocket[]; failOpens?: number }
    const Native = window.WebSocket
    window.WebSocket = class extends Native {
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url, protocols)
        ;(w.sockets ??= []).push(this)
      }
      send(data: string | ArrayBufferLike | Blob | ArrayBufferView) {
        if ((w.failOpens ?? 0) > 0 && data instanceof Uint8Array && data[0] === 0x03) {
          const msg = JSON.parse(new TextDecoder().decode(data.subarray(1))) as { t?: string; reqId?: string }
          if (msg.t === 'nvim_open') {
            w.failOpens = (w.failOpens ?? 0) - 1
            const body = new TextEncoder().encode(JSON.stringify({ t: 'nvim_event', kind: 'error', reqId: msg.reqId, code: 'too_many_requests', message: 'refused here' }))
            const out = new Uint8Array(body.length + 1)
            out[0] = 0x03
            out.set(body, 1)
            setTimeout(() => this.onmessage?.(new MessageEvent('message', { data: out.buffer })), 0)
            return
          }
        }
        super.send(data)
      }
    } as typeof WebSocket
  })
  // A directory per repeat: this test leaves swap files on purpose (other.txt's, the ended Neovim's), which a later repeat's open finds;
  // it removes them as it ends.
  const dir = `files-vim-reconnect-${test.info().repeatEachIndex}`
  const cwd = join(state.root, dir)
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'alpha\n')
  writeFileSync(join(cwd, 'other.txt'), 'beta\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'vim-reconnect', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    await pane.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    const area = page.locator('[data-editor-area]')
    const lines = area.locator('.monaco-editor .view-lines')
    const status = area.locator('[data-nvim-status]')
    const lost = area.locator('[data-nvim-lost]')
    const tab = (name: string) => area.locator('[data-editor-tab]', { hasText: name })
    await expect(lines).toContainText('alpha', { timeout: 30_000 })
    await area.locator('[data-editor-keymap]').click()
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    await lines.click()
    await page.keyboard.type('Axx')
    await page.keyboard.press('Escape')
    await expect(lines).toContainText('alphaxx', { timeout: 15_000 })
    await expect(tab('notes.txt').locator('[data-editor-dirty]')).toBeVisible({ timeout: 15_000 })
    // Its swap file written now, as Neovim would after a pause.
    await page.keyboard.type(':preserve')
    await page.keyboard.press('Enter')
    // Another tab in front, with a change of its own: notes.txt's Neovim held behind it.
    await pane.locator(`[data-file-node="${cwd}/other.txt"]`).click()
    await expect(lines).toContainText('beta', { timeout: 30_000 })
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    await lines.click()
    await page.keyboard.type('Ayy')
    await page.keyboard.press('Escape')
    await expect(lines).toContainText('betayy', { timeout: 15_000 })
    await expect(tab('other.txt').locator('[data-editor-dirty]')).toBeVisible({ timeout: 15_000 })
    // The terminal's connection ends.
    await page.evaluate(
      (id) =>
        (window as unknown as { sockets?: WebSocket[] }).sockets
          ?.filter((ws) => ws.url.includes(`/ws/sessions/${id}?`) && ws.readyState === WebSocket.OPEN)
          .forEach((ws) => ws.close()),
      s.id,
    )
    await expect(lost).toContainText('the connection to the machine was lost', { timeout: 15_000 })
    await expect(lines).toContainText('betayy')
    // Reopen waits for the connection: there is nothing to open on until then.
    await expect(lost.locator('[data-nvim-reopen]')).toBeDisabled()
    await page.getByRole('button', { name: 'Reconnect', exact: true }).click()
    await expect(page.getByRole('button', { name: 'Reconnect', exact: true })).toHaveCount(0, { timeout: 30_000 })
    await expect(lost.locator('[data-nvim-reopen]')).toBeEnabled({ timeout: 15_000 })
    // Still lost after the Reconnect, both tabs, their text and dots kept.
    await expect(lost).toContainText('the connection to the machine was lost')
    await expect(tab('other.txt').locator('[data-editor-dirty]')).toBeVisible()
    await tab('notes.txt').click()
    await expect(lost).toContainText('the connection to the machine was lost', { timeout: 15_000 })
    await expect(lines).toContainText('alphaxx')
    await expect(tab('notes.txt').locator('[data-editor-dirty]')).toBeVisible()
    // A Reopen whose open is refused: the tab stays lost, its text kept, and says why.
    await page.evaluate(() => ((window as unknown as { failOpens?: number }).failOpens = 1))
    await lost.locator('[data-nvim-reopen]').click()
    // The refusal had (the page says so), then the tab as it is: what the refusal left, not a moment after the click.
    await expect(status).toContainText('Reopen failed: refused here', { timeout: 15_000 })
    await expect(lost).toContainText('the connection to the machine was lost')
    await expect(lines).toContainText('alphaxx')
    await expect(tab('notes.txt').locator('[data-editor-dirty]')).toBeVisible()
    // Reopen: a Neovim on the new connection, which finds the ended one's swap file; Recover brings the change back.
    await lost.locator('[data-nvim-reopen]').click()
    await expect(lost).toHaveCount(0)
    const banner = area.locator('[data-editor-swap]')
    await expect(banner).toBeVisible({ timeout: 30_000 })
    await expect(banner).toHaveAttribute('data-swap-running', 'false', { timeout: 15_000 })
    await banner.locator('[data-swap-choice="recover"]').click()
    await expect(lines).toContainText('alphaxx', { timeout: 15_000 })
    await lines.click()
    await page.keyboard.type(':w')
    await page.keyboard.press('Enter')
    await expect.poll(() => readFileSync(join(cwd, 'notes.txt'), 'utf8'), { timeout: 15_000 }).toBe('alphaxx\n')
  } finally {
    await api.stopSession(s.id)
    for (const f of swapFilesOf(state.home, dir)) rmSync(f, { force: true })
  }
})

// The page's terminal connections, watched (the lost-Neovim tests below): the keys it sends Neovim (their editor and number), the
// editors it is told ended, the answers to the pings sent here, and the frames handed to it here (deliver); and the editors it asks
// for, those it is told opened and those it closes.
interface SocketSeam {
  sockets?: WebSocket[]
  nvimSent?: Array<{ id: string; keys: string; seq?: number }>
  nvimClosed?: string[]
  pongs?: number[]
  delivered?: number
  nvimOpened?: Array<{ id: string; reqId: string; path: string }>
  nvimOpens?: Array<{ reqId: string; path: string }>
  nvimCloses?: string[]
}
async function watchSockets(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const w = window as unknown as SocketSeam
    const control = (data: unknown): Record<string, unknown> | null => {
      const bytes = data instanceof ArrayBuffer ? new Uint8Array(data) : data instanceof Uint8Array ? data : null
      if (!bytes || bytes[0] !== 0x03) return null
      try {
        return JSON.parse(new TextDecoder().decode(bytes.subarray(1))) as Record<string, unknown>
      } catch {
        return null
      }
    }
    const Native = window.WebSocket
    window.WebSocket = class extends Native {
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url, protocols)
        ;(w.sockets ??= []).push(this)
        // Before the page's own handler (set later), in the same dispatch: what is noted here the page has had.
        this.addEventListener('message', (e) => {
          // A frame handed to the page here, not one its server sent.
          if (!e.isTrusted) w.delivered = (w.delivered ?? 0) + 1
          const msg = control(e.data)
          if (msg?.t === 'nvim_event' && msg.kind === 'closed' && typeof msg.id === 'string') (w.nvimClosed ??= []).push(msg.id)
          else if (msg?.t === 'nvim_event' && msg.kind === 'opened' && typeof msg.id === 'string')
            (w.nvimOpened ??= []).push({ id: msg.id, reqId: String(msg.reqId), path: String(msg.path) })
          else if (msg?.t === 'pong' && typeof msg.ts === 'number') (w.pongs ??= []).push(msg.ts)
        })
      }
      send(data: string | ArrayBufferLike | Blob | ArrayBufferView) {
        const msg = control(data)
        if (msg?.t === 'nvim_input') (w.nvimSent ??= []).push({ id: String(msg.id), keys: String(msg.keys), seq: typeof msg.seq === 'number' ? msg.seq : undefined })
        else if (msg?.t === 'nvim_close') (w.nvimCloses ??= []).push(String(msg.id))
        else if (msg?.t === 'nvim_open') (w.nvimOpens ??= []).push({ reqId: String(msg.reqId), path: String(msg.path) })
        super.send(data)
      }
    } as typeof WebSocket
  })
}
/** The control frame of a message, as the page and the server frame it. */
function controlFrame(msg: Record<string, unknown>): Uint8Array {
  const body = new TextEncoder().encode(JSON.stringify(msg))
  const out = new Uint8Array(body.length + 1)
  out[0] = 0x03
  out.set(body, 1)
  return out
}
/**
 * Returns once the server has answered every frame the page sent to session id so far, and the page has had what it said of them: a
 * ping's answer comes after them (a connection's frames are taken in order). Its number is small, so the page takes the answer for
 * none of its own (rttFromPong).
 */
let pings = 0
async function serverCaughtUp(page: Page, id: string, timeout = nvimEndBound + 15_000): Promise<void> {
  const ts = ++pings
  await page.evaluate(
    ({ id, frame }) => {
      const w = window as unknown as SocketSeam
      const ws = w.sockets?.filter((s) => s.url.includes(`/ws/sessions/${id}?`) && s.readyState === WebSocket.OPEN).pop()
      if (!ws) throw new Error(`no open connection to session ${id}`)
      ws.send(new Uint8Array(frame))
    },
    { id, frame: [...controlFrame({ t: 'ping', ts })] },
  )
  // By default: behind keys Neovim never took and the open of another tab's Neovim (15 s at most, the server's bound), and a close,
  // which the server answers once that Neovim has left (nvimEndBound).
  await page.waitForFunction((ts) => !!(window as unknown as SocketSeam).pongs?.includes(ts), ts, { timeout })
}
/**
 * Hands the page a frame on session id's connection as its server would send it: dispatched on the socket, so every listener of the
 * page's has it, and refused when the page has no handler there or the frame did not go through the socket's listeners.
 */
async function deliver(page: Page, id: string, msg: Record<string, unknown>): Promise<void> {
  await page.evaluate(
    ({ id, frame }) => {
      const w = window as unknown as SocketSeam
      const ws = w.sockets?.filter((s) => s.url.includes(`/ws/sessions/${id}?`) && s.readyState === WebSocket.OPEN).pop()
      if (!ws) throw new Error(`no open connection to session ${id}`)
      if (typeof ws.onmessage !== 'function') throw new Error(`the page reads session ${id}'s connection without onmessage: deliver needs another way in`)
      const before = w.delivered ?? 0
      ws.dispatchEvent(new MessageEvent('message', { data: new Uint8Array(frame).buffer }))
      if ((w.delivered ?? 0) !== before + 1) throw new Error(`the frame handed to session ${id}'s connection did not reach its listeners`)
    },
    { id, frame: [...controlFrame(msg)] },
  )
}
/**
 * Returns once the page has let go of enough of its Neovims for another to open under the server's bound of four a connection
 * (proto.MaxNvimPerSub): those it was told opened, less those it closed. The server ends a closed one before it reads the next frame
 * (NvimClose), so an open sent after this finds its place free. A tab left with keys on their way keeps its Neovim until Neovim has
 * done them (a loaded machine makes that slow), and only then does the page close it: this waits for that, not for a time.
 */
async function roomForNeovim(page: Page): Promise<void> {
  await page.waitForFunction(
    () => {
      const w = window as unknown as SocketSeam
      const closed = new Set(w.nvimCloses ?? [])
      return (w.nvimOpened ?? []).filter((o) => !closed.has(o.id)).length < 4
    },
    undefined,
    { timeout: 60_000 },
  )
}

// Characters typed in insert mode show at once (the local echo); Neovim, stopped here (SIGSTOP, so it reads none of them), dies
// before it has them. The tab keeps them: the text Neovim last held with what was typed laid over it, and its changes that come
// after its end reconciled with it rather than written over it. (#281: a Neovim merely slowed by its config had taken the first
// character before the kill on a loaded box, and the banner said `yz`; stopped, it takes none, whatever the box does. #429: the
// server refused the key on its way as Neovim died, a word from another goroutine than its end, and a page that had the refusal
// first took the `x` off as if Neovim lived; the keys that meet its end are no longer refused.) Neovim has done every key before
// it is stopped, so the ones typed after are all it never had; once it has ended, the server has answered every key typed before the
// tab is looked at.
test('a Neovim lost while typed characters wait for it keeps them in its tab', async ({ page, api, state }) => {
  await watchSockets(page)
  const cwd = join(state.root, 'files-vim-lost-typed')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'alpha\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'vim-lost-typed', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    await pane.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    const area = page.locator('[data-editor-area]')
    await expect(area.locator('.monaco-editor')).toBeVisible({ timeout: 30_000 })
    await area.locator('[data-editor-keymap]').click()
    const status = area.locator('[data-nvim-status]')
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    const lines = area.locator('.monaco-editor .view-lines')
    await lines.click()
    // Neovim's process, from its own word.
    await page.keyboard.type(':echo getpid()')
    await page.keyboard.press('Enter')
    const message = status.locator('[data-nvim-message]')
    await expect(message).toHaveText(/^\d+$/, { timeout: 15_000 })
    const pid = Number(await message.textContent())
    await page.keyboard.type('A')
    await expect(status.locator('[data-nvim-mode-words]')).toHaveText('-- INSERT --', { timeout: 15_000 })
    // Every key so far done, as Neovim said (the keymap waits for keys on their way): the mode can show before that word comes.
    await expect(area.locator('[data-editor-keymap]')).toBeEnabled({ timeout: 15_000 })
    // Stopped, Neovim reads nothing more: the characters typed now wait for it, shown by the local echo alone, until it is killed.
    process.kill(pid, 'SIGSTOP')
    await page.keyboard.type('xyz')
    await expect(lines).toContainText('alphaxyz', { timeout: 5_000 })
    process.kill(pid, 'SIGKILL')
    await expect(area.locator('[data-nvim-lost]')).toContainText('Neovim ended unexpectedly', { timeout: 15_000 })
    await serverCaughtUp(page, s.id)
    await expect(lines).toContainText('alphaxyz')
    // And the banner says which characters Neovim never had.
    await expect(area.locator('[data-nvim-unconfirmed]')).toHaveText('xyz')
    await expect(area.locator('[data-editor-tab-active] [data-editor-dirty]')).toBeVisible()
  } finally {
    await api.stopSession(s.id)
  }
})

// The same behind another tab: a tab not in front whose Neovim is lost keeps the characters typed that it never had, and a
// refusal of them that comes after its end changes nothing, as in front. Keys that reach the server once the editor of a Neovim
// that died has been let go are refused, and the tab not in front took that word as if its Neovim lived (#429); the refusal is
// handed to the page here, so the order is the test's, not the machine's.
test('a Neovim lost behind another tab keeps the characters typed it never had, though a refusal of them follows its end', async ({ page, api, state }) => {
  await watchSockets(page)
  const cwd = join(state.root, 'files-vim-lost-held')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'alpha\n')
  writeFileSync(join(cwd, 'other.txt'), 'beta\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'vim-lost-held', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    await pane.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    const area = page.locator('[data-editor-area]')
    await expect(area.locator('.monaco-editor')).toBeVisible({ timeout: 30_000 })
    await area.locator('[data-editor-keymap]').click()
    const status = area.locator('[data-nvim-status]')
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    const lines = area.locator('.monaco-editor .view-lines')
    const tab = (name: string) => area.locator('[data-editor-tab]', { hasText: name })
    await lines.click()
    await page.keyboard.type(':echo getpid()')
    await page.keyboard.press('Enter')
    const message = status.locator('[data-nvim-message]')
    await expect(message).toHaveText(/^\d+$/, { timeout: 15_000 })
    const pid = Number(await message.textContent())
    await page.keyboard.type('A')
    await expect(status.locator('[data-nvim-mode-words]')).toHaveText('-- INSERT --', { timeout: 15_000 })
    await expect(area.locator('[data-editor-keymap]')).toBeEnabled({ timeout: 15_000 })
    process.kill(pid, 'SIGSTOP')
    await page.keyboard.type('xyz')
    await expect(lines).toContainText('alphaxyz', { timeout: 5_000 })
    // Another tab in front: notes.txt's Neovim held behind it, what was typed laid over its text.
    await pane.locator(`[data-file-node="${cwd}/other.txt"]`).click()
    await expect(area.locator('[data-editor-tab-active]')).toContainText('other.txt')
    process.kill(pid, 'SIGKILL')
    // The keys typed while it was stopped, the last sent of each.
    const sent = (await page.evaluate(() => (window as unknown as SocketSeam).nvimSent ?? [])).reverse()
    const id = sent.find((k) => k.keys === 'x')?.id ?? ''
    const ySeq = sent.find((k) => k.keys === 'y' && k.id === id)?.seq
    expect(id).not.toBe('')
    expect(ySeq).toBeGreaterThan(0)
    // Its end, then a refusal of one of the keys it never had, as for keys that reached the server once its editor was let go.
    await page.waitForFunction((id) => !!(window as unknown as SocketSeam).nvimClosed?.includes(id), id, { timeout: 15_000 })
    await deliver(page, s.id, { t: 'nvim_event', id, kind: 'error', ack: ySeq, code: 'file_denied', message: 'session: file access denied' })
    // Whatever the server itself said of the keys typed, the page has had it.
    await serverCaughtUp(page, s.id)
    // In front again: lost, the text it last held with every character it never had, all named.
    await tab('notes.txt').click()
    await expect(area.locator('[data-nvim-lost]')).toContainText('Neovim ended unexpectedly', { timeout: 15_000 })
    await expect(lines).toContainText('alphaxyz')
    await expect(area.locator('[data-nvim-unconfirmed]')).toHaveText('xyz')
    await expect(status).not.toContainText('Keys did not reach Neovim')
    await expect(area.locator('[data-editor-tab-active] [data-editor-dirty]')).toBeVisible()
  } finally {
    await api.stopSession(s.id)
  }
})

// How many Neovims the server has opened on this page for file name, from the answers to the page's opens (watchSockets). The status
// line shows normal mode before a tab's Neovim answers, so keys typed then reach none; and a tab left then would leave its Neovim
// running on the server (#500), holding its file's swap file and one of the four a connection keeps. A tab that comes to the front,
// opened or shown again as another closes, has its Neovim before a test goes on: nvimUp.
function nvimOpens(page: Page, name: string): Promise<number> {
  return page.evaluate(
    (n) =>
      ((window as unknown as SocketSeam).nvimOpened ?? []).filter((o) => o.reqId !== 'undefined' && (o.path === n || o.path.endsWith(`/${n}`))).length,
    name,
  )
}

// How long a Neovim the server ends may take to be gone, its swap file with it: the bridge lets it leave for nvimEndWait (30 s) and
// gives it nvimReap (2 s) more to be reaped (internal/session/nvim.go; nvim.exitWait is the same 30 s). A loaded machine makes an exit
// slow (#281: a swap file still there 15 s after a :q), so a test that judges a swap file after an end the page cannot see waits this
// long, the sum of the product's caps, and no less.
const nvimEndBound = 32_000

// The swap files Neovim keeps for the files under the directory named dir (its name in the swap file's name, ended as the path is):
// a test that leaves one on purpose removes its own as it ends, so nothing of it is left for a later test or repeat to find.
function swapFilesOf(home: string, dir: string): string[] {
  const swapDir = join(home, '.local', 'state', 'nvim', 'swap')
  return existsSync(swapDir) ? readdirSync(swapDir).filter((f) => f.includes(`%${dir}%`)).map((f) => join(swapDir, f)) : []
}

// A tab left while keys Neovim has not done are on their way keeps its Neovim: they may be making a change it has not said yet. Neovim
// is slow here and answers in the middle of a key (its config waits 1.5 s with vim.wait in InsertCharPre and in an x mapped to x, so
// a key's acknowledgement comes before its change; j is busy for 2 s). Its change then shows on the tab not in front, or folded: the
// unsaved dot, and the close asks; closing right after typing asks too. Kept Neovims whose keys changed nothing go once done, so tabs
// left that way do not use up the four a window may keep. Each open that needs one of those four waits for the page to have let a
// kept one go (roomForNeovim), never for a time: on a loaded machine the keys take longer than any fixed wait (#429's review).
test('a tab left with keys on their way keeps its Neovim until they are done: their change shows on it, else it goes', async ({ page, api, state }) => {
  await watchSockets(page)
  const config = join(state.home, '.config', 'nvim')
  mkdirSync(config, { recursive: true })
  writeFileSync(
    join(config, 'init.lua'),
    "local uv = vim.uv or vim.loop\nvim.o.timeoutlen = 1000\nvim.api.nvim_create_autocmd('InsertCharPre', { callback = function() vim.wait(1500) end })\nvim.keymap.set('n', 'x', function() vim.wait(1500) return 'x' end, { expr = true })\nvim.keymap.set('n', 'j', function() local t = uv.hrtime() while uv.hrtime() - t < 2e9 do end return 'j' end, { expr = true })\nvim.keymap.set('n', 'kk', 'k')\n",
  )
  // A directory per repeat: a Neovim of the last repeat's tabs, still leaving as its session stopped, would greet this one's open of
  // the same file with its swap file.
  const cwd = join(state.root, `files-vim-waiting-${test.info().repeatEachIndex}`)
  mkdirSync(cwd, { recursive: true })
  const names = ['f1.txt', 'f2.txt', 'f3.txt', 'f4.txt', 'f5.txt', 'edit.txt', 'fold.txt', 'normal.txt', 'quick.txt', 'saved.txt', 'other.txt']
  for (const n of names) writeFileSync(join(cwd, n), `${n}\nsecond\n`)
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'vim-waiting', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    const area = page.locator('[data-editor-area]')
    const lines = area.locator('.monaco-editor .view-lines')
    const status = area.locator('[data-nvim-status]')
    const tab = (name: string) => area.locator('[data-editor-tab]', { hasText: name })
    // A tab that comes to the front, opened or shown again as another closes (other.txt here), has its Neovim before the test goes on:
    // one left before would stay running on the server (#500), a fifth of the four a connection keeps, and later opens are refused.
    const opens = (name: string) => nvimOpens(page, name)
    const nvimUp = async (name: string, before: number) => {
      await expect.poll(() => opens(name), { timeout: 30_000 }).toBeGreaterThan(before)
      await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    }
    const open = async (name: string) => {
      const before = await opens(name)
      await pane.locator(`[data-file-node="${cwd}/${name}"]`).click()
      await expect(lines).toContainText(name, { timeout: 30_000 })
      await nvimUp(name, before)
    }
    // The server ends a Neovim the page closes on the connection's read loop (NvimClose), as long as that Neovim takes to leave, and
    // reads the keys sent after it only then: A goes once the server has answered every frame before it (a kept Neovim the page let go
    // as its keys were done among them), so that its mode is Neovim's answer to A alone. That answer has nothing of the config's waits
    // in it (InsertCharPre fires for the x after it): 30 s, the spec's bound for a Neovim's answer on a loaded machine.
    const typeInto = async () => {
      await lines.click()
      await serverCaughtUp(page, s.id)
      await page.keyboard.type('A')
      await expect(status.locator('[data-nvim-mode-words]')).toHaveText('-- INSERT --', { timeout: 30_000 })
      await page.keyboard.type('x')
    }
    const question = page.locator('[role="dialog"]')
    const discard = async (name: string) => {
      const before = await opens('other.txt')
      await tab(name).click()
      await page.keyboard.press('Escape')
      await area.locator('[data-editor-tab-active] [data-editor-close]').click()
      await expect(question).toContainText(`Save ${name}?`)
      await question.locator('[data-editor-discard]').click()
      await expect(tab(name)).toHaveCount(0)
      await nvimUp('other.txt', before)
    }
    await pane.locator(`[data-file-node="${cwd}/edit.txt"]`).click()
    await expect(lines).toContainText('edit.txt', { timeout: 30_000 })
    await area.locator('[data-editor-keymap]').click()
    await nvimUp('edit.txt', 0)
    // Another tab in front while x is on its way: once Neovim has it, the tab not in front wears the dot, and its text holds x.
    await typeInto()
    await open('other.txt')
    await expect(tab('edit.txt').locator('[data-editor-dirty]')).toBeVisible({ timeout: 15_000 })
    await tab('edit.txt').click()
    await expect(lines).toContainText('edit.txtx', { timeout: 15_000 })
    await discard('edit.txt')
    // A normal-mode x (no character to show at once) on its way as the tab is left: kept until done, then its change shows.
    await open('normal.txt')
    await lines.click()
    await page.keyboard.type('0')
    await page.keyboard.type('x')
    await open('other.txt')
    await expect(tab('normal.txt').locator('[data-editor-dirty]')).toBeVisible({ timeout: 15_000 })
    await tab('normal.txt').click()
    await expect(lines).toContainText('ormal.txt', { timeout: 15_000 })
    await discard('normal.txt')
    // Closed right after typing, before Neovim has the character: the close asks.
    await open('quick.txt')
    await typeInto()
    let before = await opens('other.txt')
    await area.locator('[data-editor-tab-active] [data-editor-close]').click()
    await expect(question).toContainText('Save quick.txt?')
    await question.locator('[data-editor-discard]').click()
    await expect(tab('quick.txt')).toHaveCount(0)
    await nvimUp('other.txt', before)
    // Closed right after typing, and Save chosen: the character is waited for, then written, then the tab closes.
    await open('saved.txt')
    await typeInto()
    before = await opens('other.txt')
    await area.locator('[data-editor-tab-active] [data-editor-close]').click()
    await expect(question).toContainText('Save saved.txt?')
    await question.locator('[data-editor-close-save]').click()
    await expect(tab('saved.txt')).toHaveCount(0, { timeout: 15_000 })
    expect(readFileSync(join(cwd, 'saved.txt'), 'utf8')).toBe('saved.txtx\nsecond\n')
    await nvimUp('other.txt', before)
    // The area folded while x is on its way: once Neovim has it, the folded tab wears the dot, and Close all asks. Opened once the
    // page has let go of the Neovims of the tabs closed above (a close waits for the keys on their way).
    await roomForNeovim(page)
    await open('fold.txt')
    await typeInto()
    await area.locator('[data-editor-fold]').click()
    await expect(area.locator('[data-editor-folded]')).toBeVisible()
    await expect(area.locator('[data-editor-folded] [data-editor-folded-tab] [data-editor-dirty]')).toBeVisible({ timeout: 15_000 })
    await area.locator('[data-editor-folded] [data-editor-close-all]').click()
    await expect(question).toContainText('Save fold.txt?')
    // The question brings the tab to the front, unfolded.
    await question.getByRole('button', { name: 'Cancel' }).click()
    await expect(lines).toContainText('fold.txtx', { timeout: 15_000 })
    await discard('fold.txt')
    // Four clean tabs left each with a key on its way (other.txt's, clean and done, went as it was left): a j Neovim is busy with, a k
    // that waits for a second k (the kk mapping) until it times out with no key after it. Kept until their key is done, then gone, so
    // a fifth opens.
    for (const [n, key] of [['f1.txt', 'j'], ['f2.txt', 'j'], ['f3.txt', 'k'], ['f4.txt', 'k']] as const) {
      await open(n)
      await lines.click()
      await page.keyboard.type(key)
    }
    await roomForNeovim(page)
    await open('f5.txt')
    await expect(status.locator('[data-nvim-message]')).toHaveCount(0)
  } finally {
    rmSync(join(config, 'init.lua'), { force: true })
    await api.stopSession(s.id)
  }
})

// A tab left while a key is on its way follows what Neovim then does: here `o` waits 1.5 s (its mapping's vim.wait) and the tab is left
// at once. Shown again, it is where Neovim is: insert mode, on the new line, and the next keys land there. And a tab left with typed
// characters still showing (`jj`, an insert mapping to Escape that waits too) comes back to Neovim's cursor once they go, not after
// them. Before, it came back with the cursor and mode it was left with, though Neovim had moved on: the keys typed next went somewhere
// else than it showed.
test('a tab left while keys are on their way comes back where Neovim is, in its mode', async ({ page, api, state }) => {
  const config = join(state.home, '.config', 'nvim')
  mkdirSync(config, { recursive: true })
  writeFileSync(
    join(config, 'init.lua'),
    [
      "vim.keymap.set('n', 'o', function() vim.wait(1500) return 'o' end, { expr = true })",
      "vim.keymap.set('i', 'jj', function() vim.wait(1500) return '<Esc>' end, { expr = true })",
      "vim.keymap.set('i', 'q', function() vim.wait(3000) return '' end, { expr = true })",
    ].join('\n') + '\n',
  )
  const cwd = join(state.root, 'files-vim-follow')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'alpha\nbeta\n')
  writeFileSync(join(cwd, 'other.txt'), 'gamma\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'vim-follow', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    await pane.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    const area = page.locator('[data-editor-area]')
    const lines = area.locator('.monaco-editor .view-lines')
    const status = area.locator('[data-nvim-status]')
    const tab = (name: string) => area.locator('[data-editor-tab]', { hasText: name })
    await expect(lines).toContainText('alpha', { timeout: 30_000 })
    await area.locator('[data-editor-keymap]').click()
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    await lines.click()
    await page.keyboard.type('gg')
    await expect(area.locator('[data-editor-pos]')).toHaveText('Ln 1, Col 1', { timeout: 15_000 })
    await page.keyboard.type('o')
    await pane.locator(`[data-file-node="${cwd}/other.txt"]`).click()
    await expect(lines).toContainText('gamma', { timeout: 30_000 })
    // Neovim done with the o meanwhile.
    await page.waitForTimeout(3000)
    await tab('notes.txt').click()
    await expect(lines).toContainText('beta', { timeout: 15_000 })
    await expect(status.locator('[data-nvim-mode-words]')).toHaveText('-- INSERT --', { timeout: 15_000 })
    await expect(area.locator('[data-editor-pos]')).toHaveText('Ln 2, Col 1')
    // The editor's keys again (its text area focused, the cursor where it is).
    await area.locator('.monaco-editor textarea').focus()
    await page.keyboard.type('new')
    await page.keyboard.press('Escape')
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 15_000 })
    await page.keyboard.type(':w')
    await page.keyboard.press('Enter')
    await expect.poll(() => readFileSync(join(cwd, 'notes.txt'), 'utf8'), { timeout: 15_000 }).toBe('alpha\nnew\nbeta\n')
    // A change not written, then jj, the tab left while it shows: Neovim leaves insert mode, its cursor on the last x.
    await page.keyboard.type('Axx')
    await expect(lines).toContainText('newxx', { timeout: 15_000 })
    await expect(tab('notes.txt').locator('[data-editor-dirty]')).toBeVisible({ timeout: 15_000 })
    await page.keyboard.type('jj')
    await tab('other.txt').click()
    await expect(lines).toContainText('gamma', { timeout: 30_000 })
    await page.waitForTimeout(3000)
    await tab('notes.txt').click()
    await expect(lines).toContainText('newxx', { timeout: 15_000 })
    await expect(lines).not.toContainText('newxxjj')
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 15_000 })
    await expect(area.locator('[data-editor-pos]')).toHaveText('Ln 2, Col 5')
    // x deletes what the cursor shows.
    await area.locator('.monaco-editor textarea').focus()
    await page.keyboard.type('x')
    await expect(lines).not.toContainText('newxx', { timeout: 15_000 })
    await page.keyboard.type(':w')
    await page.keyboard.press('Enter')
    await expect.poll(() => readFileSync(join(cwd, 'notes.txt'), 'utf8'), { timeout: 15_000 }).toBe('alpha\nnewx\nbeta\n')
    // A key that changes nothing, in the middle of a line (q, which waits 3 s), after a change not written (the tab keeps its Neovim),
    // its tab left, shown and left again before Neovim is done: it comes back where Neovim's cursor was when it took the key, not after
    // the q that never was.
    await page.keyboard.type('gg0lli')
    await expect(status.locator('[data-nvim-mode-words]')).toHaveText('-- INSERT --', { timeout: 15_000 })
    await page.keyboard.type('Y')
    await expect(tab('notes.txt').locator('[data-editor-dirty]')).toBeVisible({ timeout: 15_000 })
    await expect(area.locator('[data-editor-pos]')).toHaveText('Ln 1, Col 4', { timeout: 15_000 })
    await page.waitForTimeout(500)
    await page.keyboard.type('q')
    await expect(lines).toContainText('alYqpha')
    await tab('other.txt').click()
    await expect(lines).toContainText('gamma', { timeout: 15_000 })
    await tab('notes.txt').click()
    await expect(lines).toContainText('alYqpha', { timeout: 15_000 })
    await tab('other.txt').click()
    await expect(lines).toContainText('gamma', { timeout: 15_000 })
    await page.waitForTimeout(4000)
    await tab('notes.txt').click()
    await expect(lines).toContainText('alYpha', { timeout: 15_000 })
    await expect(lines).not.toContainText('alYqpha')
    await expect(area.locator('[data-editor-pos]')).toHaveText('Ln 1, Col 4')
    await area.locator('.monaco-editor textarea').focus()
    await page.keyboard.type('z')
    await expect(lines).toContainText('alYzpha', { timeout: 15_000 })
    await page.keyboard.press('Escape')
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 15_000 })
    await page.keyboard.type(':w')
    await page.keyboard.press('Enter')
    await expect.poll(() => readFileSync(join(cwd, 'notes.txt'), 'utf8'), { timeout: 15_000 }).toBe('alYzpha\nnewx\nbeta\n')
  } finally {
    rmSync(join(config, 'init.lua'), { force: true })
    await api.stopSession(s.id)
  }
})

// Don't save drops what the closed tabs' Neovims hold, that once: a file closed with it, opened again, edited and closed before Neovim
// says so (its x waits 1.5 s here) does not ask, and its Neovim ends keeping the change, which the next open offers from its swap file.
test("Don't save holds for its own close only: a later close keeps a change still on its way", async ({ page, api, state }) => {
  await watchSockets(page)
  const config = join(state.home, '.config', 'nvim')
  mkdirSync(config, { recursive: true })
  writeFileSync(join(config, 'init.lua'), "vim.keymap.set('n', 'x', function() vim.wait(1500) return 'x' end, { expr = true })\n")
  // A directory per repeat: the swap file this test leaves on purpose (clean.txt's) would greet a later repeat's first open; it removes
  // that file as it ends.
  const dir = `files-vim-consent-${test.info().repeatEachIndex}`
  const cwd = join(state.root, dir)
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'dirty.txt'), 'dirty\n')
  writeFileSync(join(cwd, 'clean.txt'), 'clean\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'vim-consent', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    const area = page.locator('[data-editor-area]')
    const lines = area.locator('.monaco-editor .view-lines')
    const status = area.locator('[data-nvim-status]')
    const question = page.locator('[role="dialog"]')
    const open = async (name: string) => {
      await pane.locator(`[data-file-node="${cwd}/${name}"]`).click()
      await expect(lines).toContainText(name.replace('.txt', ''), { timeout: 30_000 })
      await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    }
    await pane.locator(`[data-file-node="${cwd}/clean.txt"]`).click()
    await expect(lines).toContainText('clean', { timeout: 30_000 })
    await area.locator('[data-editor-keymap]').click()
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    await open('dirty.txt')
    await lines.click()
    await page.keyboard.type('Ayy')
    await page.keyboard.press('Escape')
    await expect(area.locator('[data-editor-tab-active] [data-editor-dirty]')).toBeVisible({ timeout: 15_000 })
    // Close all, Don't save: both tabs go.
    await area.locator('[data-editor-close-all]').first().click()
    await question.locator('[data-editor-discard]').click()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(0)
    // clean.txt again: an x on its way as its tab closes, which does not ask (no change said yet).
    await open('clean.txt')
    await lines.click()
    await page.keyboard.type('0x')
    await area.locator('[data-editor-tab-active] [data-editor-close]').click()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(0)
    // The page lets that Neovim go once its x is done (the tab kept it until then), and the server ends it on the read loop before it
    // reads the next frame (NvimClose), so a ping answered after the close comes once it has left, its swap file kept.
    const second = await page.waitForFunction(() => (window as unknown as SocketSeam).nvimOpened?.filter((o) => o.reqId !== 'undefined' && o.path.endsWith('/clean.txt'))[1]?.id, undefined, { timeout: 30_000 })
    const kept = (await second.jsonValue()) as string
    await page.waitForFunction((id) => !!(window as unknown as SocketSeam).nvimCloses?.includes(id), kept, { timeout: 30_000 })
    await serverCaughtUp(page, s.id)
    // Its Neovim ended keeping the change: the next open offers it from the swap file.
    await pane.locator(`[data-file-node="${cwd}/clean.txt"]`).click()
    const banner = area.locator('[data-editor-swap]')
    await expect(banner).toContainText('with changes not written', { timeout: 30_000 })
    await banner.locator('[data-swap-choice="recover"]').click()
    await expect(lines).toContainText('lean', { timeout: 15_000 })
    await expect(lines).not.toContainText('clean')
  } finally {
    rmSync(join(config, 'init.lua'), { force: true })
    await api.stopSession(s.id)
    for (const f of swapFilesOf(state.home, dir)) rmSync(f, { force: true })
  }
})

// Save in the close question writes even with an operator waiting for its motion (d), which never settles by itself: the write goes in
// order after it, ending the wait. And a key an insert mapping takes without changing the text (q here) shows at once, then goes once
// Neovim says it is done: no change, no dot; in the middle of a line, the cursor goes back with it, where the next character lands.
test('Save writes with an operator waiting; a key a mapping takes leaves no change behind', async ({ page, api, state }) => {
  const config = join(state.home, '.config', 'nvim')
  mkdirSync(config, { recursive: true })
  writeFileSync(join(config, 'init.lua'), "vim.keymap.set('i', 'q', '<Nop>')\n")
  const cwd = join(state.root, 'files-vim-pending')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'op.txt'), 'op\n')
  writeFileSync(join(cwd, 'nop.txt'), 'nop\n')
  writeFileSync(join(cwd, 'mid.txt'), 'abcde\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'vim-pending', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    const area = page.locator('[data-editor-area]')
    const lines = area.locator('.monaco-editor .view-lines')
    const status = area.locator('[data-nvim-status]')
    const question = page.locator('[role="dialog"]')
    const dot = area.locator('[data-editor-tab-active] [data-editor-dirty]')
    await pane.locator(`[data-file-node="${cwd}/op.txt"]`).click()
    await expect(lines).toContainText('op', { timeout: 30_000 })
    await area.locator('[data-editor-keymap]').click()
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    await lines.click()
    await page.keyboard.type('Ayy')
    await page.keyboard.press('Escape')
    await expect(dot).toBeVisible({ timeout: 15_000 })
    await page.keyboard.type('d')
    await area.locator('[data-editor-tab-active] [data-editor-close]').click()
    await expect(question).toContainText('Save op.txt?')
    await question.locator('[data-editor-close-save]').click()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(0, { timeout: 15_000 })
    expect(readFileSync(join(cwd, 'op.txt'), 'utf8')).toBe('opyy\n')
    // q in insert mode: shown at once, gone once Neovim is done with it, and nothing to save.
    await pane.locator(`[data-file-node="${cwd}/nop.txt"]`).click()
    await expect(lines).toContainText('nop', { timeout: 30_000 })
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    await lines.click()
    await page.keyboard.type('A')
    await expect(status.locator('[data-nvim-mode-words]')).toHaveText('-- INSERT --', { timeout: 15_000 })
    await page.keyboard.type('q')
    await expect(lines).not.toContainText('nopq', { timeout: 15_000 })
    await expect(dot).toHaveCount(0)
    await page.keyboard.press('Escape')
    await area.locator('[data-editor-tab-active] [data-editor-close]').click()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(0, { timeout: 15_000 })
    // In the middle of a line: q goes and the cursor with it, back before c, where z then lands.
    await pane.locator(`[data-file-node="${cwd}/mid.txt"]`).click()
    await expect(lines).toContainText('abcde', { timeout: 30_000 })
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    await area.locator('.monaco-editor textarea').focus()
    await page.keyboard.type('0lli')
    await expect(status.locator('[data-nvim-mode-words]')).toHaveText('-- INSERT --', { timeout: 15_000 })
    await expect(area.locator('[data-editor-pos]')).toHaveText('Ln 1, Col 3', { timeout: 15_000 })
    await page.keyboard.type('q')
    await expect(lines).not.toContainText('abqcde', { timeout: 15_000 })
    await expect(area.locator('[data-editor-pos]')).toHaveText('Ln 1, Col 3', { timeout: 15_000 })
    await page.keyboard.type('z')
    await expect(lines).toContainText('abzcde', { timeout: 15_000 })
    await page.waitForTimeout(500)
    await expect(lines).toContainText('abzcde')
    await page.keyboard.press('Escape')
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 15_000 })
    await page.keyboard.type(':w')
    await page.keyboard.press('Enter')
    await expect.poll(() => readFileSync(join(cwd, 'mid.txt'), 'utf8'), { timeout: 15_000 }).toBe('abzcde\n')
  } finally {
    rmSync(join(config, 'init.lua'), { force: true })
    await api.stopSession(s.id)
  }
})

// The keymap waits while a key is still on its way to Neovim (its j waits 1.5 s here): what the key makes is Neovim's to show, and a
// Neovim dying meanwhile would leave it with no tab to say so. Once it is done the keymap turns, and Monaco's keys own the text.
test("the keymap waits for keys on their way to Neovim, then Monaco's keys own the text", async ({ page, api, state }) => {
  const config = join(state.home, '.config', 'nvim')
  mkdirSync(config, { recursive: true })
  writeFileSync(join(config, 'init.lua'), "vim.keymap.set('n', 'j', function() vim.wait(1500) return 'j' end, { expr = true })\n")
  const cwd = join(state.root, 'files-vim-keymap')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'alpha\nbeta\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'vim-keymap', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    await pane.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    const area = page.locator('[data-editor-area]')
    const lines = area.locator('.monaco-editor .view-lines')
    const button = area.locator('[data-editor-keymap]')
    await expect(lines).toContainText('alpha', { timeout: 30_000 })
    await button.click()
    await expect(area.locator('[data-nvim-status]')).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    await lines.click()
    await page.keyboard.type('j')
    await expect(button).toBeDisabled()
    await expect(button).toBeEnabled({ timeout: 10_000 })
    await button.click()
    await expect(button).toHaveAttribute('data-editor-keymap', 'default')
    await lines.click()
    await page.keyboard.press('Control+Home')
    await page.keyboard.type('MY ')
    await expect(lines).toContainText('MY alpha')
    await page.waitForTimeout(2000)
    await expect(lines).toContainText('MY alpha')
  } finally {
    rmSync(join(config, 'init.lua'), { force: true })
    await api.stopSession(s.id)
  }
})

// Keys Neovim has no room for are refused: they never reach it. Here a large paste into a Neovim slow to read it (its config waits
// 20 ms with vim.wait on each typed character, answering requests meanwhile) fills its input buffer. The status line names what was
// refused until dismissed, and Save from the close question does not close the tab while they are: the file would be written without
// them.
test('keys refused on their way are named until dismissed, and Save waits for that', async ({ page, api, state }) => {
  const config = join(state.home, '.config', 'nvim')
  mkdirSync(config, { recursive: true })
  writeFileSync(join(config, 'init.lua'), "vim.api.nvim_create_autocmd('InsertCharPre', { callback = function() vim.wait(20) end })\n")
  const cwd = join(state.root, 'files-vim-refused')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'end\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'vim-refused', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    await pane.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    const area = page.locator('[data-editor-area]')
    const lines = area.locator('.monaco-editor .view-lines')
    const status = area.locator('[data-nvim-status]')
    await expect(lines).toContainText('end', { timeout: 30_000 })
    await area.locator('[data-editor-keymap]').click()
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    await lines.click()
    await page.keyboard.type('I')
    await expect(status.locator('[data-nvim-mode-words]')).toHaveText('-- INSERT --', { timeout: 15_000 })
    // A paste larger than Neovim's input buffer: the end of it is refused, piece by piece, each once Neovim has had a second to make
    // room. Waited for until no more come.
    await page.keyboard.insertText('a'.repeat(18_000))
    const refused = status.locator('[data-nvim-refused]')
    await expect(refused).toBeVisible({ timeout: 30_000 })
    let said = ''
    await expect
      .poll(
        async () => {
          const now = (await refused.textContent()) ?? ''
          const same = now === said
          said = now
          return same
        },
        { timeout: 60_000, intervals: [3_000] },
      )
      .toBe(true)
    await page.keyboard.press('Escape')
    // Save from the close question does not close the tab while they are not dismissed.
    await area.locator('[data-editor-tab-active] [data-editor-close]').click()
    const question = page.locator('[role="dialog"]')
    await expect(question.locator('[data-editor-close-refused]')).toBeVisible()
    await question.locator('[data-editor-close-save]').click()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(1)
    await question.getByRole('button', { name: 'Cancel' }).click()
    // Dismissed: they no longer hold the tab (Neovim, still busy with the paste, has said no change yet: the close does not ask).
    await refused.locator('[data-nvim-refused-dismiss]').click()
    await expect(refused).toHaveCount(0)
    await area.locator('[data-editor-tab-active] [data-editor-close]').click()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(0, { timeout: 15_000 })
    expect(readFileSync(join(cwd, 'notes.txt'), 'utf8')).toBe('end\n')
  } finally {
    rmSync(join(config, 'init.lua'), { force: true })
    await api.stopSession(s.id)
  }
})

// Keys refused stay named when their tab is left, though Neovim has done and written all it had: here its first typed character waits
// 5 s (vim.wait), so a paste fills its input and the end of it is refused; then it takes the rest at once and :w writes it. Another tab
// in front, the area folded: the tab keeps its Neovim and the word of what it never had, and Save still waits for that. Before, the
// tab left clean and done let its Neovim go, and the word with it.
//
// The paste is a hundred nvim_input frames, which the server gives Neovim one after another on the connection's read loop, each
// waiting for room in Neovim's input (InputSeq) and for its acknowledgement; Neovim, the server and the page use a few seconds of CPU
// for all of it, yet on a loaded machine (load average 100 to 120 on the 64-core box, 2026-10-31) the read loop is through them only
// some 90 s after the paste, and the Escape typed after them waits as long (#281: the test ran into its 120 s). So the test waits for
// the server to have answered every frame of the paste (a ping behind them), the word of every refusal sent before that answer, not
// for the word to stop changing over a few seconds; and nothing bounds that wait in the product but the machine's pace, so its bound
// only bounds a hang, and the test's timeout is that bound and the suite's 120 s for the rest.
const pasteBound = 240_000
test('keys refused stay named across a tab switched and the area folded, after Neovim wrote the rest', async ({ page, api, state }) => {
  test.setTimeout(pasteBound + 120_000)
  await watchSockets(page)
  const config = join(state.home, '.config', 'nvim')
  mkdirSync(config, { recursive: true })
  writeFileSync(
    join(config, 'init.lua'),
    "local first = true\nvim.api.nvim_create_autocmd('InsertCharPre', { callback = function() if first then first = false vim.wait(5000) end end })\n",
  )
  const cwd = join(state.root, `files-vim-refused-kept-${test.info().repeatEachIndex}`)
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'end\n')
  writeFileSync(join(cwd, 'other.txt'), 'beta\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'vim-refused-kept', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    await pane.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    const area = page.locator('[data-editor-area]')
    const lines = area.locator('.monaco-editor .view-lines')
    const status = area.locator('[data-nvim-status]')
    const tab = (name: string) => area.locator('[data-editor-tab]', { hasText: name })
    await expect(lines).toContainText('end', { timeout: 30_000 })
    await area.locator('[data-editor-keymap]').click()
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    await lines.click()
    await page.keyboard.type('I')
    await expect(status.locator('[data-nvim-mode-words]')).toHaveText('-- INSERT --', { timeout: 15_000 })
    // Larger than its input buffer, in short lines, so that Neovim, out of its wait, has the rest at once (each character it inserts
    // sends its whole line).
    await page.keyboard.insertText('aaaaaaaaa\n'.repeat(2_000))
    const refused = status.locator('[data-nvim-refused]')
    // Every frame of the paste answered, its refusals with it: the word is whole.
    await serverCaughtUp(page, s.id, pasteBound)
    await expect(refused).toBeVisible()
    const said = (await refused.textContent()) ?? ''
    expect(said).not.toBe('')
    // Neovim out of its wait and done with what it had (the Escape after it); then written, its buffer clean and every key done.
    await page.keyboard.press('Escape')
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 60_000 })
    await page.keyboard.type(':w')
    await page.keyboard.press('Enter')
    await expect.poll(() => readFileSync(join(cwd, 'notes.txt'), 'utf8'), { timeout: 30_000 }).toMatch(/^(a+\n)+a*end\n$/)
    // The write and every answer to the keys before it had: the word is as it was.
    await serverCaughtUp(page, s.id)
    await expect(refused).toHaveText(said)
    // Another tab in front, then back: the word stays, and its Neovim with it.
    await pane.locator(`[data-file-node="${cwd}/other.txt"]`).click()
    await expect(lines).toContainText('beta', { timeout: 30_000 })
    await expect(refused).toHaveCount(0)
    await tab('notes.txt').click()
    await expect(refused).toHaveText(said, { timeout: 15_000 })
    // The area folded, then not: the same.
    await area.locator('[data-editor-fold]').click()
    await area.locator('[data-editor-unfold]').click()
    await expect(refused).toHaveText(said, { timeout: 15_000 })
    // Save from the close question still waits for the person's word on them; dismissed, the tab closes with nothing to ask.
    await area.locator('[data-editor-tab-active] [data-editor-close]').click()
    const question = page.locator('[role="dialog"]')
    await expect(question.locator('[data-editor-close-refused]')).toBeVisible()
    await question.locator('[data-editor-close-save]').click()
    await expect(tab('notes.txt')).toHaveCount(1)
    await question.getByRole('button', { name: 'Cancel' }).click()
    await refused.locator('[data-nvim-refused-dismiss]').click()
    await expect(refused).toHaveCount(0)
    await area.locator('[data-editor-tab-active] [data-editor-close]').click()
    await expect(tab('notes.txt')).toHaveCount(0, { timeout: 15_000 })
    expect(readFileSync(join(cwd, 'notes.txt'), 'utf8')).toMatch(/^(a+\n)+a*end\n$/)
  } finally {
    rmSync(join(config, 'init.lua'), { force: true })
    await api.stopSession(s.id)
  }
})

// An older owner (a `conductor host` from before the word that keys are done) sends no nvimSettled in its welcome and no settled
// event: the page takes an acknowledgement for that word, as it did. Here the page's connection drops both, as such an owner would
// not send them. The keymap turns once the keys are acknowledged, and Save in the close question writes and closes. Before, the page
// waited for a word that never came: the keymap stayed disabled, and Save gave up after ten seconds, its tab kept.
test('with an owner that never says keys are done, an acknowledgement stands for it: the keymap turns, Save closes', async ({ page, api, state }) => {
  await page.addInitScript(() => {
    const w = window as unknown as { offerSeen?: boolean; settledDropped?: number }
    const d = Object.getOwnPropertyDescriptor(WebSocket.prototype, 'onmessage')!
    Object.defineProperty(WebSocket.prototype, 'onmessage', {
      configurable: true,
      get() {
        return d.get!.call(this)
      },
      set(fn: ((ev: MessageEvent) => unknown) | null) {
        const self = this as WebSocket
        d.set!.call(
          self,
          fn &&
            ((ev: MessageEvent) => {
              if (ev.data instanceof ArrayBuffer) {
                const b = new Uint8Array(ev.data)
                if (b[0] === 0x03) {
                  let msg: Record<string, unknown> | null = null
                  try {
                    msg = JSON.parse(new TextDecoder().decode(b.subarray(1)))
                  } catch {
                    msg = null
                  }
                  if (msg?.t === 'nvim_event' && msg.kind === 'settled') {
                    w.settledDropped = (w.settledDropped ?? 0) + 1
                    return
                  }
                  if (msg?.t === 'welcome' && 'nvimSettled' in msg) {
                    w.offerSeen = true
                    delete msg.nvimSettled
                    const body = new TextEncoder().encode(JSON.stringify(msg))
                    const out = new Uint8Array(body.length + 1)
                    out[0] = 0x03
                    out.set(body, 1)
                    return fn.call(self, new MessageEvent('message', { data: out.buffer }))
                  }
                }
              }
              return fn.call(self, ev)
            }),
        )
      },
    })
  })
  const cwd = join(state.root, 'files-vim-older')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'alpha\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'vim-older', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    await pane.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    const area = page.locator('[data-editor-area]')
    const lines = area.locator('.monaco-editor .view-lines')
    const status = area.locator('[data-nvim-status]')
    const button = area.locator('[data-editor-keymap]')
    const question = page.locator('[role="dialog"]')
    await expect(lines).toContainText('alpha', { timeout: 30_000 })
    await button.click()
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    // This server offers the word; the page was made not to see it.
    expect(await page.evaluate(() => (window as unknown as { offerSeen?: boolean }).offerSeen)).toBe(true)
    const dot = area.locator('[data-editor-tab-active] [data-editor-dirty]')
    await lines.click()
    await page.keyboard.type('Axy')
    await page.keyboard.press('Escape')
    await expect(lines).toContainText('alphaxy', { timeout: 15_000 })
    await page.keyboard.type(':w')
    await page.keyboard.press('Enter')
    await expect.poll(() => readFileSync(join(cwd, 'notes.txt'), 'utf8'), { timeout: 15_000 }).toBe('alphaxy\n')
    await expect(dot).toHaveCount(0, { timeout: 15_000 })
    // Written and every key acknowledged: the keymap turns.
    await expect(button).toBeEnabled({ timeout: 10_000 })
    expect(await page.evaluate(() => (window as unknown as { settledDropped?: number }).settledDropped ?? 0)).toBeGreaterThan(0)
    // Save from the close question: written, and the tab closes.
    await page.keyboard.type('Azz')
    await page.keyboard.press('Escape')
    await expect(lines).toContainText('alphaxyzz', { timeout: 15_000 })
    await area.locator('[data-editor-tab-active] [data-editor-close]').click()
    await expect(question).toContainText('Save notes.txt?')
    await question.locator('[data-editor-close-save]').click()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(0, { timeout: 8_000 })
    expect(readFileSync(join(cwd, 'notes.txt'), 'utf8')).toBe('alphaxyzz\n')
  } finally {
    await api.stopSession(s.id)
  }
})

// Round 13, G3: a file another Vim left a swap file for opens read-only
// under the Neovim keymap, with a banner naming the swap file's writer and
// what applies. A Vim that died with unsaved text left this one: Recover
// reads that text in, :w keeps it, and Delete the swap file ends the banner.
test('a swap file left by a Vim that died opens read-only with a banner; Recover, write, Delete the swap file', async ({ page, api, state }) => {
  const cwd = join(state.root, 'files-swap')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'alpha\n')
  // A Vim with the server's home edits the file, writes its swap file, and dies.
  const swapDir = join(state.home, '.local', 'state', 'nvim', 'swap')
  const vim = spawn('nvim', ['--headless', '-c', 'normal! Ifrom the swap ', '-c', 'preserve', 'notes.txt'], {
    cwd,
    env: { PATH: process.env.PATH ?? '/usr/bin:/bin', HOME: state.home, LANG: 'C.UTF-8' },
    stdio: 'ignore',
  })
  const swapFile = () => (existsSync(swapDir) ? readdirSync(swapDir).find((f) => f.includes('files-swap') && f.endsWith('.swp')) : undefined)
  await expect.poll(swapFile, { timeout: 15_000, message: 'the dying Vim wrote its swap file' }).toBeTruthy()
  await page.waitForTimeout(500)
  vim.kill('SIGKILL')
  await new Promise((r) => vim.once('exit', r))
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'swap', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    await pane.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    const area = page.locator('[data-editor-area]')
    await expect(area.locator('.monaco-editor')).toBeVisible({ timeout: 30_000 })
    await area.locator('[data-editor-keymap]').click()
    const banner = area.locator('[data-editor-swap]')
    await expect(banner).toBeVisible({ timeout: 30_000 })
    await expect(banner).toHaveAttribute('data-swap-running', 'false')
    await expect(banner).toContainText('A swap file from a Vim that ended')
    await expect(banner).toContainText('with changes not written')
    await expect(banner.locator('[data-swap-choice]')).toHaveCount(3)
    const lines = area.locator('.monaco-editor .view-lines')
    await expect(lines).toContainText('alpha')
    // Recover reads the dead Vim's text in; the banner says to write, then delete.
    await banner.locator('[data-swap-choice="recover"]').click()
    await expect(lines).toContainText('from the swap alpha', { timeout: 15_000 })
    await expect(banner).toContainText('write the file (:w)')
    await lines.click()
    await page.keyboard.type(':w')
    await page.keyboard.press('Enter')
    await expect.poll(() => readFileSync(join(cwd, 'notes.txt'), 'utf8'), { timeout: 15_000 }).toBe('from the swap alpha\n')
    await banner.locator('[data-swap-choice="delete"]').click()
    await expect(banner).toHaveCount(0)
    await expect.poll(swapFile, { timeout: 15_000, message: 'the swap file is gone' }).toBeFalsy()
    // Neovim's own question shows as buttons: :confirm q over a change not written; Cancel keeps the tab.
    await lines.click()
    await page.keyboard.type('Ax')
    await page.keyboard.press('Escape')
    await page.keyboard.type(':confirm q')
    await page.keyboard.press('Enter')
    const confirm = area.locator('[data-nvim-confirm]')
    await expect(confirm).toContainText('Save changes', { timeout: 15_000 })
    await expect(confirm.locator('[data-nvim-confirm-choice]')).toHaveCount(3)
    await confirm.locator('[data-nvim-confirm-choice="c"]').click()
    await expect(confirm).toHaveCount(0)
    await expect(area.locator('[data-nvim-status]')).toBeVisible()
    await expect(lines).toContainText('from the swap alphax')
  } finally {
    await api.stopSession(s.id)
  }
})

// Round 13, G4: text that comes with no key press reaches Neovim too. A
// dead key's character or dictation arrives as inserted text (Playwright's
// insertText); an input method composes a word and commits it (Chromium's
// Input.imeSetComposition, then the commit): only the committed word is
// typed, not the composition on the way.
test('text with no key press, a dead key\'s character and an input method\'s word, reaches Neovim', async ({ page, api, state }) => {
  const cwd = join(state.root, 'files-ime')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'end\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'ime', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    await pane.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    const area = page.locator('[data-editor-area]')
    await expect(area.locator('.monaco-editor')).toBeVisible({ timeout: 30_000 })
    await area.locator('[data-editor-keymap]').click()
    const status = area.locator('[data-nvim-status]')
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    const lines = area.locator('.monaco-editor .view-lines')
    await lines.click()
    await page.keyboard.type('I')
    await expect(status.locator('[data-nvim-mode-words]')).toHaveText('-- INSERT --', { timeout: 15_000 })
    // Typed as fast as a machine sends keys, far past the 20 a quick burst once lost (the chat's bound, before Neovim's own).
    await page.keyboard.type('the quick brown fox jumps over the lazy dog ')
    await expect(lines).toContainText('the quick brown fox jumps over the lazy dog end', { timeout: 15_000 })
    // A dead key's é, or dictation: text with no key press.
    await page.keyboard.insertText('café ')
    await expect(lines).toContainText('dog café end', { timeout: 15_000 })
    // An input method: the composition is shown on the way and only the committed word is typed.
    const cdp = await page.context().newCDPSession(page)
    await cdp.send('Input.imeSetComposition', { text: 'に', selectionStart: 1, selectionEnd: 1 })
    await cdp.send('Input.imeSetComposition', { text: 'にほん', selectionStart: 3, selectionEnd: 3 })
    await cdp.send('Input.insertText', { text: '日本 ' })
    await expect(lines).toContainText('dog café 日本 end', { timeout: 15_000 })
    await expect(lines).not.toContainText('にほん')
    // No "Cannot edit in read-only editor" from Monaco: Neovim took it all.
    await expect(page.getByText('Cannot edit in read-only editor')).toHaveCount(0)
    await page.keyboard.press('Escape')
    await page.keyboard.type(':w')
    await page.keyboard.press('Enter')
    await expect.poll(() => readFileSync(join(cwd, 'notes.txt'), 'utf8'), { timeout: 15_000 }).toBe('the quick brown fox jumps over the lazy dog café 日本 end\n')
  } finally {
    await api.stopSession(s.id)
  }
})

// Round 13, G5: the local echo. Neovim here is slow on purpose (its config,
// in the server's home for this test alone, busy-waits 300 ms on each typed
// character), so what shows at once is the page's guess: plain characters
// in insert mode, marked while Neovim has not handled them, then settled
// with nothing doubled. An autopair (the config maps ( to ()<Left>) is not
// the guess: Neovim's text wins once, and guessing stops in that insert.
test('typing in insert mode shows at once and settles as Neovim has it; an autopair corrects once', async ({ page, api, state }) => {
  const config = join(state.home, '.config', 'nvim')
  mkdirSync(config, { recursive: true })
  writeFileSync(
    join(config, 'init.lua'),
    // A busy wait, as a slow link is: Neovim answers nothing meanwhile (:sleep would let it answer requests mid-key).
    "local uv = vim.uv or vim.loop\nvim.api.nvim_create_autocmd('InsertCharPre', { callback = function() local t = uv.hrtime() while uv.hrtime() - t < 3e8 do end end })\nvim.keymap.set('i', '(', '()<Left>')\n",
  )
  const cwd = join(state.root, 'files-echo')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'end\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'echo', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    await pane.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    const area = page.locator('[data-editor-area]')
    await expect(area.locator('.monaco-editor')).toBeVisible({ timeout: 30_000 })
    await area.locator('[data-editor-keymap]').click()
    const status = area.locator('[data-nvim-status]')
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    const lines = area.locator('.monaco-editor .view-lines')
    await lines.click()
    await page.keyboard.type('I')
    await expect(status.locator('[data-nvim-mode-words]')).toHaveText('-- INSERT --', { timeout: 15_000 })
    // Four characters, a space among them: Neovim needs 1.2 s for them; the page shows them at once, marked. The space is guessed
    // like a letter (it comes as <Space>, which held every word after it until Neovim's round trip).
    await page.keyboard.type('ab c')
    await expect(lines).toContainText('ab cend', { timeout: 250 })
    await expect(area.locator('.nvim-guess')).not.toHaveCount(0)
    // Settled: the marks go, nothing is doubled.
    await expect(area.locator('.nvim-guess')).toHaveCount(0, { timeout: 10_000 })
    await expect(lines).toContainText('ab cend')
    await expect(lines).not.toContainText('ab cab c')
    // The autopair: the guess "(" becomes Neovim's "()" once; the next character is Neovim's, between the two.
    await page.keyboard.type('(')
    await expect(lines).toContainText('ab c()end', { timeout: 10_000 })
    await page.keyboard.type('x')
    await expect(lines).toContainText('ab c(x)end', { timeout: 10_000 })
    await expect(area.locator('.nvim-guess')).toHaveCount(0)
    await page.keyboard.press('Escape')
    await expect(status.locator('[data-nvim-mode-words]')).toHaveText('', { timeout: 10_000 })
    await page.keyboard.type(':w')
    await page.keyboard.press('Enter')
    await expect.poll(() => readFileSync(join(cwd, 'notes.txt'), 'utf8'), { timeout: 15_000 }).toBe('ab c(x)end\n')
  } finally {
    rmSync(join(config, 'init.lua'), { force: true })
    await api.stopSession(s.id)
  }
})

// A tab left before its Neovim has opened lets that Neovim go once it opens: no tab takes it. Neovim here waits at its start until the
// test opens a gate (its config waits for a file), so the tab is left while its open is under way, whatever the machine's load. Before,
// that Neovim stayed until the connection ended: one of the four a connection may keep, and its swap file, which the file's next open
// met as another Vim's, read only behind a banner, and which outlived that tab's `:q` (#429's review round 2: the swap-file test below
// failed so under load, a tab switched while its open was slow).
test('a tab left before its Neovim opened lets that Neovim go, and the file opens again with no swap file', async ({ page, api, state }) => {
  await watchSockets(page)
  const gate = join(state.root, `nvim-gate-${test.info().repeatEachIndex}`)
  const config = join(state.home, '.config', 'nvim')
  mkdirSync(config, { recursive: true })
  // Blocking (uv.sleep, not vim.wait, which would answer the server's requests meanwhile): the open waits for the gate, 60 s at most.
  writeFileSync(
    join(config, 'init.lua'),
    `local uv = vim.uv or vim.loop\nlocal t = uv.hrtime()\nwhile not uv.fs_stat(${JSON.stringify(gate)}) and uv.hrtime() - t < 6e10 do uv.sleep(20) end\n`,
  )
  writeFileSync(gate, '')
  const dir = `files-vim-early-${test.info().repeatEachIndex}`
  const cwd = join(state.root, dir)
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'a.txt'), 'alpha\n')
  writeFileSync(join(cwd, 'b.txt'), 'beta\n')
  const swapDir = join(state.home, '.local', 'state', 'nvim', 'swap')
  const swaps = () => (existsSync(swapDir) ? readdirSync(swapDir).filter((f) => f.includes(dir)) : [])
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'vim-early', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    const area = page.locator('[data-editor-area]')
    const lines = area.locator('.monaco-editor .view-lines')
    const status = area.locator('[data-nvim-status]')
    const tab = (name: string) => area.locator('[data-editor-tab]', { hasText: name })
    await pane.locator(`[data-file-node="${cwd}/b.txt"]`).click()
    await expect(lines).toContainText('beta', { timeout: 30_000 })
    await area.locator('[data-editor-keymap]').click()
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    // The gate shut: a.txt's Neovim starts and waits. Its tab is left (b.txt in front again) before it has opened.
    rmSync(gate)
    await pane.locator(`[data-file-node="${cwd}/a.txt"]`).click()
    await expect(lines).toContainText('alpha', { timeout: 30_000 })
    await page.waitForFunction(() => !!(window as unknown as SocketSeam).nvimOpens?.some((o) => o.path.endsWith('/a.txt')), undefined, { timeout: 30_000 })
    await tab('b.txt').click()
    await expect(lines).toContainText('beta', { timeout: 30_000 })
    expect(await page.evaluate(() => !!(window as unknown as SocketSeam).nvimOpened?.some((o) => o.path.endsWith('/a.txt'))), "a.txt's Neovim opened before its tab was left").toBe(false)
    // The gate open: both Neovims open, b.txt's for its tab, a.txt's for none, and the page lets a.txt's go.
    writeFileSync(gate, '')
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    const early = await page.waitForFunction(() => (window as unknown as SocketSeam).nvimOpened?.find((o) => o.path.endsWith('/a.txt'))?.id, undefined, { timeout: 30_000 })
    const earlyId = (await early.jsonValue()) as string
    await expect
      .poll(() => page.evaluate((id) => !!(window as unknown as SocketSeam).nvimCloses?.includes(id), earlyId), { timeout: 15_000, message: "the Neovim opened for a tab already left is closed" })
      .toBe(true)
    await expect.poll(() => swaps().filter((f) => f.includes('a.txt')), { timeout: 30_000, message: "that Neovim's swap file goes with it" }).toEqual([])
    // a.txt shown again: a Neovim of its own, the file as it is, no other Vim's swap file in a banner.
    await tab('a.txt').click()
    await expect(lines).toContainText('alpha', { timeout: 30_000 })
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    await expect(area.locator('[data-editor-swap]')).toHaveCount(0)
  } finally {
    rmSync(join(config, 'init.lua'), { force: true })
    rmSync(gate, { force: true })
    await api.stopSession(s.id)
  }
})

// A tab whose Neovim buffer holds changes not written keeps its Neovim while
// another tab is in front: back in front, the changes are there and the
// editing goes on (it ended, before, and the next showing met its swap file
// in a banner). The tab wears the unsaved dot and the keymap waits for a save;
// closing it asks: Don't save drops the changes and the swap file with them,
// Save writes through Neovim's :w. Neovim's own :q leaves no swap file.
test('a Neovim tab keeps its changes not written while another is in front; closing asks; nothing leaves a swap file', async ({ page, api, state }) => {
  // A directory per repeat, and the swap files of that one alone: a Neovim of the last repeat still leaving is not this one's.
  const dir = `files-tabs-${test.info().repeatEachIndex}`
  const cwd = join(state.root, dir)
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'a.txt'), 'alpha\n')
  writeFileSync(join(cwd, 'b.txt'), 'beta\n')
  const swaps = () => swapFilesOf(state.home, dir)
  await watchSockets(page)
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'tabs', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    const area = page.locator('[data-editor-area]')
    const lines = area.locator('.monaco-editor .view-lines')
    const status = area.locator('[data-nvim-status]')
    const tab = (name: string) => area.locator('[data-editor-tab]', { hasText: name })
    // A tab that comes to the front (opened, or shown again as another closes) has its Neovim's answer before the test goes on: one
    // left before it stays running on the server, its swap file with it (#500).
    const nvimUp = async (name: string, before: number) => {
      await expect.poll(() => nvimOpens(page, name), { timeout: 30_000 }).toBeGreaterThan(before)
      await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    }
    const open = async (name: string, text: string) => {
      const before = await nvimOpens(page, name)
      await pane.locator(`[data-file-node="${cwd}/${name}"]`).click()
      await expect(lines).toContainText(text, { timeout: 30_000 })
      await nvimUp(name, before)
    }
    await pane.locator(`[data-file-node="${cwd}/a.txt"]`).click()
    await expect(area.locator('.monaco-editor')).toBeVisible({ timeout: 30_000 })
    await area.locator('[data-editor-keymap]').click()
    await nvimUp('a.txt', 0)
    await lines.click()
    await page.keyboard.type('Ixx ')
    await page.keyboard.press('Escape')
    await expect(lines).toContainText('xx alpha')
    // Neovim says the buffer holds a change: the dot, and the keymap waits.
    await expect(tab('a.txt').locator('[data-editor-dirty]')).toBeVisible({ timeout: 15_000 })
    await expect(area.locator('[data-editor-keymap]')).toBeDisabled()
    // Another tab in front, then back: the change is there, no swap file banner, and the editing goes on.
    await open('b.txt', 'beta')
    await expect(tab('a.txt').locator('[data-editor-dirty]')).toBeVisible()
    await tab('a.txt').click()
    await expect(lines).toContainText('xx alpha', { timeout: 15_000 })
    // Its own Neovim reports its mode once it listens again (the status starts empty for each tab): only then are keys its.
    await expect(status).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
    await expect(area.locator('[data-editor-swap]')).toHaveCount(0)
    await lines.click()
    await page.keyboard.type('A!')
    await page.keyboard.press('Escape')
    await expect(lines).toContainText('xx alpha!', { timeout: 15_000 })
    // Closing it asks; Don't save drops the changes, the file is as it was, and its swap file goes.
    const question = page.locator('[role="dialog"]', { hasText: 'Save a.txt?' })
    // b.txt comes to the front as a.txt closes, with a Neovim of its own.
    let before = await nvimOpens(page, 'b.txt')
    await area.locator('[data-editor-tab-active] [data-editor-close]').click()
    await expect(question).toBeVisible()
    await question.locator('[data-editor-discard]').click()
    await expect(tab('a.txt')).toHaveCount(0)
    await nvimUp('b.txt', before)
    expect(readFileSync(join(cwd, 'a.txt'), 'utf8')).toBe('alpha\n')
    // A Neovim's swap file goes as it leaves, which the server lets take nvimEndBound.
    await expect.poll(() => swaps().filter((f) => f.includes('a.txt')), { timeout: nvimEndBound, message: "a.txt's swap file goes with its changes" }).toEqual([])
    await expect(area.locator('[data-editor-keymap]')).toBeEnabled()
    // Open again: no banner, the file as it is.
    await open('a.txt', 'alpha')
    await expect(area.locator('[data-editor-swap]')).toHaveCount(0)
    // Save from the question writes through Neovim and closes the tab.
    await lines.click()
    await page.keyboard.type('Ayy')
    await page.keyboard.press('Escape')
    await expect(tab('a.txt').locator('[data-editor-dirty]')).toBeVisible({ timeout: 15_000 })
    before = await nvimOpens(page, 'b.txt')
    await area.locator('[data-editor-tab-active] [data-editor-close]').click()
    await question.locator('[data-editor-close-save]').click()
    await expect(tab('a.txt')).toHaveCount(0, { timeout: 15_000 })
    expect(readFileSync(join(cwd, 'a.txt'), 'utf8')).toBe('alphayy\n')
    // Neovim's own :q on b.txt (in front again as a.txt closed): the tab closes and no swap file is left.
    await tab('b.txt').click()
    await expect(lines).toContainText('beta', { timeout: 15_000 })
    await nvimUp('b.txt', before)
    await lines.click()
    await page.keyboard.type(':q')
    await page.keyboard.press('Enter')
    await expect(area.locator('[data-editor-tab]')).toHaveCount(0, { timeout: 15_000 })
    // Its tab closes on Neovim's word that it is leaving (conductor_leaving), before it has left and removed its swap file.
    await expect.poll(swaps, { timeout: nvimEndBound, message: 'no swap file left behind' }).toEqual([])
  } finally {
    await api.stopSession(s.id)
  }
})

// A control link may edit, but Neovim is kept for the session's own window
// (the owner's). The guest gets the editor with Save, and the Keys button
// shows Neovim's note saying so rather than offering it.
test('a control-link guest edits in Monaco with Save, but is not offered Neovim', async ({ page, api, state, browser }) => {
  const cwd = join(state.root, 'files-vim-link')
  mkdirSync(cwd, { recursive: true })
  const file = join(cwd, 'notes.txt')
  writeFileSync(file, 'alpha\nbeta\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'vim-link', cwd })
  try {
    // The owner's own window is offered Neovim.
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    await pane.locator(`[data-file-node="${file}"]`).click()
    const area = page.locator('[data-editor-area]')
    await expect(area.locator('.monaco-editor')).toBeVisible({ timeout: 30_000 })
    await area.locator('[data-editor-keymap]').click()
    await expect(area.locator('[data-nvim-status]')).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })

    // A control link's guest may edit but gets no Neovim.
    const link = await api.ok<{ token: string }>('POST', `/api/sessions/${s.id}/links`, { role: 'control', ttlSeconds: 3600 })
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
    await ctx.addInitScript(() => localStorage.setItem('conductor.displayName', 'Jane'))
    const guest = await ctx.newPage()
    await guest.goto(`/join/${link.token}`)
    await guest.getByRole('button', { name: /^Join/ }).first().click()
    const aside = guest.locator('[data-files-aside]')
    await expect(aside.locator(`[data-file-node="${file}"]`)).toBeVisible({ timeout: 30_000 })
    await aside.locator(`[data-file-node="${file}"]`).click()
    const garea = guest.locator('[data-editor-area]')
    await expect(garea.locator('.monaco-editor .view-lines')).toContainText('alpha', { timeout: 30_000 })
    // It may edit (Save shows, not Read only).
    await expect(garea.locator('[data-editor-save]')).toBeVisible()
    await expect(garea.locator('[data-editor-readonly]')).toHaveCount(0)
    // The Keys button is Monaco's; choosing Neovim only shows why it is kept for the owner.
    await expect(garea.locator('[data-editor-keymap]')).toHaveAttribute('data-editor-keymap', 'default')
    await garea.locator('[data-editor-keymap]').click()
    await expect(garea.locator('[data-editor-keymap-note]')).toContainText("kept for the session's own window", { timeout: 15_000 })
    await expect(garea.locator('[data-nvim-status]')).toHaveAttribute('data-nvim-mode', 'off')
    await ctx.close()
  } finally {
    await api.stopSession(s.id)
  }
})
