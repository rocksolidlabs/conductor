import { execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Request } from '@playwright/test'
import { expect, showView, test, type Session } from './fixtures'

// The Markdown preview (#166): a `.md` file in the editor shown rendered by
// the header's Preview, a Mermaid block drawn, raw HTML shown as text, a
// relative link opening its file in a tab, the choice kept per tab, Alt+P
// bringing the source back (with Neovim holding the keys too, where nvim is
// on PATH). A repository file is untrusted: an image on the web is held, a
// Mermaid image node fails inside the sandboxed renderer (its frame's policy
// lets nothing be fetched), an HTML image in a label and a url( in a
// diagram's CSS are stripped, every diagram is shown as an image (an img
// with a data URL, from which a browser loads nothing), and the page makes
// no request outside its own origin while it shows all four. An empty link and a fragment link never
// navigate the page. A reload of the source while previewing refreshes the
// preview. Mermaid draws SVG only in a browser (vitest's node environment
// has no DOM), so the drawing is checked here.
const hasNvim = (() => {
  try {
    execFileSync('sh', ['-c', 'command -v nvim'], { stdio: 'ignore' })
    return true
  } catch {
    return false
  }
})()

test('a Markdown file previews rendered with its diagram drawn and nothing fetched from outside; a link opens its file; Alt+P brings the source back', async ({ page, api, state }) => {
  const cwd = join(state.root, 'markdown-preview')
  mkdirSync(join(cwd, 'docs'), { recursive: true })
  writeFileSync(
    join(cwd, 'README.md'),
    [
      '# Signals',
      '',
      'The crew reads [the users guide](docs/users.md) first, then [the table](#the-table); [a placeholder]() leads nowhere.',
      '',
      '<script>alert(1)</script>',
      '',
      '![tracker](https://attacker.invalid/pixel.png)',
      '',
      '## The table',
      '',
      '| Signal | Means |',
      '|---|---|',
      '| green | go |',
      '',
      '- [x] built',
      '- [ ] shipped',
      '',
      '```mermaid',
      'flowchart LR',
      '  A[Yard] --> B[Roundhouse]',
      '```',
      '',
      '```mermaid',
      'flowchart LR',
      '  A@{ "img": "https://attacker.invalid/node.png", h: 60 }',
      '```',
      '',
      '```mermaid',
      '%%{init: {"themeCSS": ".node rect { fill: url(https://attacker.invalid/fill.svg#p); } @import url(https://attacker.invalid/x.css);"}}%%',
      'flowchart LR',
      '  A --> B',
      '```',
      '',
      '```mermaid',
      'flowchart LR',
      '  A["<img src=https://attacker.invalid/label.png>"] --> B',
      '```',
      '',
      '```mermaid',
      'flowchart LR',
      '  Docs[Docs] --> Read[Read]',
      '  click Docs href "https://attacker.invalid/docs"',
      '```',
      '',
      '```go',
      'package main',
      '```',
      '',
    ].join('\n'),
  )
  const users = join(cwd, 'docs', 'users.md')
  writeFileSync(users, '# Users\n\n```mermaid\nsequenceDiagram\n  Yard->>Roundhouse: who is free?\n```\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'markdown', cwd })
  // Every request the page makes to another origin (the file names attacker.invalid four ways). The browser reports one the
  // frame's policy refused as a request that failed with ERR_BLOCKED_BY_CSP before anything was sent; nothing else may show.
  const outside: Request[] = []
  let origin = ''
  page.on('request', (r) => {
    const u = new URL(r.url())
    if (u.protocol === 'data:' || u.protocol === 'blob:' || u.protocol === 'about:') return
    // The first request is the page itself, from the server: its origin is the one everything else must have.
    if (!origin) origin = u.origin
    else if (u.origin !== origin) outside.push(r)
  })
  // Chromium names the refusal `csp` (its blocked reason) through Playwright; the net error name is accepted too.
  const refused = (r: Request) => /^(csp|net::ERR_BLOCKED_BY_CSP)$/.test(r.failure()?.errorText ?? '')
  const leaked = () => outside.filter((r) => !refused(r)).map((r) => `${r.url()} ${r.failure()?.errorText ?? 'sent'}`)
  try {
    await page.goto(`/sessions/${s.id}`)
    // A mark on the window: a navigation of the page would lose it.
    await page.evaluate(() => ((window as unknown as { __alive: boolean }).__alive = true))
    const alive = () => page.evaluate(() => (window as unknown as { __alive?: boolean }).__alive === true)
    await showView(page, 'explorer')
    const pane = page.locator('[data-files-mode]')
    const box = pane.locator('[data-files-box] input, input[data-files-box]').first()
    await box.fill('README.md')
    await box.press('Enter')
    const area = page.locator('[data-editor-area]')
    await expect(area).toHaveAttribute('data-editor-area', 'open')
    await expect(area.locator('[data-editor-pos]')).toHaveText('Ln 1, Col 1', { timeout: 30_000 })
    // The source first; Preview renders it: the heading, the table, the task list, the script as text, the code highlighted.
    const toggle = area.locator('[data-editor-preview]')
    await expect(toggle).toHaveAttribute('data-editor-preview', 'off')
    await expect(toggle).toHaveText('Preview')
    await toggle.click()
    await expect(toggle).toHaveAttribute('data-editor-preview', 'on')
    const preview = area.locator('[data-md-preview]')
    await expect(preview.locator('h1')).toHaveText('Signals')
    await expect(preview.locator('td').first()).toHaveText('green')
    await expect(preview.locator('li.task input[type=checkbox]')).toHaveCount(2)
    await expect(preview.locator('li.task input:checked')).toHaveCount(1)
    await expect(preview).toContainText('<script>alert(1)</script>')
    await expect(preview.locator('script')).toHaveCount(0)
    await expect(preview.locator('pre.shiki')).toContainText('package main', { timeout: 30_000 })
    await expect(area.locator('[data-editor-pos]')).toHaveCount(0)
    // The image on the web is held as its alt text. The diagrams are drawn in a sandboxed frame: the plain one, the one whose
    // theme CSS names a url( and an @import (stripped from its style), the one with an HTML image in a label (stripped) and
    // the one whose node is a link (the link gone, the node kept) come back as SVG; the image node fails there, its URL
    // refused by the frame's policy, and keeps its source with Mermaid's words.
    await expect(preview.locator('.md-image-held')).toHaveText('tracker')
    await expect(preview.locator('img:not([src^="data:image/svg+xml"])')).toHaveCount(0)
    const diagrams = preview.locator('[data-mermaid-rendered] img[src^="data:image/svg+xml"]')
    // Each diagram as the browser has it: decoded (a width once the image document parsed) and its SVG text.
    const drawn = () => diagrams.evaluateAll((els) => els.map((e) => ({ width: (e as HTMLImageElement).naturalWidth, svg: decodeURIComponent((e.getAttribute('src') ?? '').split(',').slice(1).join(',')) })))
    await expect(diagrams.first()).toBeVisible({ timeout: 30_000 })
    await expect(preview.locator('[data-mermaid-rendered]')).toHaveCount(4, { timeout: 30_000 })
    await expect(diagrams).toHaveCount(4)
    await expect.poll(async () => (await drawn()).map((d) => d.width > 0)).toEqual([true, true, true, true])
    const svgs = await drawn()
    expect(svgs[0]!.svg).toContain('Roundhouse')
    expect(svgs[3]!.svg).toContain('Docs')
    expect(svgs[3]!.svg).toContain('Read')
    expect(svgs[3]!.svg).not.toContain('attacker')
    for (const d of svgs) {
      expect(d.svg).toContain('<style')
      // A url( may point into the document (an arrowhead marker), never outside it. A match shows with what is around it.
      const bad = /url\s*\(\s*["']?\s*[^#"'\s)]|@import|<image|<a |<script|<foreignObject|\son[a-z]+=/i.exec(d.svg)
      expect(bad ? d.svg.slice(Math.max(0, bad.index - 80), bad.index + 120) : '').toBe('')
    }
    await expect(preview.locator('[data-mermaid-error]')).toHaveCount(1, { timeout: 30_000 })
    await expect(preview.locator('[data-mermaid-error]')).toContainText('Mermaid could not draw this block')
    await expect(preview.locator('[data-mermaid-error]')).toContainText('attacker.invalid/node.png')
    await expect(preview.locator('[data-mermaid-rendered] svg, [data-mermaid-rendered] a, [data-mermaid-rendered] image')).toHaveCount(0)
    const frame = page.locator('iframe[data-mermaid-frame]')
    await expect(frame).toHaveCount(1)
    await expect(frame).toHaveAttribute('sandbox', 'allow-scripts')
    expect(leaked()).toEqual([])
    expect(outside.map((r) => r.url())).toContain('https://attacker.invalid/node.png')
    // An empty link and a fragment link never navigate the page: the fragment scrolls to its heading in the preview.
    await preview.locator('a', { hasText: 'a placeholder' }).click()
    await preview.locator('a[href="#the-table"]').click()
    expect(await alive()).toBe(true)
    expect(new URL(page.url()).hash).toBe('')
    await expect(preview.locator('h2#md-the-table')).toBeInViewport()
    // A relative link opens its file in a tab of its own, previewable in turn; the README's tab keeps its preview; switching
    // between the two previews at once (their requests to the one frame interleaving) leaves both drawn.
    await preview.locator('a[data-md-link]').click()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(2)
    await expect(area.locator('[data-editor-tab-active]')).toContainText('users.md')
    await expect(toggle).toHaveAttribute('data-editor-preview', 'off')
    await toggle.click()
    await expect(preview.locator('h1')).toHaveText('Users')
    await area.locator('[data-editor-tab]', { hasText: 'README.md' }).click()
    await expect(toggle).toHaveAttribute('data-editor-preview', 'on')
    await expect(preview.locator('h1')).toHaveText('Signals')
    await expect(diagrams).toHaveCount(4, { timeout: 30_000 })
    await expect.poll(async () => (await drawn()).map((d) => d.width > 0)).toEqual([true, true, true, true])
    await area.locator('[data-editor-tab]', { hasText: 'users.md' }).click()
    await expect(preview.locator('h1')).toHaveText('Users')
    await expect(diagrams).toHaveCount(1, { timeout: 30_000 })
    await expect.poll(async () => (await drawn()).map((d) => d.width > 0)).toEqual([true])
    expect((await drawn())[0]!.svg).toContain('who is free?')
    // Alt+P in the preview brings the source back; Alt+P in the text (Monaco's own action: Monaco keeps a chord it sees from the
    // page) shows the preview again, and once more the source.
    await preview.click()
    await page.keyboard.press('Alt+KeyP')
    await expect(toggle).toHaveAttribute('data-editor-preview', 'off')
    await expect(area.locator('[data-md-preview]')).toHaveCount(0)
    await expect(area.locator('[data-code-editor]')).toBeVisible()
    await expect(area.locator('[data-editor-pos]')).toBeVisible()
    const lines = area.locator('.monaco-editor .view-lines')
    await lines.click()
    await page.keyboard.press('Alt+KeyP')
    await expect(toggle).toHaveAttribute('data-editor-preview', 'on')
    await expect(preview.locator('h1')).toHaveText('Users')
    await preview.click()
    await page.keyboard.press('Alt+KeyP')
    await expect(toggle).toHaveAttribute('data-editor-preview', 'off')
    // The preview shows the editor's text, unsaved edits included; a reload of the source while previewing (the file changed
    // on disk, a save refused) shows the file as read again, the edits gone.
    await lines.click()
    await page.keyboard.press('Control+End')
    await page.keyboard.type('\nEdited here\n')
    await expect(area.locator('[data-editor-dirty]')).toHaveCount(1)
    await toggle.click()
    await expect(preview).toContainText('Edited here')
    await toggle.click()
    writeFileSync(users, '# Users\n\nFrom the agent\n')
    await api.ok('POST', `/api/sessions/${s.id}/events`, { type: 'file', op: 'edit', path: 'docs/users.md', tool: 'Edit' })
    await lines.click()
    await page.keyboard.press('Control+s')
    const conflict = area.locator('[data-editor-conflict]')
    await expect(conflict).toContainText('Changed on disk since you opened it', { timeout: 15_000 })
    await toggle.click()
    await expect(preview).toContainText('Edited here')
    await conflict.locator('[data-editor-reload]').click()
    await expect(conflict).toHaveCount(0)
    await expect(preview).toContainText('From the agent', { timeout: 15_000 })
    await expect(preview).not.toContainText('Edited here')
    await expect(area.locator('[data-editor-dirty]')).toHaveCount(0)
    await toggle.click()
    await expect(toggle).toHaveAttribute('data-editor-preview', 'off')
    // With Neovim holding the keys, Alt+P is still the preview's: it never reaches Neovim.
    if (hasNvim) {
      await area.locator('[data-editor-keymap]').click()
      await expect(area.locator('[data-editor-keymap]')).toHaveAttribute('data-editor-keymap', 'nvim')
      await expect(area.locator('[data-nvim-status]')).toHaveAttribute('data-nvim-mode', /^(n|normal)$/, { timeout: 30_000 })
      await lines.click()
      await page.keyboard.press('Alt+KeyP')
      await expect(toggle).toHaveAttribute('data-editor-preview', 'on')
      await expect(preview.locator('h1')).toHaveText('Users')
      await preview.click()
      await page.keyboard.press('Alt+KeyP')
      await expect(toggle).toHaveAttribute('data-editor-preview', 'off')
      await expect(area.locator('[data-nvim-status]')).toHaveAttribute('data-nvim-mode', /^(n|normal)$/)
    }
    expect(await alive()).toBe(true)
    expect(leaked()).toEqual([])
  } finally {
    await api.stopSession(s.id)
  }
})
