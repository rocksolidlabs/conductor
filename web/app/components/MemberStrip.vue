<script setup lang="ts">
import { usePageAltCodes } from '~/composables/useShortcuts'
import { runTitle } from '~/utils/crewWords'
import { backTitle, BACK_TO_CREW, stripsRun } from '~/utils/memberStrip'

/**
 * A crew member's page carries its crew (UX3 R12): one strip above the session's header, at every width, with `Back to the crew` and
 * the run's title. Its room is taken as soon as the page knows the session is a member, so that the terminal under it is fitted once;
 * the title fills in when the run is read. Alt+Q goes back to the crew from anywhere on the page, the terminal included: the strip
 * asks the terminal to hand KeyQ back (usePageAltCodes, as the Yard does), so a page that is no member keeps Alt+Q for its terminal.
 * Escape is not bound: in a terminal it is the agent's own key. A run the server no longer holds, or a Brakeman turn's run, says
 * `gone`, and the page draws no strip.
 */
const props = defineProps<{ runId: string }>()
const emit = defineEmits<{ gone: [] }>()

const live = useAttention()
const router = useRouter()
const overlaid = useOverlaid()
const run = computed(() => live.runOf(props.runId))
const to = computed(() => `/runs/${encodeURIComponent(props.runId)}`)
const title = computed(() => (run.value ? runTitle(run.value) : ''))

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

usePageAltCodes().register(['KeyQ'])
defineShortcuts(computed(() => (overlaid.value ? {} : { alt_q: { usingInput: true, handler: () => void router.push(to.value) } })))
</script>

<template>
  <div class="flex h-10 min-w-0 flex-none items-center gap-2 border-b border-default px-4 sm:px-6" data-member-strip :data-run="runId">
    <UTooltip :text="backTitle(run)" :kbds="['alt', 'Q']">
      <UButton :to="to" icon="i-lucide-arrow-left" :label="BACK_TO_CREW" color="neutral" variant="ghost" size="xs" class="flex-none" data-back-to-crew />
    </UTooltip>
    <USeparator orientation="vertical" class="h-4 flex-none" />
    <span v-if="title" class="min-w-0 truncate text-sm font-semibold text-highlighted" :title="title" data-member-strip-title>{{ title }}</span>
  </div>
</template>
