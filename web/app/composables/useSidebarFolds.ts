import { DEFAULT_FOLDS, readFolds, writeFolds, type Folds, type SectionKey } from '~/utils/sidebar'

/**
 * Which sidebar sections are folded, per browser (localStorage, as the mode
 * and the width are). Exited starts folded; the list unfolds Needs you by
 * itself when a new prompt arrives (reopenNeeds), so folding it never hides one.
 */
export function useSidebarFolds() {
  const folds = useState<Folds>('sidebarFolds', () => {
    if (!import.meta.client) return { ...DEFAULT_FOLDS }
    try {
      return readFolds(localStorage)
    } catch {
      return { ...DEFAULT_FOLDS }
    }
  })

  const ui = useUiState()

  function set(next: Folds) {
    folds.value = next
    ui.setLayout({ sidebar: { folds: { ...next } } })
    if (import.meta.client) {
      try {
        writeFolds(localStorage, next)
      } catch {
        /* ignore */
      }
    }
  }

  function fold(key: SectionKey, folded: boolean) {
    if (folds.value[key] !== folded) set({ ...folds.value, [key]: folded })
  }

  return {
    folds: readonly(folds),
    set,
    fold,
    toggle: (key: SectionKey) => fold(key, !folds.value[key]),
    unfold: (key: SectionKey) => fold(key, false),
  }
}
