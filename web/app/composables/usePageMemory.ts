import { ref, type Ref } from 'vue'

/** Where the values live: a property of this page's Nuxt app, outside its payload. */
const MEMORY = '_conductorPageMemory'

/**
 * A value of this page shared by every caller under `key`, as useState's is, and kept in the page's memory as a property of its Nuxt
 * app, outside its payload, so no state Nuxt writes at a reload it makes itself (`nuxt:reload:state`, the payload's `state`) holds
 * it: for a value that holds a credential (a joined link's token, a run's link shown once). The workbench token's store is kept the
 * same way (useWorkbenchToken).
 */
export function usePageMemory<T>(key: string, init: () => T): Ref<T> {
  const app = useNuxtApp() as unknown as { [MEMORY]?: Map<string, Ref<unknown>> }
  const memory = (app[MEMORY] ??= new Map())
  if (!memory.has(key)) memory.set(key, ref(init()))
  return memory.get(key) as Ref<T>
}
