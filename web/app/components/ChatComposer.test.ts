import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

// The box to write in, a chat's agent's (docs/plans/chat-or-terminal.md, design 11). The app's vitest mounts no component: its source
// is checked for the bindings that draw the help line; what a browser shows is chat-view.spec.ts's.

const src = readFileSync(new URL('./ChatComposer.vue', import.meta.url), 'utf8')
const view = readFileSync(new URL('./ChatView.vue', import.meta.url), 'utf8')

describe("the agent's box", () => {
  it('helpLineByVerbatim: the "Sent as written" line only where the session\'s verbatim is true, the other where it is false, none when not known', () => {
    expect(src).toMatch(/<p v-if="agent && agent\.verbatim === true"[^>]*data-chat-help="verbatim">Sent as written: <code[^>]*>@file<\/code> and <code[^>]*>\/command<\/code> are not expanded here\.<\/p>/)
    expect(src).toMatch(/<p v-else-if="agent && agent\.verbatim === false"[^>]*data-chat-help="commands">Messages starting with <code[^>]*>\/<\/code> may run as the agent's commands\.<\/p>/)
    // The people's chat (no `agent`) has neither.
    expect(src.match(/data-chat-help=/g)).toHaveLength(2)
    // The chat gives the box the session's verbatim: its own welcome's, else the page's word from the listing.
    expect(view).toContain("const verbatim = computed<boolean | undefined>(() => (chatWelcome.value ? !!conn.welcome.value?.verbatim : props.verbatim))")
    expect(view).toContain(':agent="{ verbatim, running,')
  })

  it("bounds the agent's message by the submit's bound, and offers Stop only while a turn runs, to a viewer who may stop it", () => {
    expect(src).toContain('const tooLong = computed(() => (props.agent ? bytes.value > MAX_SUBMIT : chatTooLong(bytes.value)))')
    expect(src).toMatch(/<IconButton v-if="agent\?\.running && agent\.canStop"[^>]*label="Stop this turn"[^>]*data-chat-stop/)
  })
})
