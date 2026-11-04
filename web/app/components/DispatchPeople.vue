<script setup lang="ts">
import type { LaunchSummary, PlanBody } from '~/composables/useDispatch'
import type { AgentInfo } from '~/composables/useSessions'
import { agentVersionWords, type PersonRow } from '~/utils/dispatch'
import { argvLine, resolvedArgv, revisionTag } from '~/utils/dispatchPlans'
import { shortRunId } from '~/utils/crewWords'

/**
 * A ticket's People (Program item DS2, docs/plans/dispatch-evidence.md, "web/app", PR-12): who it is to whom, each name once with every
 * role it holds (the owner, the assignees, who approved its plan revisions, who launched its runs, who commented), then the agents: the
 * approved revision's members, each with its agent and the command it runs as the revision resolved it (the agent and model selection the
 * approval covers), and each launch's members with the agent each started and the version its identity probe reported then. One column on
 * a phone.
 */
const props = defineProps<{
  people: readonly PersonRow[]
  /** The approved revision's body and its number, when read. */
  plan?: { n: number; body: PlanBody } | null
  /** The ticket's launches known to the page, newest first. */
  launches: readonly LaunchSummary[]
  agents: readonly AgentInfo[]
}>()

const ROLE_WORDS: Record<PersonRow['roles'][number], string> = { owner: 'owner', assignee: 'assignee', approver: 'approved a plan', launcher: 'launched a run', commenter: 'commented' }
const agentName = (id: string) => props.agents.find((a) => a.id === id)?.name ?? id
const agentOf = (id: string) => props.plan?.body.agents.find((a) => a.id === id)
const started = computed(() => props.launches.filter((l) => l.state === 'started' && l.agents?.length))
</script>

<template>
  <section class="flex flex-col gap-3" data-ticket-people>
    <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">People</h3>
    <ul class="flex flex-wrap gap-1.5" data-people>
      <li v-for="p in people" :key="`${p.kind}:${p.name}`" class="flex min-w-0 items-center gap-1.5 rounded-md px-2 py-1 text-sm ring ring-default" :data-person="p.name" :data-person-kind="p.kind" :data-person-roles="p.roles.join(' ')">
        <UIcon :name="p.kind === 'agent' ? 'i-lucide-bot' : 'i-lucide-user'" class="size-3.5 flex-none text-muted" />
        <span class="min-w-0 truncate font-medium">{{ p.name }}</span>
        <span class="text-xs text-dimmed">{{ p.roles.map((r) => ROLE_WORDS[r]).join(', ') }}</span>
      </li>
      <li v-if="!people.length" class="text-sm text-dimmed">Nobody yet.</li>
    </ul>

    <div v-if="plan" class="flex flex-col gap-1" data-people-plan-agents>
      <span class="text-xs font-medium text-muted">The agents of {{ revisionTag(plan.n) }}, as it was approved</span>
      <ul class="flex flex-col gap-1">
        <li v-for="m in plan.body.crew.members ?? []" :key="m.name" class="flex min-w-0 flex-col gap-0.5 text-sm" :data-people-plan-agent="m.name" :data-agent="m.agentId">
          <span><span class="font-medium">{{ m.name }}</span> <span class="text-muted">{{ agentName(m.agentId) }}</span></span>
          <code class="break-all font-mono text-xs text-default" data-people-plan-argv>{{ argvLine(resolvedArgv(agentOf(m.agentId), m.args ?? [])) }}</code>
        </li>
      </ul>
    </div>

    <div v-if="started.length" class="flex flex-col gap-1" data-people-launch-agents>
      <span class="text-xs font-medium text-muted">The agents each launch started, with the version each reported</span>
      <ul class="flex flex-col gap-1.5">
        <li v-for="l in started" :key="l.id" class="flex min-w-0 flex-col gap-0.5 text-sm" :data-people-launch="l.id">
          <span class="text-xs text-dimmed">{{ l.label || shortRunId(l.runId) }} · {{ revisionTag(l.planN) }}</span>
          <ul class="flex flex-col gap-0.5">
            <li v-for="a in l.agents" :key="a.member" class="flex min-w-0 flex-wrap items-baseline gap-x-2" :data-people-launch-agent="`${l.id}/${a.member}`" :data-agent="a.agent" :data-version="a.version">
              <span class="font-medium">{{ a.member }}</span>
              <span class="text-muted">{{ agentName(a.agent) }}</span>
              <span class="font-mono text-xs" :class="a.version ? 'text-default' : 'text-dimmed'" data-people-version>{{ agentVersionWords(a.version) }}</span>
            </li>
          </ul>
        </li>
      </ul>
    </div>
  </section>
</template>
