<script setup lang="ts">
import type { AgentInfo } from '~/composables/useSessions'
import { launchLines, type LaunchMember } from '~/utils/crewLaunch'

/**
 * What a crew's launch asks first (docs/plans/capacity.md, PR-7): each member with its agent, its model and its account, and a compact
 * line of what the server knows of that agent's usage on that account, then Launch. Opened, it asks the server to read each line's
 * agent and account (the compact panels' read on open, at most once in five minutes per agent and account, as the server holds it).
 * What the lines say never stops the launch: a window near its limit is said, and Launch stays the person's.
 */
const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{
  crew: { name: string; members: readonly LaunchMember[]; managed: boolean; yolo: boolean } | null
  agents: AgentInfo[]
  /** The run's name, when one was given with the launch. */
  label?: string
  launching?: boolean
}>()
const emit = defineEmits<{ launch: [] }>()

const accounts = useAccounts()
watch(open, (o) => o && void accounts.store.load(), { immediate: true })
const lines = computed(() => (props.crew ? launchLines(props.crew.members, { agents: props.agents, doc: accounts.doc.value, managed: props.crew.managed, yolo: props.crew.yolo }) : []))
</script>

<template>
  <UModal v-model:open="open" :title="crew ? `Launch ${crew.name}` : 'Launch'" :description="label ? `As the run ${label}` : 'Each member with its agent, model and account'" :ui="{ content: 'max-w-2xl' }">
    <template #body>
      <ul class="flex flex-col gap-3" data-crew-launch-sheet>
        <li v-for="l in lines" :key="l.name" class="flex flex-col gap-1.5" :data-crew-launch-line="l.name">
          <div class="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-sm">
            <span class="font-mono font-medium text-highlighted">{{ l.name }}</span>
            <span class="text-muted" data-crew-launch-agent>{{ l.agent }}</span>
            <span class="text-xs text-muted">Model: <span class="text-default" data-crew-launch-model>{{ l.model }}</span></span>
            <span class="text-xs text-muted">Account: <span class="text-default" data-crew-launch-account>{{ l.account }}</span></span>
          </div>
          <CapacityPanel :query="open ? l.query : null" compact read-on-open />
        </li>
      </ul>
    </template>
    <template #footer>
      <div class="flex w-full items-center justify-end gap-2">
        <UButton label="Cancel" color="neutral" variant="ghost" @click="open = false" />
        <UButton label="Launch" icon="i-lucide-play" :loading="launching" :disabled="!crew || launching" data-crew-launch-confirm @click="emit('launch')" />
      </div>
    </template>
  </UModal>
</template>
