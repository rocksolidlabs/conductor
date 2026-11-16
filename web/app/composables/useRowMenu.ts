import type { InjectionKey, Ref, WritableComputedRef } from 'vue'

/** The row whose More menu is open in one list (`s:<session id>`), or null: kept by the list, not by the row. */
const ROW_MENU: InjectionKey<Ref<string | null>> = Symbol('rowMenu')

/**
 * Gives a list one place for its open row menu. A row moves between the list's sections as its state changes (Working, Finished,
 * Needs you, Exited: a turn ending, a question asked), and a row moved into another section is drawn there anew: a menu whose open
 * state lived in the row closed with the row it was opened on, under the person's pointer. Kept here, the menu opens again on the
 * row as it is drawn now, and stays open until the person closes it.
 */
export function provideRowMenu(): Ref<string | null> {
  const open = ref<string | null>(null)
  provide(ROW_MENU, open)
  return open
}

/**
 * Whether the menu of the row `id` is open, for a row's UDropdownMenu `v-model:open`. Outside a list that provides the place (a
 * row drawn alone) the state is the row's own. A row being taken away (moved to another section) does not close the menu: only
 * the person does, from the row as it is drawn.
 */
export function useRowMenu(id: () => string): WritableComputedRef<boolean> {
  const open = inject(ROW_MENU, null) ?? ref<string | null>(null)
  let alive = true
  onBeforeUnmount(() => (alive = false))
  return computed({
    get: () => open.value === id(),
    set: (on: boolean) => {
      if (!alive) return
      if (on) open.value = id()
      else if (open.value === id()) open.value = null
    },
  })
}
