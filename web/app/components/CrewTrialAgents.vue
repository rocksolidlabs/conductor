<script setup lang="ts">
import type { DropdownMenuItem } from '@nuxt/ui'
import { COMPARE_LIMITS, chipArgs, nextAgent, rowKey, withAgent, type DraftAgent, type TrialAgent } from '~/utils/compareTemplate'

/**
 * The agents of New agent trial, on the form's face: one chip an agent, each a menu of the installed agents and Remove (held at two),
 * showing the agent's name and, once arguments are typed for it, ` · <arguments>` cut in the middle with the whole line in its title;
 * Add agent up to four. With no agent installed it says so, with the way to the Agents page.
 * Marks: data-trial-agents (the row), data-trial-agent="<i>" (a chip), data-trial-add-agent.
 */
const model = defineModel<DraftAgent[]>({ required: true })
const props = defineProps<{
  /** The catalog. */
  agents: readonly TrialAgent[]
  /** Held while a press is out. */
  disabled?: boolean
}>()
const emit = defineEmits<{ agents: [] }>()

const installed = computed(() => props.agents.filter((a) => a.id !== 'shell' && a.available !== false))
const nameOf = (id: string) => props.agents.find((a) => a.id === id)?.name ?? (id || 'Pick an agent')

function menu(i: number): DropdownMenuItem[][] {
  const current = model.value[i]?.agentId
  return [
    installed.value.map((a) => ({
      label: a.name,
      type: 'checkbox' as const,
      checked: a.id === current,
      onSelect: () => (model.value = model.value.map((x, j) => (j === i ? withAgent(x, a.id, props.agents) : x))),
    })),
    [
      {
        label: 'Remove',
        icon: 'i-lucide-x',
        disabled: model.value.length <= COMPARE_LIMITS.minCandidates,
        onSelect: () => (model.value = model.value.filter((_, j) => j !== i)),
      },
    ],
  ]
}

function add() {
  model.value = [...model.value, { key: rowKey(), agentId: nextAgent({ agents: model.value }, props.agents), args: '', approach: '' }]
}
</script>

<template>
  <div class="flex min-w-0 flex-wrap items-center gap-2" data-trial-agents>
    <template v-if="installed.length || model.length">
      <UDropdownMenu v-for="(a, i) in model" :key="a.key" :items="menu(i)" :disabled="disabled" :content="{ align: 'start' }">
        <UButton
          color="neutral"
          variant="outline"
          trailing-icon="i-lucide-chevron-down"
          class="min-w-0 max-w-full"
          :title="a.args.trim() ? `${nameOf(a.agentId)} ${a.args.trim()}` : undefined"
          :data-trial-agent="i"
          :data-agent-id="a.agentId"
        >
          <span class="min-w-0">{{ nameOf(a.agentId) }}<template v-if="a.args.trim()"><span class="text-muted"> · </span><code class="text-xs text-muted" data-mono="command" data-trial-agent-args>{{ chipArgs(a.args) }}</code></template></span>
        </UButton>
      </UDropdownMenu>
      <UButton
        v-if="model.length < COMPARE_LIMITS.candidates && installed.length"
        label="Add agent"
        icon="i-lucide-plus"
        color="secondary"
        variant="link"
        :disabled="disabled"
        data-trial-add-agent
        @click="add"
      />
    </template>
    <p v-else class="text-sm text-muted" data-trial-no-agents>No agent is installed on this server. <NuxtLink to="/agents" class="underline" @click="emit('agents')">See Agents.</NuxtLink></p>
  </div>
</template>
