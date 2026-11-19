<script setup lang="ts">
import type { RunInfo, SessionInfo } from '~/composables/useSessions'
import { memberAsk, memberLooks, runAside, runLook, runSubtitle, shortRunId, startedClock, tookWords } from '~/utils/crewWords'
import { memberSession } from '~/utils/crews'
import { runLive, runState } from '~/utils/runs'
import { runItems } from '~/utils/status'

/**
 * One live run on the Crews page, read without a legend (C5): named by its crew and its start, its state in the one vocabulary with
 * its counts (runLook: "Needs you · 1 working · 1 idle"), then each member as a line, its name, whom it waits for ("after driver"),
 * the task it is on, and its state's word beside its mark. A member that asks has its question under the lines, with its choices
 * as buttons that answer in place through the run chat's own path (C6, useRunChatAnswer); a question without choices keeps
 * "Answer", which opens the member's session. Then when it started and how long it has gone, Stop and Open run.
 */
const props = defineProps<{ run: RunInfo; sessions: SessionInfo[]; now: number }>()

const api = useSessions()
const live = useAttention()
const toast = useToast()

const state = computed(() => runState(props.run, props.sessions))
const members = computed(() => memberLooks(props.run, props.sessions, props.now))
// The run's state and counts, its "N need you" the run's items of the one list (each thing a person can act on, once).
const needsYou = useNeedsYou()
const look = computed(() => runLook(props.run, props.sessions, runItems(needsYou.items.value, props.run.id), props.now))
const needs = computed(() => look.value.phase === 'needs')
const aside = computed(() => runAside(props.run))
const asks = computed(() =>
  members.value
    .filter((t) => t.status === 'needs_input')
    .map((t) => {
      const s = memberSession(props.run, t.m, props.sessions)
      return { name: t.m.name, sessionId: s?.id ?? t.m.sessionId, ...memberAsk(t.m.name, s) }
    }),
)
const meta = computed(() => `started ${startedClock(props.run.startedAt)} · ${tookWords(props.run, runLive(state.value.state), props.now)}`)

/**
 * A choice pressed on the card: the same path as the run's chat (useRunChatAnswer), to the one running session of the member the
 * question is on, naming the question; nothing for a stopped run. The home is the workbench's alone: a view link never opens it.
 */
const answers = useRunChatAnswer({
  members: () =>
    props.run.members.map((m) => {
      const s = memberSession(props.run, m, props.sessions)
      return { name: m.name, session: s && (s.status === 'running' || s.status === 'starting') ? s : undefined }
    }),
  may: () => !props.run.stoppedAt,
})

const stopping = ref(false)
async function stop() {
  stopping.value = true
  try {
    live.applyRun(await api.stopRun(props.run.id))
    toast.add({ title: 'Run stopped', description: 'Every member was stopped; the worktrees and branches stay.', icon: 'i-lucide-square', color: 'neutral' })
  } catch (e) {
    toast.add({ title: 'Stop failed', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    stopping.value = false
  }
}
</script>

<template>
  <article
    class="flex min-w-0 flex-col gap-2.5 rounded-lg border p-3.5 sm:p-4"
    :class="needs ? 'border-warning ring-1 ring-warning/40' : 'border-default'"
    :data-run="run.id"
    :data-state="state.state"
    :aria-label="`${run.name}, ${runSubtitle(run)}`"
  >
    <div class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
      <NuxtLink :to="`/crews/${encodeURIComponent(run.crewId)}`" class="min-w-0 truncate text-[15px] font-semibold text-highlighted hover:underline">{{ run.name }}</NuxtLink>
      <span class="min-w-0 truncate text-[13px] text-muted" data-run-subtitle>{{ runSubtitle(run) }}</span>
      <span class="font-mono text-[11px] text-dimmed" :title="run.id" data-mono="id" data-run-short-id>{{ shortRunId(run.id) }}</span>
      <YoloBadge v-if="run.yolo" icon class="flex-none" />
    </div>
    <div class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-[13px]" data-run-state :data-phase="look.phase" data-run-status-line>
      <StatusMark :status="look.view" word class="[&_[data-status-word]]:font-medium [&_[data-status-word]]:text-highlighted" />
      <span v-if="look.detail" class="min-w-0 truncate text-muted" :title="look.detail" data-run-detail>{{ look.detail }}</span>
      <span v-if="aside" class="inline-flex min-w-0 items-center gap-1 text-xs text-muted" :title="aside.text" data-run-aside><UIcon :name="aside.icon" class="size-3.5 flex-none" /><span class="truncate">{{ aside.text }}</span></span>
    </div>

    <ul class="flex min-w-0 flex-col" data-run-members>
      <li
        v-for="t in members"
        :key="t.m.name"
        class="flex min-h-7 min-w-0 items-center gap-2 border-t border-default py-1 text-[13px] first:border-t-0"
        :data-run-member="t.m.name"
        :data-status="t.status"
        :data-phase="t.phase"
      >
        <span class="min-w-0 truncate font-medium text-highlighted" :title="t.m.name">{{ t.m.name }}</span>
        <span v-if="t.after" class="flex-none text-xs text-muted" data-run-member-after>{{ t.after }}</span>
        <span v-if="t.m.task" class="min-w-0 truncate text-xs text-muted" :title="`on the task ${t.m.task}`">on {{ t.m.task }}</span>
        <StatusMark :status="t.view" word class="ml-auto" data-run-member-state />
      </li>
    </ul>

    <div v-for="a in asks" :key="a.name" class="flex flex-col gap-2 rounded-md bg-warning/10 px-2.5 py-2 text-[13px]" data-run-ask :data-member="a.name">
      <div class="flex items-start gap-2">
        <UIcon name="i-lucide-hand" class="mt-0.5 size-3.5 flex-none text-warning" />
        <!-- The question whole (#187): wrapped over its lines, never cut (the server bounds it at 500 bytes). -->
        <span class="min-w-0 break-words" data-run-ask-words><b class="font-semibold">{{ a.name }}</b> asks “{{ a.question }}”</span>
        <NuxtLink v-if="a.sessionId && !(a.line && answers.answerable(a.line))" :to="`/sessions/${encodeURIComponent(a.sessionId)}`" class="ml-auto flex-none font-medium text-highlighted hover:underline" data-run-ask-open>Answer</NuxtLink>
      </div>
      <RunAskChoices v-if="a.line && answers.answerable(a.line)" :line="a.line" :answer="answers.answer" class="pl-5.5" />
    </div>

    <div class="flex flex-wrap items-center gap-2 text-[11.5px] text-muted">
      <span class="truncate tabular-nums" data-run-meta>{{ meta }}</span>
      <div class="ml-auto flex flex-none items-center gap-1.5">
        <UButton label="Stop" icon="i-lucide-square" size="xs" color="error" variant="soft" class="max-sm:min-h-11" :loading="stopping" :aria-label="`Stop the run ${run.id}`" data-run-stop @click="stop" />
        <UButton :to="`/runs/${encodeURIComponent(run.id)}`" label="Open run" trailing-icon="i-lucide-arrow-right" size="xs" color="neutral" variant="outline" class="max-sm:min-h-11" data-run-open />
      </div>
    </div>
  </article>
</template>
