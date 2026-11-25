import { describe, expect, it } from 'vitest'
import { filesItems, inspectorItems, itemTooltip, pickItem, pillText, pollInterval, railView } from './inspector'

describe('the inspector rail', () => {
  it('lists its items in order, the people\'s chat in People', () => {
    const counts = { viewers: 2, changes: 379, commits: 9, unread: 2, chat: true }
    expect(inspectorItems(counts).map((i) => `${i.id}${i.count !== undefined ? ':' + i.count : ''}`)).toEqual(['people:2', 'explorer', 'changes:379', 'commits:9', 'activity'])
    expect(inspectorItems({ ...counts, chat: false }).map((i) => i.id)).toEqual(['people', 'explorer', 'changes', 'commits', 'activity'])
    expect(filesItems({ changes: 1, commits: 0 }).map((i) => `${i.id}:${i.count}`)).toEqual(['explorer:undefined', 'changes:1', 'commits:0'])
  })

  it('caps a pill at 99+ and shows none for nothing', () => {
    expect(pillText(0)).toBe('')
    expect(pillText(undefined)).toBe('')
    expect(pillText(9)).toBe('9')
    expect(pillText(99)).toBe('99')
    expect(pillText(379)).toBe('99+')
  })

  it('names a tooltip by the view, its sub-line or its count', () => {
    const [people, explorer, changes, commits, activity] = inspectorItems({ viewers: 2, changes: 379, commits: 9, unread: 2, chat: true, subs: { commits: '9 on feat/scoping-suggestions', people: '2 here now · 1 link' } })
    expect(itemTooltip(commits!)).toBe('Commits · 9 on feat/scoping-suggestions')
    expect(itemTooltip(people!)).toBe('People · 2 here now · 1 link · 2 unread')
    expect(itemTooltip(changes!)).toBe('Changes · 379')
    expect(itemTooltip(explorer!)).toBe('Explorer')
    expect(itemTooltip(activity!)).toBe('Activity')
  })

  it('polls fast for the view shown and slowly otherwise', () => {
    expect(pollInterval('changes', 'changes')).toBe(5000)
    expect(pollInterval('commits', 'commits')).toBe(10_000)
    expect(pollInterval('changes', 'commits')).toBe(30_000)
    expect(pollInterval('commits', null)).toBe(30_000)
  })

  it('collapses on the active item and opens on any other; a rail that cannot collapse re-picks', () => {
    expect(pickItem('commits', 'commits', true)).toEqual({ view: 'commits', open: false })
    expect(pickItem('commits', 'commits', false)).toEqual({ view: 'commits', open: true })
    expect(pickItem('people', 'commits', true)).toEqual({ view: 'people', open: true })
    expect(pickItem('commits', 'commits', true, false)).toEqual({ view: 'commits', open: true })
  })

  it('peopleIsOneItem: the rail lists People once, holding the chat; both tabs open it; its pill is the unread count while there is one', () => {
    const items = inspectorItems({ viewers: 3, changes: 0, commits: 0, unread: 0, chat: true })
    expect(items.filter((i) => i.label === 'People' || i.id === 'chat' || i.label === 'Chat')).toHaveLength(1)
    const people = items.find((i) => i.id === 'people')!
    expect(people).toMatchObject({ chat: true, count: 3 })
    expect(people.countOf).toBeUndefined()
    expect(itemTooltip(people)).toBe('People · 3 here')
    const unread = inspectorItems({ viewers: 3, changes: 0, commits: 0, unread: 4, chat: true }).find((i) => i.id === 'people')!
    expect(unread).toMatchObject({ count: 4, countOf: 'chat' })
    expect(itemTooltip(unread)).toBe('People · 4 unread')
    // ?tab=chat and ?tab=people, a palette's Open Chat of old: the same view, and the shown one collapses on either.
    expect(railView('chat')).toBe('people')
    expect(railView('people')).toBe('people')
    expect(pickItem('people', 'chat', true)).toEqual({ view: 'people', open: false })
    expect(pickItem('chat', 'people', true)).toEqual({ view: 'people', open: false })
  })
})
