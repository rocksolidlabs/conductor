<script setup lang="ts">
import { usePageAltCodes } from '~/composables/useShortcuts'
import { runTitle } from '~/utils/crewWords'
import { backTitle, BACK_TO_CREW, memberTabs, stripsRun } from '~/utils/memberStrip'

/**
 * A crew member's page carries its crew (UX3 R12): one strip above the session's header, at every width, with `Back to the crew`, the
 * run's title and a tab for each member, its state's mark beside its name, the open member's selected. A tab opens that member's page
 * in this one's place (replace), so one Back leaves the crew's members; a member with no session yet is drawn and cannot be pressed.
 * More members than fit scroll inside the strip, the open one kept in view. Its room is taken as soon as the page knows the session is
 * a member, so that the terminal under it is fitted once; the title and the tabs fill in when the run is read.
 *
 * Alt+Q goes back to the crew from anywhere on the page, the terminal included: the strip asks the terminal to hand KeyQ back
 * (usePageAltCodes, as the Yard does), so a page that is no member keeps Alt+Q for its terminal. Escape is not bound: in a terminal it
 * is the agent's own key. A run the server no longer holds, or a Brakeman turn's run, says `gone`, and the page draws no strip.
 */
const props = defineProps<{ runId: string; sessionId: string }>()
const emit = defineEmits<{ gone: [] }>()

const live = useAttention()
const router = useRouter()
const overlaid = useOverlaid()
const clock = useNow()
const run = computed(() => live.runOf(props.runId))
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
        if (!r && props.runId === id) emit('gone')
      },
      () => undefined,
    )
  },
  { immediate: true },
)
watch(run, (r) => r && !stripsRun(r) && emit('gone'), { immediate: true })

// The open member's tab in view, inside the strip's own scroll (a crew has up to twelve members), as it is drawn and when it changes.
const list = useTemplateRef<HTMLElement>('list')
watch(
  [list, () => tabs.value.find((t) => t.selected)?.name],
  ([el, name]) => {
    if (!el || !name) return
    void nextTick(() => {
      const tab = el.querySelector<HTMLElement>(`[data-member-tab="${CSS.escape(name)}"]`)
      if (!tab) return
      const left = tab.offsetLeft - el.offsetLeft
      if (left < el.scrollLeft) el.scrollLeft = left
      else if (left + tab.offsetWidth > el.scrollLeft + el.clientWidth) el.scrollLeft = left + tab.offsetWidth - el.clientWidth
    })
  },
  { immediate: true },
)

usePageAltCodes().register(['KeyQ'])
defineShortcuts(computed(() => (overlaid.value ? {} : { alt_q: { usingInput: true, handler: () => void router.push(to.value) } })))

const TAB = 'inline-flex flex-none items-center gap-1.5 rounded-md px-2 py-1 text-sm'
</script>

<template>
  <div class="flex h-10 min-w-0 flex-none items-center gap-2 border-b border-default px-4 sm:px-6" data-member-strip :data-run="runId">
    <UTooltip :text="backTitle(run)" :kbds="['alt', 'Q']">
      <UButton :to="to" icon="i-lucide-arrow-left" :label="BACK_TO_CREW" color="neutral" variant="ghost" size="xs" class="flex-none" data-back-to-crew />
    </UTooltip>
    <USeparator orientation="vertical" class="h-4 flex-none" />
    <span v-if="title" class="min-w-0 max-w-64 truncate text-sm font-semibold text-highlighted" :title="title" data-member-strip-title>{{ title }}</span>
    <nav v-if="tabs.length" ref="list" class="flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto [scrollbar-width:none]" aria-label="Members of the crew" data-member-tabs>
      <template v-for="t in tabs" :key="t.name">
        <NuxtLink
          v-if="t.sessionId"
          :to="`/sessions/${encodeURIComponent(t.sessionId)}`"
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
  </div>
</template>
