<script setup lang="ts">
import { allowWay, answerLine, checkLine, pressTarget, wideQuestion, type AllowAnswer, type FolderLine, type RootCheck } from '~/utils/folderAllow'

/**
 * The line under a folder field (UX3 R30, R76): what the server's check says of the folder the field names, and, in the desktop app,
 * Allow this folder, one press. One component under every field where a person names a folder agents will work in.
 *
 * The check (GET /api/roots/check) runs for the text the field holds: at once when the text jumps (a pick, a paste), CHECK_PAUSE_MS
 * after typing stops, and again before a launch (check()); only the answer for the text in the field now counts, and one that does not
 * come in CHECK_WAIT_MS says nothing. The press asks the desktop app's bridge alone (the page never sends it), for exactly the folder
 * the check named for that text (pressTarget), named on the line whenever it is not the text as typed, one press at a time; a wide
 * folder asks first. A text changed after the check hides the button until the new answer comes, and a check answered after a press
 * decides over the press's answer. The text is checked as the field holds it, as a launch sends it; an empty field shows nothing (a
 * launch there runs in the agent's or the server's own folder, and the server's refusal says it if that cannot be used). The line
 * never takes the focus: the press is a person's own act.
 */
const props = withDefaults(
  defineProps<{
    /** The text of the field. */
    path: string
    /** Whether the field has the focus: a folder that does not exist yet is not said while it is typed. */
    focused?: boolean
    /** Skip checks (a field the form does not use now). */
    off?: boolean
  }>(),
  { focused: false, off: false },
)
const emit = defineEmits<{
  /** The link's folder put in the field (Use that folder). */
  use: [path: string]
  /** The folder was allowed by the press: the form goes on as if it always had been. */
  allowed: [root: string]
  /** The line's link to Settings was followed: a dialog around the field closes. */
  leave: []
}>()

/** How long typing must stop before the folder is checked, and how long a check is waited for. */
const CHECK_PAUSE_MS = 500
const CHECK_WAIT_MS = 5_000

const api = useSessions()
const admin = useWorkbenchToken()
const host = useServerHost()
const { isDesktop, bridge } = useDesktop()
onMounted(() => void host.load())

const way = computed(() => allowWay({ desktop: isDesktop.value, bridgePress: typeof bridge.value?.allowFolder === 'function', folders: host.folders.value }))

/** The last answer, with the text it was asked for and its place in the order of asks. */
const checked = ref<{ text: string; reply: RootCheck; n: number } | null>(null)
/**
 * What the press made of the line, for the text it was pressed for, with its place in the same order: it holds until a check asked
 * after it answers for that text, which then decides (the folder made again, or allowed in Settings, since).
 */
const pressed = ref<{ text: string; line: FolderLine; n: number } | null>(null)
/** The wide folder's question, shown in place of the line after the first press. */
const asking = ref<{ text: string; kind: NonNullable<FolderLine['wide']> } | null>(null)
const pressing = ref(false)
/** The text a launch was tried with: a folder missing is said for it even with the focus back in the field. */
const launchedWith = ref<string | null>(null)
/** The text and the place in the order of the last check answered or failed: a press's answer holds only until one after it. */
const lastAsked = ref<{ text: string; n: number } | null>(null)
let seq = 0
let timer: ReturnType<typeof setTimeout> | undefined

/** Checks the folder of text now: its answer, or null for none in time, an older server, or an answer overtaken by the text. */
async function checkNow(text: string): Promise<RootCheck | null> {
  if (!admin.hasToken.value) return null
  const n = ++seq
  const ctl = new AbortController()
  const t = setTimeout(() => ctl.abort(), CHECK_WAIT_MS)
  try {
    // The text as the field holds it, as the launch sends it: no trimming on one side only.
    const reply = await api.checkRoot(text, ctl.signal)
    if (n !== seq || text !== props.path) return null
    checked.value = { text, reply, n }
    lastAsked.value = { text, n }
    return reply
  } catch {
    if (n === seq && text === props.path) {
      checked.value = null
      lastAsked.value = { text, n }
    }
    return null
  } finally {
    clearTimeout(t)
  }
}

watch(
  () => [props.path, props.off] as const,
  ([text, off], old) => {
    clearTimeout(timer)
    if (off || !text) return
    const before = old?.[0] ?? ''
    // A jump (a pick, a paste, Use that folder, the dialog opening) is checked at once; typing, once it stops.
    const jump = !old || Math.abs(text.length - before.length) > 1 || !(text.startsWith(before) || before.startsWith(text))
    if (jump) void checkNow(text)
    else timer = setTimeout(() => void checkNow(text), CHECK_PAUSE_MS)
  },
  { immediate: true },
)
onBeforeUnmount(() => clearTimeout(timer))

/** The answer for the text the field holds now, else none (the button goes until the new answer comes). */
const current = computed(() => (checked.value && checked.value.text === props.path ? checked.value.reply : null))

const line = computed<FolderLine | null>(() => {
  if (props.off || !props.path) return null
  const p = pressed.value
  if (p && p.text === props.path && !(lastAsked.value && lastAsked.value.text === props.path && lastAsked.value.n > p.n)) return p.line
  const c = current.value
  if (!c) return null
  if (asking.value && asking.value.text === props.path) {
    return { state: 'wide', ...wideQuestion(asking.value.kind, pressTarget(c)), tone: 'warning', button: 'wide', wide: asking.value.kind, blocks: true }
  }
  return checkLine(c, way.value, (props.focused || fieldFocused.value) && launchedWith.value !== props.path, props.path)
})

/** Whether the folder as it stands may be launched in: false only for a line that blocks it. */
const blocks = computed(() => !!line.value?.blocks)

const root = useTemplateRef<HTMLElement>('root')

// Whether the field beside the line has the cursor (the line sits in the field's own wrapper): a folder not there yet is not said
// while it is typed, in every host.
const fieldFocused = ref(false)
function onFocusIn() {
  fieldFocused.value = true
}
function onFocusOut(e: FocusEvent) {
  const to = e.relatedTarget as Node | null
  if (!to || !root.value?.parentElement?.contains(to)) fieldFocused.value = false
}
onMounted(() => {
  const field = root.value?.parentElement
  field?.addEventListener('focusin', onFocusIn)
  field?.addEventListener('focusout', onFocusOut)
  fieldFocused.value = !!field && field.contains(document.activeElement)
})
onBeforeUnmount(() => {
  const field = root.value?.parentElement
  field?.removeEventListener('focusin', onFocusIn)
  field?.removeEventListener('focusout', onFocusOut)
})

/** The press: the folder the check named, asked of the app alone; confirmed names a wide folder's kind once its question is answered. */
async function allow(confirmed = '') {
  const c = current.value
  const b = bridge.value
  if (pressing.value || !c || !b?.allowFolder) return
  const text = props.path
  if (!confirmed && c.wide && line.value?.wide) {
    asking.value = { text, kind: line.value.wide }
    await nextTick()
    root.value?.querySelector<HTMLElement>('[data-folder-cancel]')?.focus()
    return
  }
  pressing.value = true
  let a: AllowAnswer
  try {
    a = (await b.allowFolder(pressTarget(c), confirmed)) as AllowAnswer
  } catch (e) {
    a = { ok: false, code: 'failed', message: (e as Error)?.message ?? '' }
  } finally {
    pressing.value = false
  }
  asking.value = null
  if (text !== props.path) return
  const l = answerLine(a, c, text)
  // After every ask before it: a check still on its way says nothing over the press's answer.
  pressed.value = { text, line: l, n: ++seq }
  if (a.ok) {
    emit('allowed', a.root)
  } else if (l.button === 'wide') {
    asking.value = { text, kind: l.wide! }
    pressed.value = null
  }
}

function cancelWide() {
  asking.value = null
  void nextTick(() => root.value?.querySelector<HTMLElement>('[data-launch-allow]')?.focus())
}

function use(folder: string) {
  pressed.value = null
  emit('use', folder)
}

/** Checks the folder now (before a launch): whether it may be launched in, as far as the check can tell. */
async function check(): Promise<boolean> {
  clearTimeout(timer)
  if (props.off || !props.path) return true
  launchedWith.value = props.path
  await checkNow(props.path)
  return !blocks.value
}

defineExpose({ check, blocks })
</script>

<template>
  <div ref="root" aria-live="polite" data-folder-line-root>
    <div v-if="line" class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1.5 text-sm" :data-launch-folder-line="''" :data-folder-state="line.state">
      <p class="min-w-0 [overflow-wrap:anywhere]" data-folder-words :class="line.tone === 'error' ? 'text-error' : line.tone === 'warning' ? 'text-warning' : 'text-default'">
        <UIcon v-if="line.tone === 'success'" name="i-lucide-check" class="me-1.5 inline size-4 align-[-3px] text-success" />
        <span>{{ line.words }}</span>
        <template v-if="line.folder">
          {{ ' ' }}<code class="font-mono [overflow-wrap:anywhere]" data-mono="path" data-folder-named>{{ line.folder }}</code>
        </template>
        <template v-if="line.settings">{{ ' ' }}<NuxtLink to="/settings?section=folders" class="underline" data-folder-settings @click="emit('leave')">Settings</NuxtLink></template>
        <span v-if="line.after">{{ line.after }}</span>
      </p>
      <UButton
        v-if="line.button === 'allow'"
        label="Allow this folder"
        icon="i-lucide-folder-plus"
        color="neutral"
        variant="outline"
        size="sm"
        class="max-sm:min-h-11"
        :loading="pressing"
        data-launch-allow
        @click="allow()"
      />
      <template v-else-if="line.button === 'wide'">
        <UButton label="Cancel" color="neutral" variant="outline" size="sm" class="max-sm:min-h-11" data-folder-cancel @click="cancelWide" />
        <UButton
          label="Allow the whole folder"
          icon="i-lucide-folder-plus"
          color="neutral"
          variant="outline"
          size="sm"
          class="max-sm:min-h-11"
          :loading="pressing"
          :data-folder-wide="line.wide"
          data-launch-allow-wide
          @click="allow(line.wide)"
        />
      </template>
      <UButton v-else-if="line.button === 'use' && line.folder" label="Use that folder" color="neutral" variant="outline" size="sm" class="max-sm:min-h-11" data-folder-use @click="use(line.folder)" />
    </div>
  </div>
</template>
