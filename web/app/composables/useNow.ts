/** How often the app's one clock ticks: a seconds counter in the sidebar is noise, and an age is said in minutes. */
export const NOW_TICK_MS = 15_000

const now = ref(Date.now())
let users = 0
let timer: ReturnType<typeof setInterval> | undefined

/**
 * One clock for the app's status words ("finished 12m ago"): a ref that ticks every NOW_TICK_MS while any mounted component uses it,
 * and stops when the last one unmounts. Called outside a component it is the clock as it stands, ticking only while others use it.
 */
export function useNow(): Readonly<Ref<number>> {
  if (getCurrentInstance()) {
    onMounted(() => {
      if (users++ === 0) {
        now.value = Date.now()
        timer = setInterval(() => (now.value = Date.now()), NOW_TICK_MS)
      }
    })
    onBeforeUnmount(() => {
      if (--users === 0 && timer !== undefined) {
        clearInterval(timer)
        timer = undefined
      }
    })
  }
  return readonly(now)
}
