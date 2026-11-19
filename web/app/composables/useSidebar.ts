import { clampSidebarSize, openSidebarSize, readSidebarMode, readSidebarSize, sidebarBounds, writeSidebarMode, writeSidebarSize, type SidebarBounds, type SidebarMode } from '~/utils/sidebar'

/** The full sidebar's bounds in this window, in rem (Nuxt UI's rem: the root element's font size). */
function windowBounds(): SidebarBounds {
  if (!import.meta.client) return sidebarBounds(Number.NaN)
  return sidebarBounds(window.innerWidth, rootRem())
}

/** The root element's font size in px: Nuxt UI's rem. */
function rootRem(): number {
  return Number.parseFloat(getComputedStyle(document.documentElement).fontSize)
}

/**
 * The desktop sidebar's mode: full, or the icon rail. Kept in localStorage
 * alone (the layout turns Nuxt UI's own collapse cookie off and binds the
 * sidebar's collapsed state to this), per browser, so a wall left on a spare
 * monitor keeps its rail after a reload; an old "hidden" choice reads as the
 * rail. On narrow screens the sidebar is a slideover and the mode is ignored.
 *
 * The full sidebar's width (rem) is kept beside it, read once when the app
 * starts within the window's bounds (the opening width when none is saved, or
 * one below the minimum, #1022); `resize` saves the width a drag left.
 * `bounds` follows the window once a component calls `follow` (#437).
 */
export function useSidebar() {
  const mode = useState<SidebarMode>('sidebarMode', () => {
    if (!import.meta.client) return 'full'
    try {
      return readSidebarMode(localStorage)
    } catch {
      return 'full'
    }
  })
  const rail = computed(() => mode.value === 'rail')
  const bounds = useState<SidebarBounds>('sidebarBounds', windowBounds)
  const size = useState<number>('sidebarSize', () => {
    if (!import.meta.client) return openSidebarSize(bounds.value)
    try {
      return readSidebarSize(localStorage, bounds.value, window.innerWidth / rootRem())
    } catch {
      return openSidebarSize(bounds.value)
    }
  })

  function set(m: SidebarMode) {
    mode.value = m
    if (import.meta.client) {
      try {
        writeSidebarMode(localStorage, m)
      } catch {
        /* ignore */
      }
    }
  }

  function resize(width: number) {
    if (!Number.isFinite(width)) return
    size.value = clampSidebarSize(width, bounds.value)
    if (import.meta.client) {
      try {
        writeSidebarSize(localStorage, width, bounds.value)
      } catch {
        /* ignore */
      }
    }
  }

  /** Follows the window's width (and the rem's size) into `bounds`, for the component's life; called in its setup. */
  function follow() {
    const measure = () => (bounds.value = windowBounds())
    onMounted(() => {
      measure()
      window.addEventListener('resize', measure)
    })
    onBeforeUnmount(() => window.removeEventListener('resize', measure))
  }

  return {
    mode,
    rail,
    collapse: () => set('rail'),
    expand: () => set('full'),
    toggle: () => set(rail.value ? 'full' : 'rail'),
    size: readonly(size),
    bounds: readonly(bounds),
    resize,
    follow,
  }
}
