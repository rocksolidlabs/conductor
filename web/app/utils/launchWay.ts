import type { SessionWay } from './protocol'

// The Launch agent dialog's Run as: Terminal | Chat (issue 1174). A launch as a chat starts in its terminal and moves to Chat by
// itself at the first moment the move can be made (docs/protocol.md, Moving between ways); the dialog offers it only for an agent
// whose catalog entry has a protocol, in the order and shape of the header's switch (WaySwitch.vue), and remembers the side each
// agent was last launched with in this browser.

/** The two sides, in the header switch's order. */
export const RUN_AS_SIDES: ReadonlyArray<{ way: SessionWay; label: string }> = [
  { way: 'terminal', label: 'Terminal' },
  { way: 'protocol', label: 'Chat' },
]

/** The one line under the choice while Chat is picked. */
export const RUN_AS_CHAT_LINE = 'Starts in its terminal, then moves to Chat.'

/** Whether the dialog offers Run as for an agent: one whose catalog entry has a protocol (`AgentInfo.protocol`). */
export function offersRunAs(agent: { protocol?: { name?: string } } | null | undefined): boolean {
  return !!agent?.protocol?.name
}

/** The side an arrow key moves the choice to from `current`, or null for a key that moves nothing (two sides: every arrow is the other). */
export function sideForKey(current: SessionWay, key: string): SessionWay | null {
  switch (key) {
    case 'ArrowLeft':
    case 'ArrowUp':
    case 'ArrowRight':
    case 'ArrowDown':
      return current === 'terminal' ? 'protocol' : 'terminal'
    case 'Home':
      return 'terminal'
    case 'End':
      return 'protocol'
  }
  return null
}

/** Where this browser keeps the side each agent was last launched with: `{agentId: way}`, a per-viewer convenience. */
export const RUN_AS_KEY = 'conductor.launch.runAs'
/** At most this many agents are kept; the oldest written goes first. */
export const RUN_AS_KEPT = 64

function readAll(storage: Pick<Storage, 'getItem'> | null): Array<[string, SessionWay]> {
  try {
    const v = JSON.parse(storage?.getItem(RUN_AS_KEY) ?? '{}') as unknown
    if (!v || typeof v !== 'object' || Array.isArray(v)) return []
    return Object.entries(v as Record<string, unknown>).filter((e): e is [string, SessionWay] => e[1] === 'terminal' || e[1] === 'protocol')
  } catch {
    return []
  }
}

/** The side `agentId` was last launched with in this browser; `terminal` the first time, and whenever nothing can be read. */
export function readRunAs(storage: Pick<Storage, 'getItem'> | null, agentId: string): SessionWay {
  return readAll(storage).find(([id]) => id === agentId)?.[1] ?? 'terminal'
}

/** Keeps the side `agentId` was launched with: written at Launch alone. A browser that keeps nothing is told nothing. */
export function writeRunAs(storage: Pick<Storage, 'getItem' | 'setItem'> | null, agentId: string, way: SessionWay) {
  if (!agentId) return
  const kept = readAll(storage).filter(([id]) => id !== agentId)
  kept.push([agentId, way])
  try {
    storage?.setItem(RUN_AS_KEY, JSON.stringify(Object.fromEntries(kept.slice(-RUN_AS_KEPT))))
  } catch {
    // The browser may refuse storage; the next opening then starts at Terminal.
  }
}
