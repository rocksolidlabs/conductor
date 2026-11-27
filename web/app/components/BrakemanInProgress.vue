<script setup lang="ts">
import type { BrakemanRequest } from '~/utils/brakeman'
import { stateWords, stopAction } from '~/utils/brakeman'
import { askedWords, kindLine, subjectWords } from '~/utils/brakemanRows'
import { relativeTime } from '~/utils/sessions'
// A link to a session opens its place: a crew's member inside its crew (UX3 R72).
const placeOf = useSessionPlace()

/**
 * In progress, on the Brakeman page (the Brakeman design review): the turn under way, what it asks and of what, who asked it and when,
 * its progress (the reads used of its budget, the time run of its deadline, what it read last), Watch (its session's terminal, once the
 * launch has named it) and Stop (its run's stop once its brief is kept and the desk still holds it); then the requests queued, oldest
 * first, each with Cancel. Stop and Cancel are the person's: nothing here acts on its own, and nothing takes the focus.
 */
const props = defineProps<{ current: BrakemanRequest | null; queue: BrakemanRequest[] }>()

const bk = useBrakeman()
const live = useAttention()
const toast = useToast()
const now = useNow()

const names = {
  session: (id: string) => live.sessions.value.find((s) => s.id === id)?.name,
  run: (id: string) => live.runNames.value[id],
}
/** "Explain this · task build of fix-login". */
function what(r: BrakemanRequest): string {
  const subjects = subjectWords(r.subjects, names)
  return subjects ? `${kindLine(r)} · ${subjects}` : kindLine(r)
}
/** "Asked by Nate 3m ago", "Woken by events 3m ago". */
const asked = (r: BrakemanRequest) => `${askedWords(r)} ${relativeTime(r.createdAt, now.value, { suffix: true })}`

const busy = ref('')
async function cancel(r: BrakemanRequest) {
  busy.value = r.id
  try {
    await bk.cancel(r.id)
  } catch (e) {
    toast.add({ title: `${r.id} not cancelled`, description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    busy.value = ''
  }
}
/** Stop on the request in progress: an open one is cancelled, a closed one the desk still holds has its run stopped (stopAction). */
async function stop(r: BrakemanRequest) {
  busy.value = r.id
  try {
    await bk.stop(r)
  } catch (e) {
    toast.add({ title: `${r.id} not stopped`, description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    busy.value = ''
  }
}
const stopOf = computed(() => (props.current ? stopAction(props.current) : null))
</script>

<template>
  <UCard :ui="{ header: 'px-4 py-3', body: 'p-4' }" data-brakeman-in-progress>
    <template #header><SettingsCardTitle title="In progress" /></template>
    <div class="flex flex-col gap-4 text-sm">
      <div v-if="current" class="flex min-w-0 flex-col gap-3" data-brakeman-current>
        <div class="flex min-w-0 flex-col gap-0.5" :data-request="current.id" :data-request-state="current.state">
          <span class="min-w-0 break-words font-medium text-highlighted" data-request-title>{{ what(current) }}</span>
          <span class="text-xs text-muted" data-request-words>{{ asked(current) }}</span>
          <BrakemanProgressLine class="mt-2" :request="current" :progress="bk.progress.value[current.id]" />
          <div class="mt-2 flex flex-wrap items-center gap-2">
            <UButton
              label="Watch"
              icon="i-lucide-monitor"
              size="sm"
              color="neutral"
              variant="outline"
              class="max-sm:min-h-11"
              :to="current.sessionId ? placeOf(current.sessionId) : undefined"
              :disabled="!current.sessionId"
              data-request-session
            />
            <UButton
              v-if="stopOf"
              :label="stopOf === 'stop_run' ? 'Stop its run' : 'Stop'"
              icon="i-lucide-square"
              size="sm"
              color="neutral"
              variant="outline"
              class="max-sm:min-h-11"
              :loading="busy === current.id"
              :data-request-stop="stopOf"
              @click="stop(current)"
            />
          </div>
        </div>
      </div>
      <USeparator v-if="current && queue.length" />
      <div v-if="queue.length" class="flex min-w-0 flex-col gap-2" data-brakeman-queue>
        <UTooltip text="Taken one at a time, oldest first.">
          <span class="self-start text-xs font-medium text-muted" tabindex="0" data-queue-count>{{ queue.length }} queued</span>
        </UTooltip>
        <ul class="flex flex-col gap-2">
          <li v-for="r in queue" :key="r.id" class="flex min-w-0 items-center gap-3" :data-request="r.id" :data-request-state="r.state">
            <span class="min-w-0 flex-1 truncate" :title="what(r)" data-request-title>{{ what(r) }}</span>
            <span class="text-xs text-muted" data-request-words>{{ stateWords(r) }}</span>
            <UButton label="Cancel" size="sm" color="neutral" variant="outline" class="max-sm:min-h-11" :loading="busy === r.id" data-request-cancel @click="cancel(r)" />
          </li>
        </ul>
      </div>
    </div>
  </UCard>
</template>
