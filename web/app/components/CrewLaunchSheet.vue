<script setup lang="ts">
import type { AgentInfo, CrewInfo } from '~/composables/useSessions'
import type { DraftCrew } from '~/utils/crews'
import { launchLines, readSheet, type SheetRead, type SheetTarget } from '~/utils/crewLaunch'

/**
 * What a crew's launch asks first (docs/plans/capacity.md, PR-7): each member with its agent, its model and its account, and a compact
 * line of what the server knows of that agent's usage on that account, then Launch. It keeps what it was opened for (`target`), and its
 * Launch hands the page exactly that: a saved crew is read from the server as the sheet opens and that copy is drawn, Launch held until
 * it is here (a failed read says why and offers to read again); the crew page's draft is drawn as it is and saved as it launches. A
 * change someone else saves between the sheet's read and the launch is not shown: the server launches the crew as it is stored at the
 * launch. A crew that inherits its yolo setting is drawn with the server's default once that is read (readSheet), never before. Each read
 * ends at its deadline in words with Read again, and closing the sheet or opening it for another target ends the reads under way. A crew
 * gone when it is read closes the sheet (`gone`). Opened, the compact panels ask the server to read each line's agent and account (at
 * most once in five minutes per agent and account, as the server holds it). What the lines say never stops the launch: a window near its
 * limit is said, and Launch stays the person's. Its buttons are disabled while it closes.
 */
const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ target: SheetTarget | null; agents: AgentInfo[]; launching?: boolean }>()
const emit = defineEmits<{ launch: [crew: CrewInfo | null]; gone: [] }>()

const api = useSessions()
/** What the sheet read for the target it was read for; `got` null while it reads. */
const read = ref<{ target: SheetTarget; got: SheetRead<CrewInfo | DraftCrew> | null } | null>(null)
let reading: AbortController | null = null
async function readNow() {
  reading?.abort()
  const t = props.target
  if (!t || !open.value) return
  const ctl = (reading = new AbortController())
  read.value = { target: t, got: null }
  const got = await readSheet<CrewInfo | DraftCrew>('id' in t ? { id: t.id } : { draft: t.draft }, { crew: (id, s) => api.getCrew(id, s), yoloDefault: (s) => api.catalogInfo(s).then((c) => c.yoloDefault) }, ctl.signal)
  if (!got || reading !== ctl) return
  read.value = { target: t, got }
  if ('gone' in got && got.gone) emit('gone')
}
watch(
  [open, () => props.target],
  ([o, t]) => {
    if (!o || !t) reading?.abort()
    else if (read.value?.target !== t || !read.value.got) void readNow()
  },
  { immediate: true },
)
onBeforeUnmount(() => reading?.abort())
const got = computed(() => (read.value && read.value.target === props.target ? read.value.got : null))
const failed = computed(() => (got.value && 'error' in got.value ? got.value : null))
const crew = computed(() => {
  const g = got.value
  return g && 'crew' in g ? { name: g.crew.name, members: g.crew.members, managed: !!g.crew.tasks, yolo: g.yolo, saved: props.target && 'id' in props.target ? (g.crew as CrewInfo) : null } : null
})

const accounts = useAccounts()
const cap = useCapacity()
watch(open, (o) => o && void accounts.store.load(), { immediate: true })
const lines = computed(() => (crew.value ? launchLines(crew.value.members, { agents: props.agents, doc: accounts.doc.value, managed: crew.value.managed, yolo: crew.value.yolo, view: (q) => cap.entry(q).view }) : []))
</script>

<template>
  <UModal v-model:open="open" :title="crew ? `Launch ${crew.name}` : 'Launch'" :description="target?.label ? `As the run ${target.label}` : 'Each member with its agent, model and account'" :ui="{ content: 'max-w-2xl' }">
    <template #body>
      <p v-if="failed" class="flex flex-wrap items-center gap-2 text-sm text-error [overflow-wrap:anywhere]" data-crew-launch-read-error>
        <span class="min-w-0">{{ failed.what === 'crew' ? 'The crew could not be read' : "The server's yolo default could not be read" }}, so nothing is launched: {{ failed.error }}</span>
        <UButton label="Read again" icon="i-lucide-refresh-cw" size="xs" color="neutral" variant="outline" :disabled="!open" data-crew-launch-read-again @click="readNow" />
      </p>
      <p v-else-if="!crew" class="flex items-center gap-2 text-sm text-muted" data-crew-launch-reading><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" />Reading the crew as the server holds it, and the server's yolo default when it inherits it…</p>
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
        <UButton label="Cancel" color="neutral" variant="ghost" :disabled="!open" data-crew-launch-cancel @click="open = false" />
        <UButton label="Launch" icon="i-lucide-play" :loading="launching" :disabled="!open || !crew || launching" data-crew-launch-confirm @click="emit('launch', crew?.saved ?? null)" />
      </div>
    </template>
  </UModal>
</template>
