import { describe, expect, it } from 'vitest'
import { MAX_DECISION_EVENT, type DecisionEvent } from './protocol'

// The `decision` event's data (docs/protocol.md "Decisions"): its largest, every field at its longest as the server writes it,
// fits the bound the server holds it to (internal/api, maxDecisionEvent, TestDecisionEventIsBounded).
describe('the decision event', () => {
  it('fits its bound at its largest', () => {
    const largest: DecisionEvent = {
      id: 'dr-9223372036854775807',
      run: `${'c'.repeat(64)}-0123abcd`,
      task: 't'.repeat(40),
      state: 'superseded',
      rev: 2 ** 31 - 1,
    }
    expect(new TextEncoder().encode(JSON.stringify(largest)).length).toBeLessThanOrEqual(MAX_DECISION_EVENT)
    expect(MAX_DECISION_EVENT).toBe(256)
  })
})
