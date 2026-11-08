import { computed, ref, watch } from 'vue'
import { MAX_SESSION_NAME } from '~/utils/protocol'
import { renameDraft } from '~/utils/sessionName'

/** The session the Rename dialog is open on (#318): its id, and its name as the dialog opened. */
export interface RenameTarget {
  id: string
  name: string
}

/** Open state of the Rename dialog, shared by a sidebar row's menu, the session page and the palette: one dialog, in the layout. */
export function useRenameModal() {
  const open = useState<boolean>('renameOpen', () => false)
  const target = useState<RenameTarget | null>('renameTarget', () => null)
  // Counts the dialog's openings: a save remembers the one it was sent from, and a reply that comes back after the dialog was
  // dismissed and opened again (on another session, or the same) leaves that later opening alone.
  const opening = useState<number>('renameOpening', () => 0)
  return {
    open,
    target,
    opening,
    show: (s: RenameTarget) => {
      target.value = { id: s.id, name: s.name }
      opening.value++
      open.value = true
    },
  }
}

/**
 * The Rename dialog's state and its save (RenameSessionModal), called in its setup: the draft, the save under way and a refusal.
 * `beforeSend` runs as a save starts, before the dialog shows it saving (the component keeps the focus in the dialog there).
 */
export function useRenameDialog(beforeSend: () => void = () => {}) {
  const rename = useRenameModal()
  const api = useSessions()
  const draft = ref('')
  const saving = ref(false)
  const refused = ref('')

  // The name as it is, selected as the field first takes the focus: typing replaces it, a click after places the caret.
  let selectNext = false
  // Every opening starts afresh, on its session's name: one made while the dialog is open (Rename… on another session from the
  // palette, over this dialog) included, which leaves open as it was and changes only the target and the opening.
  watch(rename.opening, () => {
    if (!rename.open.value) return
    draft.value = rename.target.value?.name ?? ''
    refused.value = ''
    // A save still under way belongs to an earlier opening: this one is not waiting on it.
    saving.value = false
    selectNext = true
  })
  function onFocus(e: FocusEvent) {
    if (!selectNext) return
    selectNext = false
    ;(e.target as HTMLInputElement).select()
  }
  watch(draft, () => (refused.value = ''))

  const state = computed(() => renameDraft(draft.value))
  const error = computed(() => (state.value.problem === 'long' ? `Too long: a name is at most ${MAX_SESSION_NAME} bytes, and this one is ${state.value.bytes}` : refused.value || undefined))

  async function save() {
    const t = rename.target.value
    if (!t || saving.value || state.value.problem) return
    // The name it has already: nothing to send.
    if (state.value.name === t.name) {
      rename.open.value = false
      return
    }
    // The opening this save is sent from: its reply closes the dialog or shows its refusal only while that opening lasts, never in
    // a dialog opened since (the save's dialog dismissed while it was under way, then Rename… on another session).
    const sentFrom = rename.opening.value
    const current = () => rename.open.value && rename.opening.value === sentFrom
    beforeSend()
    saving.value = true
    try {
      await api.rename(t.id, state.value.name)
      if (current()) rename.open.value = false
    } catch (e) {
      if (current()) refused.value = (e as Error).message
    } finally {
      if (rename.opening.value === sentFrom) saving.value = false
    }
  }

  return { rename, draft, saving, state, error, onFocus, save }
}
