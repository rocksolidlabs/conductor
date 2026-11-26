import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { renderMarkdown } from '../utils/markdown'
import type { TranscriptEntry, TranscriptReply, TranscriptRequest } from '../utils/protocol'
import { atChatEnd, CHAT_END_SLACK, chatBlocks, readPart, stepsLine } from '../utils/transcript'

// A session run as a chat (docs/plans/chat-or-terminal.md, design 11). The app's vitest mounts no component: the rules are the
// functions ChatView.vue and ToolStepRow.vue draw from, and their source is checked for the bindings that draw them; what a browser
// shows is chat-view.spec.ts's.

const view = readFileSync(new URL('./ChatView.vue', import.meta.url), 'utf8')
const row = readFileSync(new URL('./ToolStepRow.vue', import.meta.url), 'utf8')
const diff = readFileSync(new URL('./InlineDiff.vue', import.meta.url), 'utf8')
const words = readFileSync(new URL('./ChatWords.vue', import.meta.url), 'utf8')
const template = (src: string) => src.slice(src.indexOf('<template>'))
const at = '2026-11-25T12:00:00.000Z'
const tool = (seq: number): TranscriptEntry => ({ seq, mark: seq, rev: 1, at, kind: 'tool', turn: 1, tool: 'Bash', summary: `step ${seq}`, status: 'ok', durationMs: 1234, outputBytes: 9, ref: `toolu_${seq}`, inputSha256: 'a'.repeat(64) })

describe('the chat', () => {
  it("drawsMarkdownWithoutHtml: an agent's raw HTML is text, a link only to https, no image loaded", () => {
    const html = renderMarkdown('**bold** <img src=x onerror="alert(1)"> [plain](http://example.invalid/) [safe](https://example.invalid/) ![pic](https://example.invalid/p.png) [here](notes.md)', { dir: '', relativeLinks: false, agentWords: true }).html
    expect(html).toContain('<strong>bold</strong>')
    expect(html).not.toMatch(/<img/)
    expect(html).toContain('&lt;img src=x onerror=&quot;alert(1)&quot;&gt;')
    expect(html).toContain('<a href="https://example.invalid/"')
    // A link's address is its title: words that name another address do not hide where it goes.
    expect(renderMarkdown('[https://bank.example/](https://evil.example/x)', { dir: '', relativeLinks: false, agentWords: true }).html).toContain('title="https://evil.example/x"')
    expect(html).not.toContain('href="http://example.invalid/"')
    expect(html).not.toContain('href="notes.md"')
    expect(html).not.toContain('data-md-link')
    // The words are set through that renderer alone, with those options, by ChatWords; nothing else of an entry is set as HTML.
    expect(words).toContain("const html = computed(() => renderMarkdown(props.text, { dir: '', relativeLinks: false, agentWords: true }).html)")
    expect((words.match(/v-html=/g) ?? []).length).toBe(1)
    expect(words).toContain('v-html="html"')
    expect(template(view) + template(row) + template(diff)).not.toContain('v-html')
    expect(view).toContain('<ChatWords v-if="it.kind === \'text\'" :text="it.text" />')
  })

  it('foldsStepsIntoOneLine: a turn\'s steps are one line until opened, and no count of tokens, timing or id is on the surface', () => {
    const entries: TranscriptEntry[] = [
      { seq: 1, mark: 1, rev: 0, at, kind: 'turn', turn: 1, phase: 'start' },
      ...Array.from({ length: 12 }, (_, i) => tool(i + 2)),
      { seq: 14, mark: 14, rev: 0, at, kind: 'turn', turn: 1, phase: 'end', outcome: 'done', durationMs: 9000, usage: { inputTokens: 10, outputTokens: 20, costUsd: 0.01 } },
    ]
    const [t] = chatBlocks(entries)
    if (t?.kind !== 'turn') throw new Error('a turn')
    expect(t.steps).toHaveLength(12)
    expect(t.items).toHaveLength(0)
    expect(stepsLine(t)).toBe('12 steps')
    expect(stepsLine({ ...t, running: true })).toBe('Working.')
    // The rows are drawn only once the line is opened.
    expect(view).toMatch(/<ul v-if="opened\.has\(b\.key\)"[^>]*data-chat-step-rows>\s*<ToolStepRow/)
    // Nothing of the timings, the usage or an agent's or the session's ids is drawn by the chat's components.
    for (const src of [view, row, diff, words]) {
      const t = template(src)
      expect(t).not.toMatch(/durationMs|usage|inputTokens|outputTokens|costUsd|outputBytes|inputBytes|inputSha256/)
      expect(t).not.toMatch(/\{\{[^}]*\.(ref|call|question|parent|seq|mark|rev)\b[^}]*\}\}/)
    }
  })

  it('stepOpensItsParts: opening a row sends one transcript_get part, for its output', async () => {
    const asked: TranscriptRequest[] = []
    const get = async (req: TranscriptRequest): Promise<TranscriptReply> => {
      asked.push(req)
      return { t: 'part', reqId: 'r', seq: 7, name: 'output', offset: 0, total: 9, kept: 9, bytes: new TextEncoder().encode('notes.txt') }
    }
    const part = await readPart(get, 7, 'output')
    expect(new TextDecoder().decode(part.bytes)).toBe('notes.txt')
    expect(asked).toEqual([{ part: { seq: 7, name: 'output', offset: 0 } }])
    // The row reads its output as it opens, once: a row opened again draws what it read.
    expect(row).toContain("if (!opened.value || output.value !== null || props.entry.status === 'running') return")
    expect(row).toContain("const part = await props.read(props.entry.seq, 'output')")
    expect(row).toMatch(/data-tool-step-row @click="toggle"/)
    expect(view).toContain('const get = limitReads((req: TranscriptRequest) => conn.transcriptGet(req), MAX_TRANSCRIPT_IN_FLIGHT)')
    expect(view).toContain('const read = (seq: number, name: TranscriptPartName) => readPart(get, seq, name)')
  })

  it("rendersEachBlockOnce: an agent's block of words is rendered again only when its own text changes, not at every change of the chat", () => {
    // Its own component, whose rendering is computed from its text alone.
    expect(words).toMatch(/defineProps<\{ text: string \}>\(\)/)
    expect(view).not.toContain('renderMarkdown(')
  })

  it("opensChangesOnlyWhereTheEditorIs: a file change's link is drawn only where the page opens the editor's diff tab", () => {
    expect(view).toContain(':can-open="!readOnly && opensChanges"')
    expect(view).toContain('opensChanges: false')
    const terminal = readFileSync(new URL('./TerminalView.vue', import.meta.url), 'utf8')
    expect(terminal).toContain(':opens-changes="opensChanges"')
    expect(terminal).toContain('opensChanges: false')
    const pages = (p: string) => readFileSync(new URL(`../pages/${p}`, import.meta.url), 'utf8')
    expect(pages('sessions/[id].vue')).toMatch(/operator\s+opens-changes/)
    for (const p of ['yard.vue', 'roundhouse.vue', 'join/[token].vue']) expect(pages(p), p).not.toContain('opens-changes')
  })

  it("noBarBesideTheQuestion: where the chat shows a permission question, no reply bar repeats it", () => {
    const pages = (p: string) => readFileSync(new URL(`../pages/${p}`, import.meta.url), 'utf8')
    expect(pages('sessions/[id].vue')).toContain(`<QuickReplyBar v-if="!(way === 'protocol' && answersInChat(attention))"`)
    expect(pages('join/[token].vue')).toContain(`<QuickReplyBar v-if="!(joinWay === 'protocol' && answersInChat(attention))"`)
    expect(pages('yard.vue')).toContain(`<QuickReplyBar v-if="!(focused.way === 'protocol' && answersInChat(focused.attention))"`)
  })

  it("followsOnlyAtTheEnd: where the person is, is measured as a change arrives, before it is drawn; one reading above the end stays", () => {
    expect(atChatEnd({ scrollHeight: 2000, scrollTop: 1500, clientHeight: 500 })).toBe(true)
    expect(atChatEnd({ scrollHeight: 2000, scrollTop: 1500 - CHAT_END_SLACK + 1, clientHeight: 500 })).toBe(true)
    // An input opened at the end grew below: the same place is no longer the end.
    expect(atChatEnd({ scrollHeight: 2800, scrollTop: 1500, clientHeight: 500 })).toBe(false)
    expect(view).toMatch(/watch\(\s*\(\) => conn\.transcriptVersion\.value,\s*\(\) => \{\s*const el = scroller\.value\s*if \(el && !atChatEnd\(el\)\) return/)
    expect(view).toContain("{ flush: 'pre' },")
    expect(view).not.toContain('atBottom')
  })

  it("readsTheSendersAnswerFromTheTab: an answer's own words come from the tab's store of answers (useTypedAnswers), as the People chat's do", () => {
    expect(view).toContain('const typedAnswers = useTypedAnswers()')
    expect(view).toContain('const answered = (p: TranscriptEntry) => answerOf(p, typedAnswers.of)')
    expect(view).not.toContain('ownAnswer')
  })
})
