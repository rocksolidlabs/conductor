<script setup lang="ts">
import { usePageAltCodes } from '~/composables/useShortcuts'
import { runTitle } from '~/utils/crewWords'
import { backByHistory, backTitle, BACK_TO_CREW, memberTabs, stripsRun } from '~/utils/memberStrip'

/**
 * A crew member's view carries its crew (UX3 R12): one strip above the session's header, at every width, with `Back to the crew`, the
 * run's title and a tab for each member, its state's mark beside its name, the open member's selected. The member is open inside its
 * crew (UX3 R72), at its run's page with `?session=`: a tab opens another member in its place (replace), so one Back leaves the crew's
 * members, and `Back to the crew` shows the run's own page (backByHistory). A member with no session yet is drawn and cannot be pressed.
 * A run the server no longer holds draws `Crews` in place of the way back, and no tabs.
 * More members than fit scroll inside the strip, the open one kept in view; an edge that cuts members draws how many are past it
 * (`+9`, a button beside the edge's fade, filled so that it shows on either theme), and a press of it scrolls that way. Its room is
 * taken as soon as the page knows the session is a member, so that the terminal under it is fitted once; the title and the tabs fill
 * in when the run is read.
 *
 * `inline`: the strip sits in the session bar's own row, left of the name, on a header wide enough for both (UX3 R12b); its tabs then
 * take at most part of the row and scroll inside it.
 *
 * Alt+Q goes back to the crew from anywhere on the page, the terminal included: the strip asks the terminal to hand KeyQ back
 * (usePageAltCodes, as the Yard does), so a page that is no member keeps Alt+Q for its terminal. Escape is not bound: in a terminal it
 * is the agent's own key. A run the server no longer holds, or a Brakeman turn's run, says `gone`, and the page draws no strip.
 */
const props = defineProps<{ runId: string; sessionId: string; inline?: boolean; room?: number }>()
// `room`: inline, the width the strip may take in the header's row (px): the session's name and meta yield before it does.
const emit = defineEmits<{ gone: [] }>()

const live = useAttention()
const router = useRouter()
const overlaid = useOverlaid()
const clock = useNow()
const run = computed(() => live.runOf(props.runId))
/** The run's read answered that the server no longer holds it. */
const missing = ref(false)
const to = computed(() => `/runs/${encodeURIComponent(props.runId)}`)
const title = computed(() => (run.value ? runTitle(run.value) : ''))
const tabs = computed(() => (run.value ? memberTabs(run.value, live.sessions.value, props.sessionId, clock.value) : []))

// A run the live store does not hold yet is read once; a run the server no longer holds draws no strip.
watch(
  () => props.runId,
  (id) => {
    if (live.runOf(id)) return
    void live.refreshRun(id).then(
      (r) => {
        if (!r && props.runId === id) missing.value = true
      },
      () => undefined,
    )
  },
  { immediate: true },
)
watch(run, (r) => r && !stripsRun(r) && emit('gone'), { immediate: true })

// The open member's tab in view, inside the strip's own scroll (a crew has up to twelve members): as it is drawn, when it changes, when
// members are added, and when the strip's width changes (the window, the strip moving into the header's row).
const list = useTemplateRef<HTMLElement>('list')
const openName = computed(() => tabs.value.find((t) => t.selected)?.name ?? '')
/**
 * The tabs past each edge of the strip, whole or cut: an edge with any draws its mark, the count beside the fade, so a person sees
 * that the crew goes on and how far. A fade alone said nothing where the edge fell between two tabs, nor on the dark theme.
 */
const more = ref<{ left: number; right: number }>({ left: 0, right: 0 })
function measureMore() {
  const el = list.value
  if (!el) return
  let [left, right] = [0, 0]
  if (el.scrollWidth > el.clientWidth + 1) {
    const box = el.getBoundingClientRect()
    for (const t of el.querySelectorAll<HTMLElement>('[data-member-tab]')) {
      const b = t.getBoundingClientRect()
      if (b.left < box.left - 1) left++
      else if (b.right > box.right + 1) right++
    }
  }
  if (left !== more.value.left || right !== more.value.right) more.value = { left, right }
}
/** The tabs' edges fade where they cut, as before; the marks beside them say how many. */
const fade = computed(() => {
  const { left, right } = more.value
  if (!left && !right) return undefined
  const mask = `linear-gradient(to right, ${left ? 'transparent, black 1.5rem' : 'black'}, ${right ? 'black calc(100% - 1.5rem), transparent' : 'black'})`
  return { maskImage: mask, WebkitMaskImage: mask }
})
/** A press of an edge's mark: the strip scrolls that way by most of its width. */
function scrollMore(dir: -1 | 1) {
  const el = list.value
  if (el) el.scrollBy({ left: dir * Math.max(80, el.clientWidth - 48), behavior: 'smooth' })
}
function keepInView(given?: HTMLElement) {
  measureMore()
  const el = list.value
  if (!el || (!given && !openName.value)) return
  const tab = given ?? el.querySelector<HTMLElement>(`[data-member-tab="${CSS.escape(openName.value)}"]`)
  if (!tab) return
  const [l, t] = [el.getBoundingClientRect(), tab.getBoundingClientRect()]
  // Clear of the edges' fade, so the open tab stays whole and readable.
  const pad = 24
  if (t.left < l.left + (el.scrollLeft > 0 ? pad : 0)) el.scrollLeft -= l.left - t.left + pad
  else if (t.right > l.right - pad) el.scrollLeft += t.right - l.right + pad
  measureMore()
}
watch([list, openName, () => tabs.value.length], () => void nextTick(keepInView), { immediate: true })
// The strip's room (the window, the strip moving into the header's row) keeps the open tab in view; the tabs' own width, which an
// edge's mark drawn or gone changes, is only measured again, so a press that scrolled away is not undone.
// (Not `room`: that is the prop the inline strip's width comes from.)
const tabsBox = useTemplateRef<HTMLElement>('tabsBox')
watch(
  [tabsBox, list],
  ([box, el], _prev, onCleanup) => {
    if (!box || !el) return
    const outer = new ResizeObserver(() => keepInView())
    const inner = new ResizeObserver(() => measureMore())
    outer.observe(box)
    inner.observe(el)
    onCleanup(() => {
      outer.disconnect()
      inner.disconnect()
    })
  },
  { immediate: true },
)

/** Back to the run's own page: the browser's Back when the person came from it, else a replace to it. */
function backToCrew() {
  if (backByHistory((window.history.state as { back?: unknown } | null)?.back, to.value)) router.back()
  else void router.replace(to.value)
}

usePageAltCodes().register(['KeyQ'])
// KeyQ is the page's while the strip is drawn, so Alt+Q always does what the strip's way back does: Crews while the run is missing.
defineShortcuts(computed(() => (overlaid.value ? {} : { alt_q: { usingInput: true, handler: () => (missing.value ? void router.push('/crews') : backToCrew()) } })))

const TAB = 'inline-flex flex-none items-center gap-1.5 rounded-md px-2 py-1 text-sm'
</script>

<template>
  <div
    class="flex min-w-0 items-center gap-2"
    :class="inline ? 'flex-none border-r border-default pr-3' : 'h-10 flex-none border-b border-default px-4 sm:px-6'"
    :style="inline && room ? { maxWidth: `${room}px` } : undefined"
    data-member-strip
    :data-run="runId"
    :data-strip-inline="inline ? '' : undefined"
  >
    <UButton v-if="missing" to="/crews" icon="i-lucide-arrow-left" label="Crews" color="neutral" variant="ghost" size="xs" class="flex-none" data-back-to-crews />
    <UTooltip v-else :text="backTitle(run)" :kbds="['alt', 'Q']">
      <UButton icon="i-lucide-arrow-left" :label="BACK_TO_CREW" color="neutral" variant="ghost" size="xs" class="flex-none" data-back-to-crew @click="backToCrew" />
    </UTooltip>
    <USeparator orientation="vertical" class="h-4 flex-none" />
    <span v-if="title" class="min-w-0 max-w-64 truncate text-sm font-semibold text-highlighted" :title="title" data-member-strip-title>{{ title }}</span>
    <div v-if="tabs.length" ref="tabsBox" class="flex min-w-0 flex-1 items-center gap-1">
      <UButton v-if="more.left" :label="`+${more.left}`" color="neutral" variant="soft" size="xs" class="flex-none tabular-nums" :aria-label="`${more.left} more ${more.left === 1 ? 'member' : 'members'} before`" tabindex="-1" data-tabs-edge="left" @click="scrollMore(-1)" />
      <nav
        ref="list"
        class="flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto [scrollbar-width:none]"
        :style="fade"
        aria-label="Members of the crew"
        data-member-tabs
        :data-tabs-more="more.left && more.right ? 'both' : more.left ? 'left' : more.right ? 'right' : undefined"
        @scroll.passive="measureMore"
        @focusin="(e: FocusEvent) => keepInView(((e.target as HTMLElement).closest('[data-member-tab]') as HTMLElement | null) ?? undefined)"
      >
        <template v-for="t in tabs" :key="t.name">
          <NuxtLink
            v-if="t.sessionId"
            :to="{ path: to, query: { session: t.sessionId } }"
            replace
            :class="[TAB, t.selected ? 'bg-elevated font-medium text-highlighted' : 'text-muted hover:bg-elevated/60 hover:text-default', 'focus-visible:outline-2 focus-visible:outline-primary']"
            :aria-current="t.selected ? 'page' : undefined"
            :data-member-tab="t.name"
            :data-selected="t.selected ? '' : undefined"
          >
            <StatusMark :status="t.mark" />
            <span class="whitespace-nowrap">{{ t.name }}</span>
          </NuxtLink>
          <span v-else :class="[TAB, 'cursor-default text-muted']" role="link" aria-disabled="true" :data-member-tab="t.name" data-member-tab-idle>
            <StatusMark :status="t.mark" />
            <span class="whitespace-nowrap">{{ t.name }}</span>
          </span>
        </template>
      </nav>
      <UButton v-if="more.right" :label="`+${more.right}`" color="neutral" variant="soft" size="xs" class="flex-none tabular-nums" :aria-label="`${more.right} more ${more.right === 1 ? 'member' : 'members'}`" tabindex="-1" data-tabs-edge="right" @click="scrollMore(1)" />
    </div>
  </div>
</template>
