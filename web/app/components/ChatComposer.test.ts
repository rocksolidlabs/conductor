import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

// The box to write in, a chat's agent's (docs/plans/chat-or-terminal.md, design 11; UX3 R4). The app's vitest mounts no component:
// its source is checked for the bindings that draw the box; what a page shows is chat-box.spec.ts's and chat-view.spec.ts's.

const src = readFileSync(new URL('./ChatComposer.vue', import.meta.url), 'utf8')
const view = readFileSync(new URL('./ChatView.vue', import.meta.url), 'utf8')
const agentForm = src.slice(src.indexOf('<form v-if="agent"'), src.indexOf('<form v-else'))

describe("the agent's box", () => {
  it('draws no standing line: what the session does with a command is said only while the line begins with /', () => {
    // Where the server lets this box send a line to expand, the line is no plain text and says nothing (UX3 R10).
    expect(src).toContain("const slash = computed(() => !!props.agent && !props.agent.expand && draft.value.trimStart().startsWith('/'))")
    expect(agentForm).toMatch(/<p v-if="slash && agent\.verbatim === true"[^>]*data-chat-help="verbatim">Sent as plain text\. To run a command, use the Terminal\.<\/p>/)
    expect(agentForm).toMatch(/<p v-else-if="slash && agent\.verbatim === false"[^>]*data-chat-help="commands">Messages starting with <code[^>]*>\/<\/code> may run as the agent's commands\.<\/p>/)
    expect(src.match(/data-chat-help=/g)).toHaveLength(3)
    // Where the box may expand, a / line that is not the person's own goes as plain text, and says so (UX3 R10).
    expect(agentForm).toMatch(/<p v-else-if="plainSlash"[^>]*data-chat-help="plain">Sent as plain text\. Type it yourself to run it as a command\.<\/p>/)
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
    expect(src).toContain('if (!a?.reply) return props.agent?.expand ? `Message ${who.value} · @ for a file, / for a command` : `Message ${who.value}`')
    expect(view).not.toContain('Write to the agent')
  })

  it('Escape leaves the field, so that the page\'s keys work again', () => {
    expect(src).toMatch(/if \(e\.key === 'Escape' && props\.agent\) \{\s*;\(e\.target as HTMLElement \| null\)\?\.blur\(\)/)
  })

  it('keeps its line by session in the app\'s store, and takes the cursor only when nothing else holds it', () => {
    expect(src).toContain('const lines: RowDrafts = props.agent?.draftKey ? useBoxDrafts() : (reactive(new RowDrafts()) as RowDrafts)')
    expect(view).toContain('name: agentName, draftKey, asked, expand: canExpand, folder: conn.welcome.value?.cwd }"')
    expect(view).toMatch(/function takeCursor\(\) \{\s*if \(!controls\.value \|\| keysAnswer\.value/)
    expect(view).toContain('const keysAnswer = computed(() => !!props.asked && (props.asked.choices || !props.asked.reply))')
    expect(view).toContain('if (!el || el === document.body) composer.value?.focus()')
  })

  it('answers the question the page shows: a line begun as it shows is bound to it and goes as its answer, Send reading Answer', () => {
    // Bound as the sidebar's rows bind theirs (RowDrafts.edit, bindDraft): to the question shown at the line's first character.
    expect(src).toContain('const now = asking()')
    expect(src).toMatch(/if \(!a\?\.reply\) return undefined\s*const id = a\.id \?\? ASKED/)
    expect(src).toContain("const answering = computed(() => !!props.agent && lines.get(lineKey.value)?.question !== undefined)")
    expect(src).toContain('if (answering.value) return answer()')
    // The answer leaves the box as it goes; a refused one comes back bound as it was (RowDrafts.settle).
    expect(src).toMatch(/emit\('answer', raw\.trim\(\), bound === ASKED \? undefined : bound, \(outcome\) => \{[\s\S]*?lines\.settle\(key, raw, outcome\)/)
    expect(agentForm).toContain(`:label="answering ? 'Answer' : 'Send'"`)
    // The placeholder says what the box does now.
    expect(src).toContain("return a.choices ? 'Pick an answer above, or type your own' : `Answer ${who.value}…`")
    // The chat hands the answer to its page, which answers through answerQuestion; the card in Chat draws no field of its own.
    expect(view).toContain(`@answer="(text, question, taken) => emit('reply', text, question, taken)"`)
    const page = readFileSync(new URL('../pages/sessions/[id].vue', import.meta.url), 'utf8')
    expect(page).toMatch(/:asked="chatAsked"\s*@reply="reply"/)
    expect(page).toContain('answers pastes :field="false"')
    const bar = readFileSync(new URL('./QuickReplyBar.vue', import.meta.url), 'utf8')
    expect(bar).toContain('<form v-else-if="field"')
  })

  it("bounds the agent's message by the submit's bound", () => {
    expect(src).toContain('const tooLong = computed(() => (props.agent ? bytes.value > MAX_SUBMIT : chatTooLong(bytes.value)))')
  })
  it("listsFilesForAnAtWord (UX3 R6): the agent's box alone, where the connection may read files; a pick writes the path as text", () => {
    // The agent's box draws the list; the people's chat has none.
    expect(agentForm).toContain('<ChatBoxMenu v-if="menuOpen"')
    expect(src.slice(src.indexOf('<form v-else'))).not.toContain('ChatBoxMenu')
    expect(src).toContain('const mention = computed<Mention | null>(() => (props.agent && props.files ? mentionAt(draft.value, caret.value) : null))')
    // Only where this box may expand a line: a picked file there is attached; elsewhere no list reads like it would be (UX3 R10).
    expect(view).toContain(':files="canExpand && conn.fileView.value ? files : undefined"')
    // Enter and Tab pick while it is open and never send; nothing while an input method composes.
    expect(src).toMatch(/function onKeydown\(e: KeyboardEvent\) \{\s*if \(menuKey\(e\)\) return/)
    expect(src).toContain("if (!menuOpen.value || e.isComposing) return false")
    expect(src).toMatch(/if \(e\.key === 'Enter' \|\| e\.key === 'Tab'\) \{[\s\S]*?e\.preventDefault\(\)\s*pick\(active\.value, e\.isTrusted\)\s*return true/)
    // Escape closes the list and keeps the text: it is the list's, not the box's.
    expect(src).toMatch(/if \(e\.key === 'Escape'\) \{\s*e\.preventDefault\(\)\s*e\.stopPropagation\(\)\s*dismissed\.value = mentionKey\.value\s*return true/)
    // Closed with Escape, it opens again for the next @ word, even one typed the same.
    expect(src).toMatch(/if \(!m\) \{[\s\S]*?dismissed\.value = ''/)
    // One read on its way at a time, and a late answer dropped.
    expect(src).toContain('if (n !== asked || mention.value?.words !== words) return')
    expect(src).toMatch(/if \(inFlight\) \{\s*pending = words\s*return/)
    // A pick: only a row that may be picked, the @ word replaced by the path and a space.
    expect(src).toContain('if (!m || !row?.pickable) return')
    expect(src).toContain('const put = pickMention(draft.value, m, row.value)')
  })
  it("marksForExpandOnlyThePersonsOwnLine (UX3 R10): the box keeps by utils/boxLine.ts, whose tests prove the rules", () => {
    // The box hands every input to markInput with the text it left, and asks expandsLine at a send: the rules are boxLine.test.ts's.
    expect(src).toMatch(/function onInput\(e: Event\) \{\s*markInput\(own, lineKey\.value, e as InputEvent, \(e\.target as HTMLTextAreaElement \| null\)\?\.value \?\? ''\)/)
    expect(src).toContain('const mine = expandsLine(true, own, key, draft.value)')
    expect(src).toContain('if (trusted) own.typed(lineKey.value, put.text)')
    expect(src).toContain("if (v === '') own.set(lineKey.value)")
    expect(src).toContain('if (freeHeld(lines, own, key, raw))')
    expect(src).toContain('const back = (text: string) => lines.putBack(key, text)')
    // The chat asks for expand only for that line, only on the server's word for this connection; a not_sent puts the line back.
    expect(view).toContain('const canExpand = computed(() => controls.value && conn.expand.value)')
    expect(view).toContain('const expand = canExpand.value && how?.own === true')
    expect(view).toMatch(/if \(e instanceof ReplyError && e\.code === 'not_sent'\) \{[\s\S]*?how\?\.back\(text\)/)
    // No other place of the app passes an `expand` to a submit, with any value: each submitAcked call read whole, to its paren.
    const root = new URL('..', import.meta.url).pathname
    const asking: string[] = []
    const calls = (text: string) => {
      const out: string[] = []
      for (let i = text.indexOf('submitAcked('); i >= 0; i = text.indexOf('submitAcked(', i + 1)) {
        let depth = 0
        let j = i + 'submitAcked'.length
        for (; j < text.length; j++) {
          if (text[j] === '(') depth++
          else if (text[j] === ')' && --depth === 0) break
        }
        out.push(text.slice(i, j + 1))
      }
      return out
    }
    const walk = (dir: string) => {
      for (const f of readdirSync(dir)) {
        const p = join(dir, f)
        if (statSync(p).isDirectory()) walk(p)
        else if (/\.(vue|ts)$/.test(f) && !/\.test\.ts$/.test(f) && calls(readFileSync(p, 'utf8')).some((c) => /\bexpand\b|\.\.\./.test(c))) asking.push(p.slice(root.length))
      }
    }
    walk(root)
    // The chat, the connection that passes its word on, and the transport's own signatures.
    expect(asking.sort()).toEqual(['components/ChatView.vue', 'composables/useSessionConnection.ts', 'utils/transport/base.ts', 'utils/transport/types.ts'])
  })

  it("keepsAnAnswerAnAnswer (UX3 R5's read): what was typed for one purpose is never sent for another", () => {
    // B1: a line begun while this box's answer to the question is on its way is a message, never a second answer; one the page had
    // already answered goes free, said.
    expect(src).toMatch(/return answeredHere\.has\(id\) \? undefined : id/)
    expect(src).toContain('if (bound && bound !== ASKED) answeredHere.add(bound)')
    expect(src).toMatch(/if \(outcome === 'held'\) \{\s*if \(freeHeld\(lines, own, key, raw\)\)/)
    // B2: a kept line rebinds only to a question that takes a typed reply; with none it stays bound, and says how to go on.
    expect(src).toContain('lines.edit(lineKey.value, v, d?.kept && now === undefined ? d.question : now)')
    expect(src).toContain("'Its question was answered. Empty the box to write a message instead.'")
    // B3: a line begun before its question had an id takes the id as it comes.
    expect(src).toMatch(/if \(now && !was\) lines\.rebind\(lineKey\.value, ASKED, now\)/)
  })
})
