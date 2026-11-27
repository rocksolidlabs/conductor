import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { RUN_AS_CHAT_LINE, RUN_AS_KEPT, RUN_AS_KEY, RUN_AS_SIDES, offersRunAs, readRunAs, sideForKey, writeRunAs } from './launchWay'
import { AFTER_FIRST_REPLY, MOVES_TO_CHAT, pressFor, switchState, type SwitchFacts } from './switchState'

// The Launch agent dialog's Run as: Terminal | Chat (issue 1174). The app's vitest mounts no component: the choice's keys and what it
// remembers are this file's functions, and the component's source is checked for the bindings that draw them; what a browser shows is
// launch-chat.spec.ts's.

const choice = readFileSync(new URL('../components/RunAsChoice.vue', import.meta.url), 'utf8')
const modal = readFileSync(new URL('../components/LaunchSessionModal.vue', import.meta.url), 'utf8')
const header = readFileSync(new URL('../components/WaySwitch.vue', import.meta.url), 'utf8')

/** A browser's storage of its own, as localStorage holds it. */
function store(init: Record<string, string> = {}) {
  const m = new Map(Object.entries(init))
  return {
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => void m.set(k, v),
    raw: () => m.get(RUN_AS_KEY),
  }
}

describe('Run as', () => {
  it('has the two sides in the header switch’s order, Terminal first, and its one line', () => {
    expect(RUN_AS_SIDES.map((s) => s.label)).toEqual(['Terminal', 'Chat'])
    expect(RUN_AS_SIDES.map((s) => s.way)).toEqual(['terminal', 'protocol'])
    expect(header).toContain("{ way: 'terminal', label: 'Terminal' },\n  { way: 'protocol', label: 'Chat' },")
    expect(RUN_AS_CHAT_LINE).toBe('Starts in its terminal, then moves to Chat.')
  })

  it('is offered only for an agent whose catalog entry has a protocol', () => {
    expect(offersRunAs({ protocol: { name: 'claude-stream-json' } })).toBe(true)
    expect(offersRunAs({ protocol: {} })).toBe(false)
    expect(offersRunAs({})).toBe(false)
    expect(offersRunAs(undefined)).toBe(false)
  })

  it('keys: every arrow moves to the other side, Home and End to the first and the last, anything else nothing', () => {
    for (const key of ['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown']) {
      expect(sideForKey('terminal', key)).toBe('protocol')
      expect(sideForKey('protocol', key)).toBe('terminal')
    }
    expect(sideForKey('protocol', 'Home')).toBe('terminal')
    expect(sideForKey('terminal', 'End')).toBe('protocol')
    expect(sideForKey('terminal', 'Enter')).toBeNull()
    expect(sideForKey('terminal', 'Tab')).toBeNull()
    expect(sideForKey('terminal', 'a')).toBeNull()
  })

  it('remembers the side per agent, Terminal the first time', () => {
    const s = store()
    expect(readRunAs(s, 'claude')).toBe('terminal')
    writeRunAs(s, 'claude', 'protocol')
    expect(readRunAs(s, 'claude')).toBe('protocol')
    // Another agent keeps its own.
    expect(readRunAs(s, 'claude-work')).toBe('terminal')
    writeRunAs(s, 'claude-work', 'terminal')
    expect(readRunAs(s, 'claude')).toBe('protocol')
    writeRunAs(s, 'claude', 'terminal')
    expect(readRunAs(s, 'claude')).toBe('terminal')
  })

  it('reads nothing it did not write, keeps a bounded count, and survives a browser that keeps nothing', () => {
    expect(readRunAs(store({ [RUN_AS_KEY]: 'not json' }), 'claude')).toBe('terminal')
    expect(readRunAs(store({ [RUN_AS_KEY]: '["protocol"]' }), 'claude')).toBe('terminal')
    expect(readRunAs(store({ [RUN_AS_KEY]: '{"claude":"chat"}' }), 'claude')).toBe('terminal')
    expect(readRunAs(null, 'claude')).toBe('terminal')
    const refusing = { getItem: () => null, setItem: () => { throw new Error('quota') } }
    expect(() => writeRunAs(refusing, 'claude', 'protocol')).not.toThrow()
    const s = store()
    for (let i = 0; i <= RUN_AS_KEPT; i++) writeRunAs(s, `agent-${i}`, 'protocol')
    const kept = Object.keys(JSON.parse(s.raw() ?? '{}'))
    expect(kept).toHaveLength(RUN_AS_KEPT)
    expect(kept).not.toContain('agent-0')
    expect(readRunAs(s, `agent-${RUN_AS_KEPT}`)).toBe('protocol')
  })

  it('the component: the label, the sides as radios moved by the keys, and the line only while Chat is picked', () => {
    expect(choice).toContain('<UFormField label="Run as"')
    expect(choice).toMatch(/role="radiogroup"[^>]*@keydown="onKey"/)
    expect(choice).toContain('v-for="side in RUN_AS_SIDES"')
    expect(choice).toContain('role="radio"')
    expect(choice).toContain(':aria-checked="side.way === way"')
    expect(choice).toContain(":variant=\"side.way === way ? 'soft' : 'ghost'\"")
    expect(choice).toContain('const next = sideForKey(way.value, e.key)')
    expect(choice).toMatch(/<p v-if="way === 'protocol'"[^>]*data-run-as-line>\{\{ RUN_AS_CHAT_LINE \}\}<\/p>/)
  })

  it('the dialog: shown for an agent that runs as a chat on this server, never a clone; read at each opening and pick; written at Launch', () => {
    expect(modal).toContain("const showsRunAs = computed(() => !cloning.value && state.runsOn === 'server' && offersRunAs(selected.value))")
    expect(modal).toContain('<RunAsChoice v-if="showsRunAs" v-model="runAs" />')
    expect(modal).toContain("if (v) runAs.value = agentId ? readRunAs(browserStorage(), agentId) : 'terminal'")
    // Written after the launch's answer, in submit alone: a side pressed and the dialog cancelled is not kept.
    expect(modal.match(/writeRunAs\(/g)).toHaveLength(1)
    expect(modal).toMatch(/const session = await api\.create\(\{[\s\S]*?way: showsRunAs\.value && runAs\.value === 'protocol' \? 'protocol' : undefined,\n {4}\}\)\n {4}if \(showsRunAs\.value\) writeRunAs\(browserStorage\(\), state\.agentId, runAs\.value\)/)
  })
})

describe("the header's switch on a session launched as a chat", () => {
  const facts: SwitchFacts = { hasProtocol: true, way: 'terminal', noted: 'protocol', why: '', canMove: true, firstReply: false, qualified: true, inTurn: false, moving: false }

  it('says it moves to Chat after the first reply, from a welcome that carries the noted move alone', () => {
    expect(AFTER_FIRST_REPLY).toBe("Moves to Chat after the agent's first reply.")
    expect(switchState(facts).line).toBe(AFTER_FIRST_REPLY)
    expect(switchState({ ...facts, inTurn: true }).line).toBe(AFTER_FIRST_REPLY)
    // After the first reply a noted move waits for its turn, in the plan's words; a `way` message's sentence comes first.
    expect(switchState({ ...facts, firstReply: true, inTurn: true }).line).toBe(MOVES_TO_CHAT)
    expect(switchState({ ...facts, why: 'A request is waiting for an answer.' }).line).toBe('A request is waiting for an answer.')
  })

  it('lets a person press Terminal to keep it where it is', () => {
    const s = switchState(facts)
    expect(s.selected).toBe('terminal')
    expect(s.noted).toBe('protocol')
    expect(pressFor(s, 'terminal')).toBe('terminal')
  })
})
