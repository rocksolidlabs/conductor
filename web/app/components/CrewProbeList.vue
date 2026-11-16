<script setup lang="ts">
import { watch } from 'vue'
import { TASK_LIMITS, secondsText, typedSeconds, type DraftProbe } from '~/utils/crews'

/**
 * A project manifest's ready probes (EN1): each an id, a URL on this machine fetched until it answers under 300 or a command run until it
 * exits 0 (one or the other: its kind picks which, and switching clears the other), how often it is tried and for how long at most. The
 * owner hands in each row's problem (manifestMarks), since a probe's id is unique across the whole manifest.
 */
const probes = defineModel<DraftProbe[]>({ default: () => [] })
const props = defineProps<{
  /** Each row's first problem, by index. */
  problems?: readonly string[]
  /** Ids taken outside this list: a new row's id is none of them. */
  taken?: readonly string[]
}>()

const what = 'ready probe'
const kinds = [
  { label: 'URL', value: 'url' },
  { label: 'command', value: 'command' },
]

// As in CrewCheckList: ArgvInput commits its chips and clears its text in one tick, and the later update must not undo the earlier.
let latest: DraftProbe[] | null = null
watch(probes, () => {
  latest = null
})
function send(next: DraftProbe[]) {
  latest = next
  probes.value = next
}

function update(i: number, patch: Partial<DraftProbe>) {
  send((latest ?? probes.value).map((p, j) => (j === i ? { ...p, ...patch } : p)))
}

function remove(i: number) {
  send((latest ?? probes.value).filter((_, j) => j !== i))
}

function add() {
  const ids = [...probes.value.map((p) => p.id), ...(props.taken ?? [])]
  let id = 'ready'
  for (let n = 2; ids.includes(id); n++) id = `ready-${n}`
  probes.value = [...probes.value, { id, by: 'url' }]
}

/** A probe's kind: as the row was given it, else by what it holds. */
function kindOf(p: DraftProbe): 'command' | 'url' {
  return p.by ?? (p.argv?.length ? 'command' : 'url')
}

function setKind(i: number, by: 'command' | 'url') {
  update(i, by === 'url' ? { by, argv: undefined, pending: undefined } : { by, url: undefined })
}

</script>

<template>
  <div class="flex flex-col gap-2" data-crew-probes>
    <div v-for="(p, i) in probes" :key="i" class="flex flex-col gap-1" data-crew-probe :data-probe-id="p.id">
      <div class="flex flex-wrap items-start gap-2">
        <UInput :model-value="p.id" placeholder="id" maxlength="40" autocapitalize="off" spellcheck="false" :aria-label="`Id of ${what} ${i + 1}`" :ui="{ base: 'font-mono' }" class="w-28" @update:model-value="update(i, { id: String($event) })" />
        <USelect :model-value="kindOf(p)" :items="kinds" :aria-label="`Kind of ${what} ${i + 1}`" class="w-28" data-probe-kind @update:model-value="setKind(i, $event as 'command' | 'url')" />
        <div class="min-w-48 flex-1">
          <ArgvInput
            v-if="kindOf(p) === 'command'"
            :model-value="p.argv ?? []"
            :pending="p.pending ?? ''"
            placeholder="program and arguments; it passes when it exits 0"
            :invalid="!p.argv?.length || !!p.pending"
            :aria-label="`Command of ${what} ${i + 1}`"
            @update:model-value="update(i, { argv: $event.length ? $event : undefined })"
            @update:pending="update(i, { pending: $event || undefined })"
          />
          <UInput
            v-else
            :model-value="p.url ?? ''"
            placeholder="http://127.0.0.1:${CONDUCTOR_PORT_WEB}/health"
            autocapitalize="off"
            spellcheck="false"
            :aria-label="`URL of ${what} ${i + 1}`"
            :ui="{ base: 'font-mono' }"
            class="w-full"
            @update:model-value="update(i, { url: String($event) || undefined })"
          />
        </div>
        <UFieldGroup class="w-36">
          <UBadge label="every" color="neutral" variant="outline" size="lg" class="flex-none" />
          <UInput :model-value="secondsText(p.intervalSeconds)" type="number" inputmode="numeric" min="1" :max="TASK_LIMITS.probeInterval" :placeholder="String(TASK_LIMITS.probeIntervalDefault)" :aria-label="`Interval in seconds of ${what} ${i + 1}`" class="min-w-0 flex-1" @update:model-value="update(i, { intervalSeconds: typedSeconds(String($event)) })">
            <template #trailing><span class="text-xs text-muted">s</span></template>
          </UInput>
        </UFieldGroup>
        <UFieldGroup class="w-36">
          <UBadge label="up to" color="neutral" variant="outline" size="lg" class="flex-none" />
          <UInput :model-value="secondsText(p.timeoutSeconds)" type="number" inputmode="numeric" min="1" :max="TASK_LIMITS.probeTimeout" :placeholder="String(TASK_LIMITS.probeTimeoutDefault)" :aria-label="`Timeout in seconds of ${what} ${i + 1}`" class="min-w-0 flex-1" @update:model-value="update(i, { timeoutSeconds: typedSeconds(String($event)) })">
            <template #trailing><span class="text-xs text-muted">s</span></template>
          </UInput>
        </UFieldGroup>
        <IconButton icon="i-lucide-x" color="neutral" variant="ghost" size="xs" :label="`Remove ${what} ${i + 1}`" @click="remove(i)" />
      </div>
      <span v-if="problems?.[i]" class="text-xs leading-snug text-error" data-probe-error>{{ problems[i] }}</span>
    </div>
    <UButton :label="`Add ${what}`" icon="i-lucide-plus" color="secondary" variant="link" size="sm" class="self-start px-0" :disabled="probes.length >= TASK_LIMITS.manifestSteps" data-add-probe @click="add" />
  </div>
</template>
