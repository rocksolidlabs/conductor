<script setup lang="ts">
import type { RunInfo, SessionInfo } from '~/composables/useSessions'
import type { CrewFeedItem } from '~/utils/crews'
import type { ChatMessage } from '~/utils/protocol'
import type { TerminalTransport } from '~/utils/transport/types'
import { memberAsk, memberLook } from '~/utils/crewWords'
import { memberOffer } from '~/utils/runs'
import { bestGrid, lastItemSpan, type RunWallTile } from '~/utils/wall'

/**
 * The run page's Members view: a tile for every member of one run in the run's order (a live terminal where the member has a session,
 * else a placeholder that says its state and offers Start now or Resume), and the run's activity. Every tile and the activity card fit
 * the area, as on the wall; on a narrow area they stack and the area scrolls. The page hands it the run and its tiles and takes back
 * what a person asks of a tile (open, start); a member's question is answered through the page's `answer`.
 */
const props = defineProps<{
  tiles: RunWallTile[]
  run: RunInfo | null
  runId: string
  /** Every session the live store holds: a member's state (memberLook) reads the sessions of its run. */
  sessions: readonly SessionInfo[]
  feed: CrewFeedItem[]
  now: number
  /** The member whose start the page has asked for, '' for none: its Start now spins. */
  starting: string
  createTransport: (s: SessionInfo) => () => TerminalTransport
  /** Whether the page may answer a question line (useRunChatAnswer): a tile offers its choices only then. */
  answerable: (line: ChatMessage) => boolean
  answer: (line: ChatMessage, index: number) => Promise<void>
  /** The members that will get the next line from the run's message bar: their tiles carry the ring. */
  receiving?: readonly string[]
}>()

const emit = defineEmits<{ open: [sessionId: string]; start: [name: string] }>()

defineSlots<{
  /** The page's own control beside a member's tile (SessionTile's leading slot). */
  leading?: (p: { tile: RunWallTile }) => unknown
}>()

function footer(t: RunWallTile): { where: string; diff: string } {
  const m = t.member
  const branch = m?.branch || t.session?.branch || (props.run?.isolation === 'worktree' ? '' : 'shared cwd')
  // A managed run's member is on a task: `build #2 · crew/<run>/build.2`.
  const where = m?.task ? `${m.task} #${m.attempt}${branch ? ` · ${branch}` : ''}` : branch
  const diff = m?.diff ? `+${m.diff.added} −${m.diff.removed}` : ''
  return { where, diff }
}

/**
 * A member's state in the Crews pages' one vocabulary (C2, memberLook): the tile's foot and a tile without a session say it, with
 * whom it waits for ("after driver") on a member not started.
 */
function looked(t: RunWallTile) {
  return t.member && props.run ? memberLook(props.run, t.member, props.sessions, props.now) : null
}

/**
 * A tile's question with its choices (C6): the same line the run's chat offers, answered through the same path (the member's running
 * session, the question named); none where the page cannot answer it.
 */
function tileAsk(t: RunWallTile) {
  const line = memberAsk(t.name, t.session).line
  return line && props.answerable(line) ? line : null
}

// Every tile and the activity card fit on screen, as on the wall; on a narrow screen they stack and the page scrolls.
const grid = useTemplateRef<HTMLDivElement>('grid')
const box = ref({ w: 0, h: 0 })
watch(
  grid,
  (el, _prev, onCleanup) => {
    if (!el) return
    const observer = new ResizeObserver((entries) => {
      const r = entries[0]?.contentRect
      if (r) box.value = { w: r.width, h: r.height }
    })
    observer.observe(el)
    onCleanup(() => observer.disconnect())
  },
  { immediate: true },
)
const narrow = computed(() => box.value.w > 0 && box.value.w < 640)
const layout = computed(() => bestGrid(props.tiles.length + 1, box.value.w, box.value.h, 12, 1.4))
const feedSpan = computed(() => lastItemSpan(props.tiles.length + 1, layout.value))
const gridStyle = computed(() =>
  narrow.value
    ? { gridTemplateColumns: 'minmax(0, 1fr)', gridAutoRows: '16rem' }
    : { gridTemplateColumns: `repeat(${layout.value.cols}, minmax(0, 1fr))`, gridTemplateRows: `repeat(${layout.value.rows}, minmax(0, 1fr))` },
)
</script>

<template>
  <div ref="grid" class="min-h-0 flex-1 p-3" :class="narrow ? 'overflow-y-auto' : 'overflow-hidden'">
    <div class="grid h-full w-full gap-3" :class="narrow && 'h-auto'" :style="gridStyle" data-run-grid>
      <template v-for="t in props.tiles" :key="t.session?.id ?? `m-${t.name}`">
        <SessionTile
          v-if="t.session"
          :session="t.session"
          :status="looked(t)?.view"
          :create-transport="props.createTransport(t.session)"
          :receiving="!!props.receiving?.includes(t.name)"
          :data-member="t.name"
          @select="emit('open', t.session.id)"
        >
          <template v-if="tileAsk(t)" #ask>
            <RunAskChoices :line="tileAsk(t)!" :answer="props.answer" />
          </template>
          <template v-if="$slots.leading" #leading>
            <slot name="leading" :tile="t" />
          </template>
          <template #footer>
            <span class="truncate font-mono" data-mono="commit" data-branch>{{ footer(t).where }}</span>
            <span class="ml-auto flex-none font-mono" data-mono="measure" data-diff>{{ footer(t).diff }}</span>
          </template>
        </SessionTile>
        <div v-else class="flex min-h-0 flex-col overflow-hidden rounded-lg border border-dashed border-accented" :data-member="t.name">
          <div class="flex items-center gap-2 border-b border-default px-2.5 py-1.5 text-xs">
            <SessionAvatar :agent-id="t.member?.agentId ?? ''" dashed />
            <span class="flex-1 truncate text-[13px] font-semibold">{{ t.name }}</span>
          </div>
          <div class="flex flex-1 flex-col items-center justify-center gap-2 p-3 text-center text-sm text-muted">
            <span v-if="looked(t)" class="inline-flex flex-wrap items-center justify-center gap-x-2" data-member-words :data-phase="looked(t)!.phase">
              <StatusMark :status="looked(t)!.view" word />
              <span v-if="looked(t)!.after && looked(t)!.phase === 'waiting'">{{ looked(t)!.after }}</span>
            </span>
            <UButton
              v-if="memberOffer(t.member?.status, props.run) === 'start'"
              label="Start now"
              icon="i-lucide-play"
              size="xs"
              color="neutral"
              variant="outline"
              :loading="props.starting === t.name"
              @click="emit('start', t.name)"
            />
            <ResumeButton v-else-if="memberOffer(t.member?.status, props.run) === 'resume'" :run-id="props.runId" :member="t.name" stay size="xs" />
          </div>
        </div>
      </template>
      <CrewFeed :items="props.feed" :style="narrow ? undefined : { gridColumn: `span ${feedSpan} / span ${feedSpan}` }" />
    </div>
  </div>
</template>
