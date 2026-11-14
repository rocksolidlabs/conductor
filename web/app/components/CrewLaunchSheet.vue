<script setup lang="ts">
import type { AgentInfo, CrewInfo } from '~/composables/useSessions'
import { launchLines, type SheetTarget } from '~/utils/crewLaunch'

/**
 * What a crew's launch asks first (docs/plans/capacity.md, PR-7): each member with its agent, its model and its account, and a compact
 * line of what the server knows of that agent's usage on that account, then Launch. It keeps what it was opened for (`target`), and its
 * Launch hands the page exactly that: a saved crew is read from the server as the sheet opens and that copy is drawn, Launch held until
 * it is here (a failed read says why and offers to read again); the crew page's draft is drawn as it is and saved as it launches. A
 * change someone else saves between the sheet's read and the launch is not shown: the server launches the crew as it is stored at the
 * launch. Opened, the compact panels ask the server to read each line's agent and account (at most once in five minutes per agent and
 * account, as the server holds it). What the lines say never stops the launch: a window near its limit is said, and Launch stays the
 * person's.
 */
const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ target: SheetTarget | null; agents: AgentInfo[]; yoloDefault: boolean; launching?: boolean }>()
const emit = defineEmits<{ launch: [crew: CrewInfo | null] }>()

const api = useSessions()
/** The saved crew as the server answered the sheet's read, or why the read failed, with the target it was read for. */
const read = ref<{ target: SheetTarget; crew?: CrewInfo; error?: string } | null>(null)
async function readCrew() {
  const t = props.target
  if (!t || !('id' in t)) return
  read.value = { target: t }
  try {
    const crew = await api.getCrew(t.id)
    if (read.value?.target === t) read.value = { target: t, crew }
  } catch (e) {
    if (read.value?.target === t) read.value = { target: t, error: (e as Error).message }
  }
}
watch(
  [open, () => props.target],
  ([o, t]) => {
    if (o && t && read.value?.target !== t) void readCrew()
  },
  { immediate: true },
)
const saved = computed(() => (props.target && 'id' in props.target && read.value?.target === props.target ? read.value : null))
const crew = computed(() => {
  const t = props.target
  const c = t && ('id' in t ? saved.value?.crew : t.draft)
  return c ? { name: c.name, members: c.members, managed: !!c.tasks, yolo: c.yolo ?? props.yoloDefault } : null
})

const accounts = useAccounts()
const cap = useCapacity()
watch(open, (o) => o && void accounts.store.load(), { immediate: true })
const lines = computed(() => (crew.value ? launchLines(crew.value.members, { agents: props.agents, doc: accounts.doc.value, managed: crew.value.managed, yolo: crew.value.yolo, view: (q) => cap.entry(q).view }) : []))
</script>

<template>
  <UModal v-model:open="open" :title="crew ? `Launch ${crew.name}` : 'Launch'" :description="target?.label ? `As the run ${target.label}` : 'Each member with its agent, model and account'" :ui="{ content: 'max-w-2xl' }">
    <template #body>
      <p v-if="saved?.error" class="flex flex-wrap items-center gap-2 text-sm text-error [overflow-wrap:anywhere]" data-crew-launch-read-error>
        <span class="min-w-0">The crew could not be read, so nothing is launched: {{ saved.error }}</span>
        <UButton label="Read again" icon="i-lucide-refresh-cw" size="xs" color="neutral" variant="outline" data-crew-launch-read-again @click="readCrew" />
      </p>
      <p v-else-if="!crew" class="flex items-center gap-2 text-sm text-muted" data-crew-launch-reading><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" />Reading the crew as the server holds it…</p>
      <ul v-else class="flex flex-col gap-3" data-crew-launch-sheet>
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
        <UButton label="Cancel" color="neutral" variant="ghost" data-crew-launch-cancel @click="open = false" />
        <UButton label="Launch" icon="i-lucide-play" :loading="launching" :disabled="!crew || launching" data-crew-launch-confirm @click="emit('launch', saved?.crew ?? null)" />
      </div>
    </template>
  </UModal>
</template>
