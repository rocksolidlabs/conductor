import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { hostAvailabilityWords, remoteWords } from '../utils/hosts'
import { atWords } from '../utils/dayTime'

// A machine's card on the Hosts page (docs/plans/hosts.md, PR-3). The app's vitest mounts no component: the words are the functions
// the card draws from (hosts.test.ts), and its source and the page's are checked for the bindings that draw them; what a browser shows
// is hosts.spec.ts's.

const card = readFileSync(new URL('./HostCard.vue', import.meta.url), 'utf8')
const page = readFileSync(new URL('../pages/hosts.vue', import.meta.url), 'utf8')
const template = (src: string) => src.slice(src.indexOf('<template>'))

describe("a machine's card", () => {
  it('draws each availability as a neutral badge with its words, never in a status colour', () => {
    expect(card).toMatch(/<UBadge color="neutral" variant="subtle" :icon="ICON\[availability\]" :label="availabilityWords"/)
    expect(template(card)).not.toMatch(/color="(success|warning|error)"|text-success|text-warning|bg-success|bg-warning/)
    const at = '2026-11-05T14:02:00Z'
    const clock = (iso: string) => atWords(iso, Date.parse(at))
    expect([hostAvailabilityWords('here'), hostAvailabilityWords('connected', at, at), hostAvailabilityWords('away', at, at), hostAvailabilityWords('seen', at, at), remoteWords(at, at)]).toEqual([
      'Here',
      'Connected',
      `Away since ${clock(at)}`,
      `Last seen ${clock(at)}`,
      `Last reported ${clock(at)}`,
    ])
  })

  it("says Holds no runs on every host's card, and draws another machine's card as reported, read only", () => {
    expect(page).toMatch(/kind="host"[\s\S]*?run-line="Holds no runs"/)
    expect(page).toMatch(/kind="remote"\s+read-only[\s\S]*?availability="reported"[\s\S]*?:availability-words="remoteWords\(m\.lastReport, reply\.at\)"/)
  })

  it("names a session the page does not hold by its id, as evidence, and links each to its page", () => {
    expect(card).toMatch(/<span v-else class="min-w-0 truncate font-mono text-xs" data-mono="id" :title="s.id">\{\{ s.id \}\}<\/span>/)
    expect(card).toContain(':to="`/sessions/${encodeURIComponent(s.id)}`"')
  })

  it('has no control: its only actions are links, and a read-only card links none of its counts', () => {
    expect(template(card)).not.toMatch(/<(UButton|button|IconButton)\b/)
    expect(card).toContain('const waitingTo = computed(() => (props.readOnly ? undefined : waitingLink(props.sessions?.waiting, props.sessions?.waitingRef)))')
    expect(card).toContain('const decidedTo = computed(() => (props.readOnly ? undefined : waitingLink(props.decisions?.open, props.decisions?.waitingRef)))')
  })

  it('never draws the junction mark', () => {
    // The mark is artwork (public/brand/conductor-mark.svg), never a status light: neither the card nor the page draws it.
    for (const src of [template(card), template(page)]) expect(src).not.toMatch(/conductor-(mark|logo|app-icon|favicon)/)
  })
})
