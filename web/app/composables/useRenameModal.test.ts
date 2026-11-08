import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, ref, type Ref } from 'vue'
import { useRenameDialog } from './useRenameModal'

// The Rename dialog dismissed while its save is on its way (#281, PR #742's review round 1): Escape closes it then (the field keeps
// the focus through the save, and the dialog's v-model takes the dismissal), and the save's reply, late, acts on nothing; a dialog
// opened since, on another session or the same, keeps what it shows. useRenameDialog reads Nuxt's auto-imports (useState,
// useRenameModal, useSessions) as globals: each is stubbed, every rename held until the test answers it.

let states: Map<string, Ref<unknown>>
let held: Array<{ id: string; name: string; answer: () => void; fail: (e: Error) => void }>
let focusKept: Array<{ saving: boolean; sent: number }>
beforeEach(async () => {
  states = new Map()
  held = []
  focusKept = []
  vi.stubGlobal('useState', <T>(key: string, init: () => T) => {
    if (!states.has(key)) states.set(key, ref(init()))
    return states.get(key) as Ref<T>
  })
  vi.stubGlobal('useRenameModal', (await import('./useRenameModal')).useRenameModal)
  vi.stubGlobal('useSessions', () => ({
    rename: (id: string, name: string) => new Promise((resolve, reject) => held.push({ id, name, answer: () => resolve({ id, name }), fail: reject })),
  }))
})
afterEach(() => vi.unstubAllGlobals())

const settle = () => new Promise((r) => setTimeout(r, 0))

/** The dialog, opened on `id` (named `name-<id>`), with `draft` typed and saved: the rename held, the dialog saving. */
async function savingOn(dialog: ReturnType<typeof useRenameDialog>, id: string, draft: string) {
  dialog.rename.show({ id, name: `name-${id}` })
  await nextTick()
  dialog.draft.value = draft
  const sent = dialog.save()
  expect(dialog.saving.value).toBe(true)
  expect(held.at(-1)).toMatchObject({ id, name: draft })
  // In an object: an async function's returned promise would be waited for, and the save is held.
  return { sent }
}

describe('the Rename dialog dismissed while its save is on its way', () => {
  it('keeps the focus in the dialog before it shows the save, so Escape there reaches it', async () => {
    // The component's hook moves the focus to the field: it runs once, before the Rename button goes disabled and the rename is sent.
    const dialog: ReturnType<typeof useRenameDialog> = useRenameDialog(() => focusKept.push({ saving: dialog.saving.value, sent: held.length }))
    await savingOn(dialog, 'a', 'renamed a')
    expect(focusKept).toEqual([{ saving: false, sent: 0 }])
  })

  it("leaves the dialog opened since on another session alone when the save's reply comes", async () => {
    const dialog = useRenameDialog()
    const { sent } = await savingOn(dialog, 'a', 'renamed a')
    // Escape: the dialog's v-model closes it, the save still on its way.
    dialog.rename.open.value = false
    dialog.rename.show({ id: 'b', name: 'name-b' })
    await nextTick()
    expect(dialog.draft.value).toBe('name-b')
    expect(dialog.saving.value).toBe(false)
    held[0]!.answer()
    await sent
    await settle()
    expect(dialog.rename.open.value).toBe(true)
    expect(dialog.rename.target.value).toEqual({ id: 'b', name: 'name-b' })
    expect(dialog.draft.value).toBe('name-b')
    expect(dialog.saving.value).toBe(false)
    expect(dialog.error.value).toBeUndefined()
  })

  it("shows no refusal of the dismissed save in the dialog opened since, the same session's included", async () => {
    const dialog = useRenameDialog()
    const { sent } = await savingOn(dialog, 'a', 'renamed a')
    dialog.rename.open.value = false
    dialog.rename.show({ id: 'a', name: 'name-a' })
    await nextTick()
    held[0]!.fail(new Error('refused: the session is gone'))
    await sent
    await settle()
    expect(dialog.rename.open.value).toBe(true)
    expect(dialog.error.value).toBeUndefined()
    expect(dialog.saving.value).toBe(false)
  })

  it('opens nothing when the reply comes with the dialog still dismissed', async () => {
    const dialog = useRenameDialog()
    const { sent } = await savingOn(dialog, 'a', 'renamed a')
    dialog.rename.open.value = false
    held[0]!.answer()
    await sent
    await settle()
    expect(dialog.rename.open.value).toBe(false)
  })

  it('closes the dialog on its reply when it was not dismissed', async () => {
    const dialog = useRenameDialog()
    const { sent } = await savingOn(dialog, 'a', 'renamed a')
    held[0]!.answer()
    await sent
    expect(dialog.rename.open.value).toBe(false)
    expect(dialog.saving.value).toBe(false)
  })
})
