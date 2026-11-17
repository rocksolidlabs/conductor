import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Frame, Page } from '@playwright/test'
import { expect, type Api } from './fixtures'

/**
 * A managed crew whose project manifest serves the preview fixture (web/e2e/fixtures/preview/serve.py) on the tree's own port, done
 * by the stub worker, with a check that holds the attempt open until the spec writes its gate: the pattern of manifest.spec.ts,
 * shared by the preview specs (preview-capture.spec.ts, and the candidate's preview and the repair loop after it). And the test seam
 * of the pane's screenshots (C9): Playwright's own screenshot of the frame's box, handed in through `window.__conductorPreviewSeam`
 * by an init script, so the moment's comparisons run on real pixels without the tab capture's prompt.
 */

/** The fixture's service, run by python3 in the tree. */
export const PREVIEW_SERVICE = fileURLToPath(new URL('./fixtures/preview/serve.py', import.meta.url))

/** A receipt of an attempt's steps, as far as the specs read it. */
export interface Step {
  phase: string
  id: string
  k: number
  state: string
}

/** What a spec made, to end after it: crews, runs and scratch directories. */
export interface Made {
  crews: string[]
  runs: string[]
  dirs: string[]
}

export const made = (): Made => ({ crews: [], runs: [], dirs: [] })

/** Ends what the spec made: its runs stopped, its crews deleted, its directories removed. */
export async function endMade(api: Api, m: Made): Promise<void> {
  for (const id of m.runs.splice(0)) await api.stopRun(id)
  for (const id of m.crews.splice(0)) await api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`)
  for (const dir of m.dirs.splice(0)) rmSync(dir, { recursive: true, force: true })
}

/** A directory of the spec's own, outside the server's allowed root, for the gate its check waits on. */
export function scratchDir(m: Made, name: string): string {
  const dir = mkdtempSync(join(tmpdir(), `conductor-e2e-${name}-`))
  m.dirs.push(dir)
  return dir
}

/**
 * Saves a managed crew with one task, build, done by the stub worker: the fixture's service on the tree's port `web` with a URL
 * probe and the preview, and a check that waits for `gate` (a file) before it passes, so the attempt stays open and its preview with it.
 */
export async function previewCrew(api: Api, m: Made, name: string, gate: string, preview = 'http://127.0.0.1:${CONDUCTOR_PORT_WEB}/'): Promise<string> {
  const body = {
    name,
    goal: 'preview the fixture',
    cwd: '',
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: false,
    yolo: null,
    members: [{ name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } }],
    tasks: [
      {
        id: 'build',
        member: 'builder',
        kind: 'work',
        instructions: 'Build it.',
        checks: [{ id: 'gate', argv: ['sh', '-c', 'until [ -e "$1" ]; do sleep 0.2; done; echo gate open', 'sh', gate], timeoutSeconds: 900 }],
      },
    ],
    manifest: {
      ports: ['web'],
      services: [{ id: 'web', argv: ['python3', PREVIEW_SERVICE, '${CONDUCTOR_PORT_WEB}'] }],
      ready: [{ id: 'up', url: 'http://127.0.0.1:${CONDUCTOR_PORT_WEB}/ready', intervalSeconds: 1, timeoutSeconds: 60 }],
      preview,
    },
  }
  const id = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', body)).crew.id
  m.crews.push(id)
  return id
}

export async function launch(api: Api, m: Made, crewId: string): Promise<string> {
  const run = await api.launchCrew(crewId)
  m.runs.push(run.id)
  expect(run.managed, 'launched as a managed run').toBeTruthy()
  return run.id
}

/** An attempt's receipts; none while the attempt is not in the ledger yet (`404 not_found`). */
export async function steps(api: Api, run: string, n: number, task = 'build'): Promise<Step[]> {
  const path = `/api/runs/${encodeURIComponent(run)}/tasks/${task}/attempts/${n}/steps`
  const r = await api.call<{ steps?: Step[]; error?: { code?: string } }>('GET', path)
  if (r.status === 404 && r.body?.error?.code === 'not_found') return []
  if (r.status >= 300) throw new Error(`GET ${path}: ${r.status} ${JSON.stringify(r.body)}`)
  return r.body.steps ?? []
}

/** Polls the receipt phase:id:k until it is in `state`. */
export async function stepIn(api: Api, run: string, n: number, key: string, state: string, task = 'build'): Promise<void> {
  await expect
    .poll(async () => (await steps(api, run, n, task)).find((s) => `${s.phase}:${s.id}:${s.k}` === key)?.state ?? 'absent', { timeout: 120_000, intervals: [250], message: `the receipt ${key} is ${state}` })
    .toBe(state)
}

/**
 * The pane's screenshots through the seam: each frame Playwright's own screenshot of the frame's box (the rect the pane asks for, in
 * the page's CSS pixels), as a base64 PNG. Set before the page's first script runs, as the app reads it once, as it loads.
 */
export async function previewSeam(page: Page): Promise<void> {
  await page.exposeFunction('conductorPreviewShot', async (r: { x: number; y: number; width: number; height: number }) => {
    if (!(r.width > 0 && r.height > 0)) return ''
    return (await page.screenshot({ clip: r, animations: 'allow', caret: 'initial' })).toString('base64')
  })
  await page.addInitScript(() => {
    if (window.top !== window) return
    ;(window as unknown as { __conductorPreviewSeam: unknown }).__conductorPreviewSeam = {
      frame: (r: unknown) => (window as unknown as { conductorPreviewShot(r: unknown): Promise<string> }).conductorPreviewShot(r),
    }
  })
}

/** The previewed page's frame once the pane has framed it at `origin`. */
export async function previewFrame(page: Page, origin: string): Promise<Frame> {
  await expect(page.locator('[data-preview-frame]')).toBeVisible()
  let frame: Frame | undefined
  await expect.poll(() => (frame = page.frames().find((f) => f.url().startsWith(origin))) !== undefined, { message: `a frame at ${origin}` }).toBe(true)
  return frame!
}

type Outlined = Window & { outlined(): string; selecting(): boolean }

/**
 * Picks an element in selection mode, as a person does: the pointer on `selector`'s centre, Down pressed `downs` times through what is
 * stacked under it, Shift+Down `siblings` times to its next siblings, until the outline names `outline`; then Enter. The keys go to
 * the frame, which has the focus.
 */
export async function pick(page: Page, frame: Frame, selector: string, outline: string, o: { downs?: number; siblings?: number; click?: boolean } = {}): Promise<void> {
  await frame.waitForFunction(() => (window as unknown as Outlined).selecting())
  await page.locator('[data-preview-frame]').focus()
  const box = await frame.locator(selector).boundingBox()
  expect(box, `${selector} is drawn`).not.toBeNull()
  const x = box!.x + box!.width / 2
  const y = box!.y + box!.height / 2
  await page.mouse.move(x, y)
  await frame.waitForFunction(() => (window as unknown as Outlined).outlined() !== 'none')
  if (o.click) {
    await page.mouse.click(x, y)
    return
  }
  for (let i = 0; i < (o.downs ?? 0); i++) await page.keyboard.press('ArrowDown')
  for (let i = 0; i < (o.siblings ?? 0); i++) await page.keyboard.press('Shift+ArrowDown')
  await frame.waitForFunction((want) => (window as unknown as Outlined).outlined() === want, outline)
  await page.keyboard.press('Enter')
}
