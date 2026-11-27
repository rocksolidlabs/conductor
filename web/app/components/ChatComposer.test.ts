import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

// The box to write in, a chat's agent's (docs/plans/chat-or-terminal.md, design 11; UX3 R4). The app's vitest mounts no component:
// its source is checked for the bindings that draw the box; what a page shows is chat-box.spec.ts's and chat-view.spec.ts's.

const src = readFileSync(new URL('./ChatComposer.vue', import.meta.url), 'utf8')
const view = readFileSync(new URL('./ChatView.vue', import.meta.url), 'utf8')
const agentForm = src.slice(src.indexOf('<form v-if="agent"'), src.indexOf('<form v-else'))

describe("the agent's box", () => {
  it('draws no standing line: what the session does with a command is said only while the line begins with /', () => {
    expect(src).toContain("const slash = computed(() => !!props.agent && draft.value.trimStart().startsWith('/'))")
    expect(agentForm).toMatch(/<p v-if="slash && agent\.verbatim === true"[^>]*data-chat-help="verbatim">Sent as plain text\. To run a command, use the Terminal\.<\/p>/)
    expect(agentForm).toMatch(/<p v-else-if="slash && agent\.verbatim === false"[^>]*data-chat-help="commands">Messages starting with <code[^>]*>\/<\/code> may run as the agent's commands\.<\/p>/)
    expect(src.match(/data-chat-help=/g)).toHaveLength(2)
    // The old standing lines are gone, the keys line among them: it is Send's tooltip now.
    expect(src).not.toContain('are not expanded here')
    expect(agentForm).not.toContain('Enter sends · Shift+Enter new line')
    expect(agentForm).toContain('<UTooltip text="Enter sends · Shift+Enter for a new line" :disabled="phone">')
    // The people's chat keeps its own keys line.
    expect(src.slice(src.indexOf('<form v-else'))).toContain('<span v-if="!phone">Enter sends · Shift+Enter new line</span>')
    // The chat gives the box the session's verbatim: its own welcome's, else the page's word from the listing.
    expect(view).toContain("const verbatim = computed<boolean | undefined>(() => (chatWelcome.value ? !!conn.welcome.value?.verbatim : props.verbatim))")
    expect(view).toContain(':agent="{ verbatim, running,')
  })

  it("is one row: the field, then Stop turn with its word while a turn runs, then Send; the placeholder names the agent", () => {
    const row = agentForm.slice(agentForm.indexOf('<div class="flex items-end gap-2">'))
    const field = row.indexOf('<UTextarea')
    const stop = row.indexOf('data-chat-stop')
    const send = row.indexOf('data-chat-send')
    expect(field).toBeGreaterThan(-1)
    expect(stop).toBeGreaterThan(field)
    expect(send).toBeGreaterThan(stop)
    expect(row).toMatch(/<UTooltip v-if="agent\.running && agent\.canStop" :text="`Stops what \$\{who\} is doing now\. The conversation stays\.`">\s*<UButton label="Stop turn" icon="i-lucide-square" color="neutral" variant="outline"/)
    expect(src).not.toContain('Stop this turn')
    expect(src).toContain('if (!a?.reply) return `Message ${who.value}`')
    expect(view).not.toContain('Write to the agent')
  })

  it('Escape leaves the field, so that the page\'s keys work again', () => {
    expect(src).toMatch(/if \(e\.key === 'Escape' && props\.agent\) \{\s*;\(e\.target as HTMLElement \| null\)\?\.blur\(\)/)
  })

  it('keeps its line by session in the app\'s store, and takes the cursor only when nothing else holds it', () => {
    expect(src).toContain('const lines: RowDrafts = props.agent?.draftKey ? useBoxDrafts() : (reactive(new RowDrafts()) as RowDrafts)')
    expect(view).toContain('name: agentName, draftKey, asked }"')
    expect(view).toMatch(/function takeCursor\(\) \{\s*if \(!controls\.value \|\| keysAnswer\.value/)
    expect(view).toContain('const keysAnswer = computed(() => !!props.asked && (props.asked.choices || !props.asked.reply))')
    expect(view).toContain('if (!el || el === document.body) composer.value?.focus()')
  })

  it('answers the question the page shows: a line begun as it shows is bound to it and goes as its answer, Send reading Answer', () => {
    // Bound as the sidebar's rows bind theirs (RowDrafts.edit, bindDraft): to the question shown at the line's first character.
    expect(src).toContain('if (props.agent) lines.edit(lineKey.value, v, asking())')
    expect(src).toContain('return a?.reply ? (a.id ?? ASKED) : undefined')
    expect(src).toContain("const answering = computed(() => !!props.agent && lines.get(lineKey.value)?.question !== undefined)")
    expect(src).toContain('if (answering.value) return answer()')
    // The answer leaves the box as it goes; a refused one comes back bound as it was (RowDrafts.settle).
    expect(src).toMatch(/emit\('answer', raw\.trim\(\), bound === ASKED \? undefined : bound, \(outcome\) => \{\s*lines\.settle\(key, raw, outcome\)/)
    expect(agentForm).toContain(`:label="answering ? 'Answer' : 'Send'"`)
    // The placeholder says what the box does now.
    expect(src).toContain("return a.choices ? 'Pick an answer above, or type your own' : `Answer ${who.value}…`")
    // The chat hands the answer to its page, which answers through answerQuestion; the card in Chat draws no field of its own.
    expect(view).toContain(`@answer="(text, question, taken) => emit('reply', text, question, taken)"`)
    const page = readFileSync(new URL('./SessionView.vue', import.meta.url), 'utf8') // the session's whole view (UX3 R72)
    expect(page).toMatch(/:asked="chatAsked"\s*@reply="reply"/)
    expect(page).toContain('answers pastes :field="false"')
    const bar = readFileSync(new URL('./QuickReplyBar.vue', import.meta.url), 'utf8')
    expect(bar).toContain('<form v-else-if="field"')
  })

  it("bounds the agent's message by the submit's bound", () => {
    expect(src).toContain('const tooLong = computed(() => (props.agent ? bytes.value > MAX_SUBMIT : chatTooLong(bytes.value)))')
  })
})
