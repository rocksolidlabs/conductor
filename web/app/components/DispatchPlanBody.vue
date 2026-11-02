<script setup lang="ts">
import type { DispatchTicket, PlanBody } from '~/composables/useDispatch'
import type { AgentInfo } from '~/composables/useSessions'
import { argvLine, expectedChecks, resolvedArgv, shortCommit, yoloWords } from '~/utils/dispatchPlans'

/**
 * A plan revision's body as the ticket page shows it (Program item DS2, docs/plans/dispatch-evidence.md, "web/app"): exactly what its
 * approval covers and what a launch runs. The yolo choice said plainly first; the commit it starts from; the crew with each member's agent
 * and arguments and the command each agent runs as the catalog resolved it when the revision was made; the tasks with who does each, its
 * kind, what it needs and its checks; the checks expected; the source references at the base; what the package hands over (the
 * attachments, the comments); the constraints; the decisions a person makes, recorded and not held yet; the note. One column.
 */
const props = defineProps<{
  body: PlanBody
  ticket: DispatchTicket
  agents: readonly AgentInfo[]
}>()

const yolo = computed(() => yoloWords(props.body.crew.yolo))
const checks = computed(() => expectedChecks(props.body))
const agentOf = (id: string) => props.body.agents.find((a) => a.id === id)
const agentName = (id: string) => props.agents.find((a) => a.id === id)?.name ?? id
const tasksOf = (member: string) => (props.body.crew.tasks ?? []).filter((t) => t.member === member).map((t) => t.id)
const attachmentName = (id: string) => props.ticket.attachments?.find((a) => a.id === id)?.name ?? 'a file since removed'
function commentWords(id: string): string {
  const c = props.ticket.comments?.find((x) => x.id === id)
  if (!c) return 'a comment not among the latest read'
  return `#${c.n} by ${c.author || c.by || 'someone'}${c.deletedAt ? ' (deleted since)' : ''}`
}
const manifestWords = computed(() => {
  const m = props.body.crew.manifest
  if (!m) return ''
  if (m.file) return props.body.manifestFile ? `conductor.manifest.json at the base (blob ${shortCommit(props.body.manifestFile.blob)})` : 'conductor.manifest.json, read at each tree'
  return 'the crew’s own project manifest'
})
</script>

<template>
  <div class="flex min-w-0 flex-col gap-4 text-sm" data-plan-body>
    <!-- The yolo choice, said plainly: what an approval most needs to show. -->
    <div class="flex items-start gap-2 rounded-md px-3 py-2" :class="yolo.tone === 'warning' ? 'bg-warning/10 text-warning' : 'bg-elevated text-muted'" :data-plan-yolo="body.crew.yolo ? 'on' : 'off'">
      <UIcon :name="body.crew.yolo ? 'i-lucide-shield-off' : 'i-lucide-shield-check'" class="mt-0.5 size-4 flex-none" />
      <span><span class="font-medium">{{ yolo.label }}.</span> {{ yolo.words }}</span>
    </div>

    <dl class="grid grid-cols-1 gap-x-4 gap-y-1.5 sm:grid-cols-[8rem_1fr]">
      <dt class="text-xs text-muted">From commit</dt>
      <dd class="min-w-0"><code class="font-mono text-xs" :title="body.base" :data-plan-base="body.base">{{ shortCommit(body.base) }}</code></dd>
      <dt class="text-xs text-muted">Crew</dt>
      <dd class="min-w-0 break-words" data-plan-crew>
        {{ body.crew.name }} <span class="font-mono text-xs text-dimmed">{{ body.crew.id }}</span>
        <span v-if="body.crew.cwd" class="block truncate font-mono text-xs text-dimmed" :title="body.crew.cwd">{{ body.crew.cwd }}</span>
      </dd>
      <template v-if="manifestWords">
        <dt class="text-xs text-muted">Manifest</dt>
        <dd class="min-w-0 text-muted">{{ manifestWords }}</dd>
      </template>
    </dl>

    <div class="flex flex-col gap-2">
      <h4 class="text-xs font-semibold text-muted">Members</h4>
      <ul class="flex flex-col gap-2">
        <li v-for="m in body.crew.members ?? []" :key="m.name" class="flex min-w-0 flex-col gap-0.5 rounded-md p-2 ring ring-default" :data-plan-member="m.name" :data-plan-member-agent="m.agentId">
          <span class="flex min-w-0 flex-wrap items-baseline gap-x-2">
            <span class="font-medium text-highlighted">{{ m.name }}</span>
            <span class="text-muted">{{ agentName(m.agentId) }}</span>
            <span v-if="tasksOf(m.name).length" class="text-xs text-dimmed">does {{ tasksOf(m.name).join(', ') }}</span>
          </span>
          <span v-if="m.args?.length" class="text-xs text-muted">Arguments: <code class="break-all font-mono">{{ argvLine(m.args) }}</code></span>
          <span class="text-xs text-muted" data-plan-member-command>
            Runs as <code class="break-all font-mono text-default">{{ argvLine(resolvedArgv(agentOf(m.agentId), m.args ?? [])) }}</code>
          </span>
        </li>
      </ul>
    </div>

    <div class="flex flex-col gap-2">
      <h4 class="text-xs font-semibold text-muted">Tasks</h4>
      <ul class="flex flex-col gap-2">
        <li v-for="t in body.crew.tasks ?? []" :key="t.id" class="flex min-w-0 flex-col gap-0.5 rounded-md p-2 ring ring-default" :data-plan-task="t.id" :data-plan-task-member="t.member" :data-plan-task-kind="t.kind">
          <span class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
            <span class="font-mono text-highlighted">{{ t.id }}</span>
            <UBadge :label="t.kind" :color="t.kind === 'review' ? 'info' : 'neutral'" variant="subtle" size="sm" />
            <span class="text-muted">by <span class="text-default" data-plan-task-assignee>{{ t.member }}</span></span>
            <span v-if="t.needs?.length" class="text-xs text-dimmed">after {{ t.needs.join(', ') }}</span>
          </span>
          <ul v-if="t.checks?.length" class="flex flex-col gap-0.5 text-xs text-muted">
            <li v-for="c in t.checks" :key="c.id" :data-plan-check="`${t.id}/${c.id}`">
              check <span class="font-mono">{{ c.id }}</span>: <code class="break-all font-mono">{{ argvLine(c.argv) }}</code>
            </li>
          </ul>
        </li>
      </ul>
    </div>

    <p class="text-xs text-muted" data-plan-expected :data-count="checks.length">
      <template v-if="checks.length">
        {{ checks.length }} {{ checks.length === 1 ? 'check is' : 'checks are' }} expected<template v-if="checks.some((c) => c.phase === 'verify')">, the manifest's verify steps ({{ checks.filter((c) => c.phase === 'verify').map((c) => c.id).join(', ') }}) among them, run on each result</template>.
      </template>
      <template v-else>No check is expected: a work task's result is accepted as submitted.</template>
    </p>

    <div v-if="body.references.length" class="flex flex-col gap-1">
      <h4 class="text-xs font-semibold text-muted">References at the base</h4>
      <ul class="flex flex-col gap-0.5 text-xs">
        <li v-for="(r, i) in body.references" :key="i" class="min-w-0 break-words" :data-plan-reference="r.path">
          <code class="font-mono">{{ r.path }}<template v-if="r.start">:{{ r.start }}<template v-if="r.end">-{{ r.end }}</template></template></code>
          <span class="text-dimmed"> · {{ r.kind }} {{ shortCommit(r.object) }}</span>
          <span v-if="r.note" class="text-muted"> · {{ r.note }}</span>
        </li>
      </ul>
    </div>

    <div class="flex flex-col gap-1" data-plan-handover>
      <h4 class="text-xs font-semibold text-muted">Handed to every attempt</h4>
      <p class="text-xs text-muted">The ticket as it stands at the launch, in <code>.conductor/ticket.md</code> and <code>.conductor/package.json</code><template v-if="body.attachments.length || body.comments.length">, with:</template><template v-else>.</template></p>
      <ul v-if="body.attachments.length || body.comments.length" class="flex flex-col gap-0.5 text-xs">
        <li v-for="id in body.attachments" :key="id" data-plan-attachment><UIcon name="i-lucide-paperclip" class="mr-1 size-3 align-[-2px]" />{{ attachmentName(id) }}</li>
        <li v-for="c in body.comments" :key="c.id" data-plan-comment><UIcon name="i-lucide-message-square" class="mr-1 size-3 align-[-2px]" />{{ commentWords(c.id) }}</li>
      </ul>
    </div>

    <div v-if="body.constraints" class="flex flex-col gap-1">
      <h4 class="text-xs font-semibold text-muted">Constraints</h4>
      <p class="whitespace-pre-wrap break-words" data-plan-constraints>{{ body.constraints }}</p>
    </div>

    <div v-if="body.decisions.length" class="flex flex-col gap-1" data-plan-decisions>
      <h4 class="text-xs font-semibold text-muted">Decisions a person makes</h4>
      <ul class="flex flex-col gap-1">
        <li v-for="d in body.decisions" :key="d.id" class="min-w-0 break-words" :data-plan-decision="d.id">
          <UBadge :label="d.kind" color="neutral" variant="subtle" size="sm" class="mr-1" />{{ d.question }}
          <span v-if="d.before.length" class="text-xs text-dimmed"> · before {{ d.before.join(', ') }}</span>
        </li>
      </ul>
      <p class="text-xs text-dimmed">Recorded and shown, not held: the run does not wait on them yet. Durable decisions, still to come, will hold the tasks before them.</p>
    </div>

    <div v-if="body.note" class="flex flex-col gap-1">
      <h4 class="text-xs font-semibold text-muted">Why this revision</h4>
      <p class="whitespace-pre-wrap break-words" data-plan-note>{{ body.note }}</p>
    </div>
  </div>
</template>
