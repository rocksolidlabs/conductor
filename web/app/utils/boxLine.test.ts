import { describe, expect, it } from 'vitest'
import { afterPick, expandsLine, freeHeld, listsOpen, markInput, ownInput } from './boxLine'
import { OwnLines, RowDrafts } from './rowDrafts'

// Whose the chat box's line is (UX3 R10): only the person's own typing, line breaks, composing, pasting and deleting, or their own
// pick, make a line theirs; only such a line, unchanged, goes for the agent to expand, and only where the server said so.

const typing = (inputType: string) => ({ isTrusted: true, inputType })

describe("whose the box's line is", () => {
  it('ownInputIsTypingAndPastingAlone: the kinds on the list, trusted; a drop, a spelling fix, a suggestion and anything else are not', () => {
    for (const t of ['insertText', 'insertLineBreak', 'insertCompositionText', 'insertFromPaste', 'deleteContentBackward', 'deleteWordBackward', 'deleteByCut']) {
      expect(ownInput(typing(t)), t).toBe(true)
    }
    for (const t of ['insertFromDrop', 'insertReplacementText', 'insertFromYank', 'historyUndo', 'deleteByDrag', 'insertLink', 'formatBold', '', 'somethingNew']) {
      expect(ownInput(typing(t)), t).toBe(false)
    }
    expect(ownInput({ isTrusted: false, inputType: 'insertText' })).toBe(false)
    expect(ownInput({ isTrusted: true })).toBe(false)
  })

  it('aDropIsThePagesLine: text dropped into a line the person typed makes the whole line the page\'s', () => {
    const own = new OwnLines()
    markInput(own, 's', typing('insertText'), 'look at ')
    expect(expandsLine(true, own, 's', 'look at ')).toBe(true)
    markInput(own, 's', typing('insertFromDrop'), 'look at /review')
    expect(expandsLine(true, own, 's', 'look at /review')).toBe(false)
    // The person changes it afterwards: theirs again.
    markInput(own, 's', typing('deleteContentBackward'), 'look at /revie')
    expect(expandsLine(true, own, 's', 'look at /revie')).toBe(true)
  })

  it("codeTakesOwnershipBack: what code writes into a box the person typed in and emptied is the page's", () => {
    const own = new OwnLines()
    markInput(own, 's', typing('insertText'), 'x')
    markInput(own, 's', typing('deleteContentBackward'), '')
    // Code's own input event, untrusted.
    markInput(own, 's', { isTrusted: false, inputType: 'insertText' }, '/compact')
    expect(expandsLine(true, own, 's', '/compact')).toBe(false)
    // Code that sets the text with no event at all: the record does not hold that text.
    markInput(own, 's', typing('insertText'), 'mine')
    expect(expandsLine(true, own, 's', '/compact')).toBe(false)
  })

  it('aFreedAnswerGoesAsWritten: a line held because its question was already answered here goes free as the page\'s', () => {
    const lines = new RowDrafts()
    const own = new OwnLines()
    lines.edit('s', '/clear', 'q1')
    own.typed('s', '/clear')
    expect(freeHeld(lines, own, 's', '/clear')).toBe(true)
    expect(lines.get('s')).toEqual({ text: '/clear' })
    expect(expandsLine(true, own, 's', '/clear')).toBe(false)
    // A box that holds another line by then is left as it is.
    lines.edit('s', 'other', undefined)
    own.typed('s', 'other')
    expect(freeHeld(lines, own, 's', '/clear')).toBe(false)
    expect(expandsLine(true, own, 's', 'other')).toBe(true)
  })

  it("expandsOnlyOnTheServersWord: the person's own line goes for expand only where the server said so", () => {
    const own = new OwnLines()
    own.typed('s', '/compact')
    expect(expandsLine(false, own, 's', '/compact')).toBe(false)
    expect(expandsLine(true, own, 's', '/compact')).toBe(true)
  })

  it("aPickNeverMakesThePagesLineThePersons: the person's own press into their own line keeps it theirs; into the page's line it stays the page's", () => {
    const own = new OwnLines()
    afterPick(own, 's', '/compact ', { trusted: true, wasMine: false })
    expect(expandsLine(true, own, 's', '/compact ')).toBe(false)
    afterPick(own, 's', '@notes.txt ', { trusted: false, wasMine: true })
    expect(expandsLine(true, own, 's', '@notes.txt ')).toBe(false)
    afterPick(own, 's', '@notes.txt ', { trusted: true, wasMine: true })
    expect(expandsLine(true, own, 's', '@notes.txt ')).toBe(true)
  })

  it("listsOpenOnThePersonsOwnLineAlone, and never while the box would answer", () => {
    expect(listsOpen({ mine: true, answering: false })).toBe(true)
    expect(listsOpen({ mine: false, answering: false })).toBe(false)
    expect(listsOpen({ mine: true, answering: true })).toBe(false)
  })
})
