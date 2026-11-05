import { describe, expect, it } from 'vitest'
import { activateTab, closeAllTabs, closeTab, cycleTab, editorHeight, emptyTabs, openPreview, openTab, readSplit, SPLIT_DEFAULT, SPLIT_KEY, tabsToClose, tabTitle, takeFocus, takeLine, toggleCollapse, toggleFold, togglePreview, writeSplit } from './editorTabs'

describe('the editor tabs', () => {
  it('opens one tab per file, brings an open one to the front at the line asked for, and unfolds', () => {
    let s = openTab(emptyTabs(), 'file', '/r/internal/api/users.go', 14)
    expect(s.tabs.map((t) => t.title)).toEqual(['users.go'])
    expect(s.active).toBe('file:/r/internal/api/users.go')
    s = openTab(s, 'file', '/r/README.md')
    s = toggleFold(s)
    expect(s.folded).toBe(true)
    s = openTab(s, 'file', '/r/internal/api/users.go', 20)
    expect(s.tabs).toHaveLength(2)
    expect(s.active).toBe('file:/r/internal/api/users.go')
    expect(s.tabs[0]!.line).toBe(20)
    expect(s.folded).toBe(false)
    expect(takeLine(s, s.active!).tabs[0]!.line).toBeUndefined()
    const d = openTab(emptyTabs(), 'diff', '/r/a.go', undefined, { status: 'M', added: 3, removed: 1, against: 'working directory vs HEAD' })
    expect(d.tabs[0]).toMatchObject({ id: 'diff:/r/a.go', kind: 'diff', title: 'a.go', meta: { status: 'M', added: 3, removed: 1 } })
    expect(openTab(d, 'diff', '/r/a.go').tabs[0]!.meta?.added).toBe(3)
    expect(tabTitle('url', 'http://localhost:3000/v1/users?limit=5')).toBe('localhost:3000')
    expect(tabTitle('url', 'not a url')).toBe('not a url')
  })

  it('closes a tab and gives its place to the next one, else the one before; the last close empties the area', () => {
    let s = openTab(openTab(openTab(emptyTabs(), 'file', '/a'), 'file', '/b'), 'file', '/c')
    s = activateTab(s, 'file:/b')
    s = closeTab(s, 'file:/b')
    expect(s.tabs.map((t) => t.path)).toEqual(['/a', '/c'])
    expect(s.active).toBe('file:/c')
    s = closeTab(s, 'file:/c')
    expect(s.active).toBe('file:/a')
    s = closeTab(s, 'file:/zzz')
    expect(s.tabs).toHaveLength(1)
    s = closeTab(s, 'file:/a')
    expect(s).toEqual(emptyTabs())
    expect(closeAllTabs()).toEqual(emptyTabs())
    expect(activateTab(s, 'file:/nope')).toBe(s)
  })

  it('cycles with Ctrl+Tab round the end, both ways', () => {
    let s = openTab(openTab(openTab(emptyTabs(), 'file', '/a'), 'file', '/b'), 'file', '/c')
    s = cycleTab(s)
    expect(s.active).toBe('file:/a')
    s = cycleTab(s, -1)
    expect(s.active).toBe('file:/c')
    expect(cycleTab(openTab(emptyTabs(), 'file', '/a')).active).toBe('file:/a')
    expect(toggleFold(emptyTabs()).folded).toBe(false)
  })

  it('keeps the split per browser within its ends, and leaves the terminal its six lines', () => {
    const store = new Map<string, string>()
    const storage = { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => void store.set(k, v) }
    expect(readSplit(storage)).toBe(SPLIT_DEFAULT)
    writeSplit(storage, 0.95)
    expect(store.get(SPLIT_KEY)).toBe('0.85')
    writeSplit(storage, 0.05)
    expect(readSplit(storage)).toBe(0.2)
    expect(readSplit(null)).toBe(SPLIT_DEFAULT)
    // 900 px tall, 20 px lines, 60 px reserved: 60 % is 540 and fits; 85 % does not, the terminal keeps 120.
    expect(editorHeight(900, 0.6, 20, 60)).toBe(540)
    expect(editorHeight(900, 0.85, 20, 60)).toBe(720)
    expect(editorHeight(200, 0.6, 20, 60)).toBe(20)
  })
})

describe('the keymap setting', () => {
  it('reads nvim or default and writes what was chosen', async () => {
    const { readKeymap, writeKeymap, KEYMAP_KEY, nvimUnavailableWords } = await import('./editorTabs')
    const store = new Map<string, string>()
    const storage = { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => void store.set(k, v) }
    expect(readKeymap(storage)).toBe('default')
    writeKeymap(storage, 'nvim')
    expect(store.get(KEYMAP_KEY)).toBe('nvim')
    expect(readKeymap(storage)).toBe('nvim')
    store.set(KEYMAP_KEY, 'vim')
    expect(readKeymap(storage)).toBe('default')
    expect(readKeymap(null)).toBe('default')
    expect(readKeymap({ getItem: () => { throw new Error('no') } })).toBe('default')
    expect(nvimUnavailableWords({ welcome: true, nvim: true, fileEdit: true })).toBe('')
    expect(nvimUnavailableWords({ welcome: true, nvim: false, fileEdit: true, machine: 'lan-box' })).toBe('Neovim is not installed on lan-box')
    expect(nvimUnavailableWords({ welcome: true, nvim: true, fileEdit: false })).toMatch(/View only/)
    expect(nvimUnavailableWords({ welcome: false, nvim: true, fileEdit: true })).toBe('Not connected')
    // A link's controller may edit but gets no Neovim; the words say so, not "not installed".
    expect(nvimUnavailableWords({ welcome: true, nvim: false, fileEdit: true, ownerOnly: true, machine: 'lan-box' })).toMatch(/kept for the session's own window/)
  })

  it('says why a file the owner marked read only is not edited, and nothing for any other', async () => {
    const { fileReadOnlyWords } = await import('./editorTabs')
    expect(fileReadOnlyWords({ readOnly: true })).toMatch(/^Read only here: a repository's \.git/)
    expect(fileReadOnlyWords({ readOnly: false })).toBe('')
    expect(fileReadOnlyWords({})).toBe('')
    expect(fileReadOnlyWords(undefined)).toBe('')
    expect(fileReadOnlyWords(null)).toBe('')
  })

  it('says why a file was refused: viewing off, git\'s files for controllers, or files for controllers', async () => {
    const { refusedWords } = await import('./editorTabs')
    expect(refusedWords('file viewing is disabled').title).toBe('File viewing is off for this session')
    const git = refusedWords("a repository's .git and git's settings and credentials are for controllers")
    expect(git.title).toBe("Git's files are for controllers")
    expect(git.words).toMatch(/view-only link does not open them/)
    expect(git.icon).toBe('i-lucide-key-round')
    expect(refusedWords('file viewing requires the control role').title).toBe('Files are for controllers on this session')
    expect(refusedWords('').title).toBe('Files are for controllers on this session')
  })
})

describe('a commit\'s diff tab', () => {
  it('is its own tab per commit, named by the short id', () => {
    let st = openTab(emptyTabs(), 'diff', '/r/users.go', undefined, { status: 'M' })
    st = openTab(st, 'diff', '/r/users.go', undefined, { status: 'M', rev: 'a'.repeat(40), short: 'aaaaaaa', base: 'b'.repeat(40) })
    st = openTab(st, 'diff', '/r/users.go', undefined, { status: 'M', rev: 'c'.repeat(40) })
    expect(st.tabs.map((t) => t.id)).toEqual(['diff:/r/users.go', `diff@${'a'.repeat(40)}:/r/users.go`, `diff@${'c'.repeat(40)}:/r/users.go`])
    expect(st.tabs.map((t) => t.title)).toEqual(['users.go', 'aaaaaaa users.go', 'ccccccc users.go'])
    st = openTab(st, 'diff', '/r/users.go', undefined, { status: 'M', rev: 'a'.repeat(40), short: 'aaaaaaa' })
    expect(st.tabs).toHaveLength(3)
    expect(st.active).toBe(`diff@${'a'.repeat(40)}:/r/users.go`)
  })
})

describe('the Markdown preview toggle', () => {
  it('shows one tab rendered, or its source again, and leaves the other tabs as they were', () => {
    let s = openTab(openTab(emptyTabs(), 'file', '/r/README.md'), 'file', '/r/docs/users.md')
    s = togglePreview(s, 'file:/r/README.md')
    expect(s.tabs.map((t) => !!t.preview)).toEqual([true, false])
    expect(s.active).toBe('file:/r/docs/users.md')
    s = togglePreview(s, 'file:/r/README.md')
    expect(s.tabs.map((t) => !!t.preview)).toEqual([false, false])
    expect(togglePreview(s, 'file:/nope').tabs.map((t) => !!t.preview)).toEqual([false, false])
  })
})

describe('Preview plan (#432)', () => {
  it('opens a new tab rendered and gives it the focus, the other tabs as they were, the editor unfolded, the terminal as it was', () => {
    let s = openTab(emptyTabs(), 'file', '/r/README.md')
    s = togglePreview(s, 'file:/r/README.md')
    s = openTab(s, 'file', '/r/main.go')
    s = toggleFold(s)
    s = openPreview(s, '/h/.claude/plans/plan.md')
    expect(s.tabs.map((t) => [t.path, !!t.preview, !!t.takeFocus])).toEqual([
      ['/r/README.md', true, false],
      ['/r/main.go', false, false],
      ['/h/.claude/plans/plan.md', true, true],
    ])
    expect(s.active).toBe('file:/h/.claude/plans/plan.md')
    expect(s.folded).toBe(false)
    expect(!!s.collapsed).toBe(false)
    // The focus is taken once.
    s = takeFocus(s, 'file:/h/.claude/plans/plan.md')
    expect(s.tabs.map((t) => !!t.takeFocus)).toEqual([false, false, false])
  })

  it('brings an open source tab to the front rendered, whatever it showed; a collapsed terminal stays collapsed', () => {
    let s = openTab(openTab(emptyTabs(), 'file', '/h/plan.md'), 'file', '/r/a.go')
    s = toggleCollapse(s)
    s = openPreview(s, '/h/plan.md')
    expect(s.tabs).toHaveLength(2)
    expect(s.active).toBe('file:/h/plan.md')
    expect(s.tabs.map((t) => !!t.preview)).toEqual([true, false])
    expect(s.collapsed).toBe(true)
    // Already rendered, it stays rendered: Preview plan never turns the preview off.
    expect(openPreview(s, '/h/plan.md').tabs[0]!.preview).toBe(true)
    // Open plan is as it was: the tab to the front, its preview untouched.
    expect(openTab(togglePreview(s, 'file:/h/plan.md'), 'file', '/h/plan.md').tabs[0]!.preview).toBe(false)
  })
})

describe("the terminal's collapse (#432)", () => {
  const two = (): TabsState => openTab(openTab(emptyTabs(), 'file', '/r/a.go'), 'file', '/r/b.go')

  it('is set by its button and cleared by the button again', () => {
    let s = two()
    expect(!!s.collapsed).toBe(false)
    s = toggleCollapse(s)
    expect(s.collapsed).toBe(true)
    expect(s.folded).toBe(false)
    s = toggleCollapse(s)
    expect(s.collapsed).toBe(false)
    // Nothing to collapse under with no tab open.
    expect(toggleCollapse(emptyTabs())).toEqual(emptyTabs())
  })

  it("lasts through opening, switching and closing tabs other than the last", () => {
    let s = toggleCollapse(two())
    s = openTab(s, 'file', '/r/c.go')
    s = activateTab(s, 'file:/r/a.go')
    s = cycleTab(s)
    s = closeTab(s, 'file:/r/c.go')
    expect(s.collapsed).toBe(true)
  })

  it("is cleared by T's fold, and a folded editor comes back to take the column as the terminal collapses", () => {
    let s = toggleFold(toggleCollapse(two()))
    expect(s.folded).toBe(true)
    expect(s.collapsed).toBe(false)
    s = toggleFold(s)
    expect(s.folded).toBe(false)
    expect(s.collapsed).toBe(false)
    s = toggleCollapse(toggleFold(s))
    expect(s.folded).toBe(false)
    expect(s.collapsed).toBe(true)
  })

  it("is cleared by the last tab's close and by closing all", () => {
    let s = toggleCollapse(two())
    s = closeTab(closeTab(s, 'file:/r/a.go'), 'file:/r/b.go')
    expect(s).toEqual(emptyTabs())
    expect(openTab(s, 'file', '/r/a.go').collapsed).toBeFalsy()
    expect(closeAllTabs().collapsed).toBeFalsy()
  })
})

describe("a tab's close menu (#330)", () => {
  const three = (): TabsState => openTab(openTab(openTab(emptyTabs(), 'file', '/r/a.go'), 'file', '/r/b.go'), 'file', '/r/c.go')
  const ids = (s: TabsState) => s.tabs.map((t) => t.id)

  it('names what each choice closes, in strip order', () => {
    const s = three()
    const [a, b, c] = ids(s) as [string, string, string]
    expect(tabsToClose(s, b, 'right')).toEqual([c])
    expect(tabsToClose(s, b, 'left')).toEqual([a])
    expect(tabsToClose(s, b, 'others')).toEqual([a, c])
    expect(tabsToClose(s, b, 'all')).toEqual([a, b, c])
    expect(tabsToClose(s, a, 'left')).toEqual([])
    expect(tabsToClose(s, c, 'right')).toEqual([])
    expect(tabsToClose(s, 'gone', 'others')).toEqual([])
    expect(tabsToClose(s, 'gone', 'all')).toEqual([a, b, c])
  })
})
