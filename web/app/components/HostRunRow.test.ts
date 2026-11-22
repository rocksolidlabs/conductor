import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

// A run's row on the Hosts page (docs/plans/hosts.md, PR-3). Its words are runRowView's and remoteRunView's (hosts.test.ts: the five
// holders, a count made elsewhere, the holder transitions redrawn reply by reply); here, its source: it draws only the row it is
// given, its only control is the run's link, and another machine's run has none (I2, both halves).

const src = readFileSync(new URL('./HostRunRow.vue', import.meta.url), 'utf8')
const template = src.slice(src.indexOf('<template>'))

describe("a run's row", () => {
  it('draws only the row it is given: nothing kept from an earlier reply', () => {
    expect(src).toContain("const props = defineProps<{ row: RunRowView; remote?: boolean }>()")
    expect(src).not.toMatch(/\bref\(|\bwatch\(|useState\(/)
  })

  it('renders no button: the run link is its only action, and a remote row links nothing', () => {
    expect(template).not.toMatch(/<(UButton|button|IconButton)\b/)
    expect(template).toMatch(/<ULink\s+v-if="row\.link && !remote"/)
    expect(template).toMatch(/<ULink v-if="row\.waitingLink && !remote"/)
    expect(template.match(/<ULink\b/g)?.length).toBe(2)
  })

  it("draws the state in the status language, and another machine's run as reported, never held", () => {
    expect(src).toContain('const mark = computed(() => heldRunStatus(props.row.state, props.row.paused))')
    expect(template).toMatch(/<span v-if="remote"[^>]*data-host-run-reported>\{\{ row\.reported \}\}<\/span>/)
    expect(template).toMatch(/<span v-else[^>]*data-host-run-holder>/)
  })
})
