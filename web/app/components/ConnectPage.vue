<script setup lang="ts">
// The page a browser sees while it holds no workbench token, or the server refused the one it holds (the UX review's P18): the mark,
// one sentence, one field and Connect, and where the token is, in words that are true without saying where this server keeps its data.
// There is no Cancel: the page leaves when a token is given, and comes back if the server refuses it. The app under it is hidden, not
// unmounted (layouts/default.vue), so a refusal in the middle of work loses nothing.
const admin = useWorkbenchToken()
const draft = ref('')
/** The server refused the token this page holds. A page that holds none is asked for one, never told that one was refused. */
const refused = computed(() => admin.standing.value === 'refused' && admin.hasToken.value)

// The field starts from the token this browser keeps (none in the desktop app), as the dialog's does.
onMounted(() => (draft.value = admin.kept()))

/** The Escape this page sends to close what of the app is open over it: let through to the layers it is meant for. */
let closing: KeyboardEvent | null = null
// The app's keys (N, ?, the palette's, the G chords) belong to the app under the page: none of them opens anything over it. Taken
// first and kept from every listener, the key still types, tabs and submits, which are the browser's own.
function holdKeys(e: KeyboardEvent) {
  if (e === closing) return
  e.stopImmediatePropagation()
}

/**
 * What of the app is open over the page: its dialogs, slideovers, menus and popovers are dismissable layers, drawn at the body's end,
 * outside the hidden app. Open, one would cover this page, hold the focus and keep the keys from it.
 */
const LAYER = '[data-dismissable-layer]'
const layers = () => document.querySelectorAll(LAYER).length
const field = useTemplateRef<{ inputRef?: HTMLInputElement | null }>('field')
const root = useTemplateRef<HTMLElement>('root')
/** The field takes the focus, unless the page already holds it (a person typing, tabbing to Connect). */
function focusField() {
  if (root.value?.contains(document.activeElement)) return
  field.value?.inputRef?.focus()
}
/** Waits until fewer than `n` layers are open; false when none closed within a second (one busy, which waits for its answer). */
async function fewer(n: number): Promise<boolean> {
  for (let i = 0; i < 20; i++) {
    await new Promise((r) => setTimeout(r, 50))
    if (layers() < n) return true
  }
  return false
}
let shutting = false
let again = false
/**
 * Closes, top first, every overlay of the app that is open as the page appears (an Alerts panel, the Launch dialog, the palette, the
 * phone's list) or that opens while it shows, each with the Escape its own layer closes on, then gives the field the focus. The close's
 * own return of the focus (to what opened it, in the hidden app) comes first: the field is focused after it.
 */
async function closeOverlays() {
  if (shutting) {
    again = true
    return
  }
  shutting = true
  try {
    do {
      again = false
      for (let n = layers(); n > 0; n = layers()) {
        closing = new KeyboardEvent('keydown', { key: 'Escape', code: 'Escape', bubbles: true, cancelable: true })
        window.dispatchEvent(closing)
        closing = null
        if (!(await fewer(n))) break
      }
    } while (again)
  } finally {
    shutting = false
  }
  focusField()
}
// An overlay the app opens while the page shows is drawn at the body's end too: it is closed as it comes.
let watcher: MutationObserver | undefined
onMounted(() => {
  window.addEventListener('keydown', holdKeys, { capture: true })
  watcher = new MutationObserver(() => {
    if (layers()) void closeOverlays()
  })
  watcher.observe(document.body, { childList: true })
  void closeOverlays()
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', holdKeys, { capture: true })
  watcher?.disconnect()
})

function connect() {
  if (draft.value.trim()) admin.set(draft.value)
}
</script>

<template>
  <main ref="root" class="flex min-h-dvh items-center justify-center bg-default p-4 sm:p-8" aria-labelledby="connect-title" :data-standing="admin.standing.value || undefined" data-connect-page>
    <div class="flex w-full max-w-md flex-col gap-6">
      <div class="flex flex-col items-center gap-3 text-center">
        <img src="/brand/conductor-mark.svg" alt="" class="size-12 dark:hidden" data-connect-mark="light" />
        <img src="/brand/conductor-mark-reversed.svg" alt="" class="hidden size-12 dark:block" data-connect-mark="dark" />
        <h1 id="connect-title" class="text-xl font-semibold text-highlighted">Connect to Conductor</h1>
        <p class="text-sm text-muted">This server asks for its workbench token before it shows anything.</p>
      </div>
      <UAlert v-if="refused" color="warning" variant="subtle" icon="i-lucide-key-round" title="The server refused the token this browser keeps. Paste the one it uses now." data-connect-refused />
      <form class="flex flex-col gap-3" @submit.prevent="connect">
        <UFormField label="Workbench token" name="token">
          <UInput ref="field" v-model="draft" type="password" autocomplete="off" placeholder="paste the workbench token" size="lg" class="w-full" autofocus />
        </UFormField>
        <UButton label="Connect" type="submit" size="lg" block :disabled="!draft.trim()" class="pointer-coarse:min-h-11" data-connect-submit />
      </form>
      <section class="flex flex-col gap-2 rounded-lg border border-default bg-elevated/50 p-4 text-sm text-muted" data-connect-where>
        <h2 class="text-xs font-semibold uppercase tracking-wider text-muted">Where to find it</h2>
        <p>
          If you set <code class="font-mono text-highlighted">CONDUCTOR_WORKBENCH_TOKEN</code>, or <code class="font-mono text-highlighted">workbenchToken</code> in the config, it is that. If not, the server made one as it started and keeps it in the file
          <code class="font-mono text-highlighted">workbench-token</code> in its data directory; the terminal it started in printed that file's path.
        </p>
        <p>With the default data directory: <code class="font-mono text-highlighted">cat ~/.conductor/workbench-token</code></p>
      </section>
    </div>
  </main>
</template>
