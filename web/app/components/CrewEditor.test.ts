import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import type { CrewInput } from '../composables/useSessions'
import { MEMBERS_RUN_AS, membersRunAs, membersRunAsLine, toCrewInput, withMembersRunAs } from '../utils/crews'
import { RUNS_AS_CHAT_FOR_RUN, switchState, type SwitchFacts } from '../utils/switchState'

// The crew editor's "Members run as: Terminal | Chat" (docs/plans/chat-or-terminal.md, design 14). The app's vitest runs in node with no
// component mounted, so the choice is tested through the functions the editor draws it from, and the editor's source is checked for the
// field it draws.

const crew: CrewInput = { name: 'API sweep', goal: '', cwd: '/work', where: 'server', isolation: 'worktree', openAfterLaunch: false, members: [] }

describe('the crew editor', () => {
  it('membersRunAs: Terminal by default, Chat for a crew whose way is chat, the choice saved as the server reads it', () => {
    expect(MEMBERS_RUN_AS.label).toBe('Members run as')
    expect(MEMBERS_RUN_AS.items.map((i) => i.label)).toEqual(['Terminal', 'Chat'])
    expect(MEMBERS_RUN_AS.chatLine).toBe("In a chat, members have Conductor's tools only: no other tool servers, the folder's or yours.")
    expect(membersRunAs(crew)).toBe('terminal')
    const chat = withMembersRunAs(crew, 'chat')
    expect(membersRunAs(chat)).toBe('chat')
    expect(toCrewInput(chat).way).toBe('chat')
    // Back to Terminal, the crew names no way, as every crew saved before the choice: its body is the one it was.
    const back = withMembersRunAs(chat, 'terminal')
    expect('way' in back).toBe(false)
    expect(toCrewInput(back)).toEqual(toCrewInput(crew))
    expect('way' in toCrewInput(crew)).toBe(false)
  })

  it('draws the choice with its line under it', () => {
    const src = readFileSync(new URL('./CrewEditor.vue', import.meta.url), 'utf8')
    expect(src).toContain(':label="MEMBERS_RUN_AS.label"')
    expect(src).toContain(':help="MEMBERS_RUN_AS.chatLine"')
    expect(src).toContain('data-crew-way')
  })

  it('the launch sheet and Dispatch\'s launch say how the members run, and change nothing', () => {
    expect(membersRunAsLine('chat')).toBe('Members run as: Chat')
    expect(membersRunAsLine(undefined)).toBe('Members run as: Terminal')
    for (const file of ['./CrewLaunchSheet.vue', './DispatchLaunch.vue']) {
      const src = readFileSync(new URL(file, import.meta.url), 'utf8')
      expect(src).toContain('membersRunAsLine(')
      expect(src).not.toContain('withMembersRunAs')
    }
  })

  it("a Chat run's member's switch is a label, its run's way, and a person's move of it shows the route's refusal as given", () => {
    const facts: SwitchFacts = { hasProtocol: true, way: 'protocol', noted: '', why: '', canMove: true, firstReply: true, qualified: true, inTurn: false, moving: false, crewChat: true }
    const s = switchState(facts)
    expect(s).toMatchObject({ shown: true, selected: 'protocol', pressable: false, line: RUNS_AS_CHAT_FOR_RUN })
    expect(RUNS_AS_CHAT_FOR_RUN).toBe('Runs as a chat for its run.')
    const refused = switchState({ ...facts, crewChat: false, way: 'terminal', refused: "A crew member's way is its run's." })
    expect(refused.line).toBe("A crew member's way is its run's.")
  })
})
