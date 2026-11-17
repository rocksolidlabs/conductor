<script setup lang="ts">
import type { BrakemanRole, RoleLogRow } from '~/utils/brakeman'
import { ROLE_LOG_READ, ReadOrder, nextBriefingWords, pauseAction, retryWords, roleSentence, wokenWords } from '~/utils/brakemanRole'

/**
 * Brakeman's background role on the Brakeman page (docs/plans/brakeman.md, BK3, PR-15; the Brakeman design review's "In the
 * background"): one sentence, its state said once, and when the next briefing is; under it how often it was woken today and the day's
 * budget; Pause or Resume (which never stops a turn in progress), and Activity, which opens its wakes and notes. Then a Notes card:
 * how many notes are kept, given to every turn, and Open. The role and its log are read again on each of its `brakeman` events and
 * after a reconnect (the store's snapshot moves roleSeq too); reads that overlap are taken newest only (ReadOrder); nothing here takes
 * the focus or moves the page when the role changes.
 */
const props = defineProps<{ windowSeconds?: number; background: boolean; briefings?: string[] }>()

const bk = useBrakeman()
const toast = useToast()
const now = useNow()

const role = ref<BrakemanRole | null>(null)
const log = ref<RoleLogRow[]>([])
const busy = ref(false)
const problem = ref('')
const sheet = ref(false)
const sheetAt = ref<'wakes' | 'notes'>('wakes')

const reads = new ReadOrder()

async function read() {
  const ticket = reads.begin()
  try {
    const out = await bk.role(ROLE_LOG_READ)
    if (!reads.current(ticket)) return
    role.value = out.role
    log.value = out.log
    problem.value = ''
  } catch (e) {
    if (reads.current(ticket)) problem.value = (e as Error).message
  }
}

onMounted(read)
watch(() => bk.roleSeq.value, () => void read())

const action = computed(() => pauseAction(role.value ?? undefined))
const state = computed(() => bk.state.value)
const sentence = computed(() => {
  if (!role.value) return ''
  const next = nextBriefingWords(role.value, state.value?.nextBriefing, props.briefings, props.background)
  return next ? `${roleSentence(role.value, props.windowSeconds)} ${next}` : roleSentence(role.value, props.windowSeconds)
})
/** The day's budget, spent and allowed. */
const budget = computed(() => {
  const s = state.value
  if (!s) return ''
  return `${s.turnsToday} of ${s.config.budget.turnsPerDay} turns, ${s.minutesToday ?? 0} of ${s.config.budget.minutesPerDay} minutes today`
})
const woken = computed(() => {
  void now.value
  return props.background && role.value ? wokenWords(log.value, new Map(bk.requests.value.map((r) => [r.id, r]))) : ''
})
const notes = computed(() => role.value?.notes?.length ?? 0)

async function toggle() {
  const a = action.value
  if (!a) return
  busy.value = true
  try {
    const ticket = reads.begin()
    const out = await bk.roleAction(a)
    if (reads.current(ticket)) role.value = { ...(role.value ?? { state: '' }), ...out }
    await read()
  } catch (e) {
    toast.add({ title: a === 'pause' ? 'Not paused' : 'Not resumed', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    busy.value = false
  }
}

async function clear() {
  busy.value = true
  try {
    await bk.clearNotes()
    await read()
  } catch (e) {
    toast.add({ title: 'Notes not cleared', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    busy.value = false
  }
}

function show(at: 'wakes' | 'notes') {
  sheetAt.value = at
  sheet.value = true
}
</script>

<template>
  <div class="flex min-w-0 flex-col gap-5">
    <UCard :ui="{ header: 'px-4 py-3', body: 'p-4' }" data-brakeman-role>
      <template #header><SettingsCardTitle title="In the background" /></template>
      <div class="flex flex-col gap-3 text-sm">
        <p v-if="!props.background" class="text-muted" data-role-off>
          Runs only when asked. Turn on "Wake on events" in <NuxtLink to="/settings" class="underline">Settings</NuxtLink> to let a failed task, a waiting question or a briefing time wake it.
        </p>
        <UAlert v-if="problem" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="problem" />
        <p v-if="role && props.background" class="text-default" :data-role-state="role.state" data-role-words>{{ sentence }}</p>
        <p v-if="role && retryWords(role)" class="text-xs text-muted" data-role-retry>{{ retryWords(role) }}</p>
        <p v-if="budget" class="text-xs text-muted" data-role-budget data-turns-today>
          <template v-if="woken">{{ woken }} </template>{{ budget }}.
        </p>
        <div v-if="role && props.background" class="flex flex-wrap items-center gap-2">
          <UTooltip :text="action === 'pause' ? 'Wakes nothing new; a turn under way goes on.' : 'Lets events and briefing times wake it again.'">
            <UButton
              v-if="action"
              :label="action === 'pause' ? 'Pause' : 'Resume'"
              :icon="action === 'pause' ? 'i-lucide-pause' : 'i-lucide-play'"
              size="sm"
              color="neutral"
              variant="outline"
              class="max-sm:min-h-11"
              :loading="busy"
              :data-role-action="action"
              @click="toggle"
            />
          </UTooltip>
          <UButton label="Activity" icon="i-lucide-list" size="sm" color="neutral" variant="ghost" class="max-sm:min-h-11" data-role-activity @click="show('wakes')" />
        </div>
      </div>
    </UCard>

    <UCard :ui="{ body: 'p-4' }" data-brakeman-notes>
      <div class="flex flex-wrap items-center gap-2 text-sm">
        <h2 class="font-semibold text-highlighted">Notes</h2>
        <span class="text-xs text-muted" data-notes-count>{{ notes ? `${notes} kept, given to every turn` : 'None kept' }}</span>
        <UButton label="Open" size="sm" color="neutral" variant="ghost" class="ml-auto max-sm:min-h-11" data-notes-open @click="show('notes')" />
      </div>
    </UCard>

    <BrakemanActivity v-model:open="sheet" :role="role" :log="log" :busy="busy" :at="sheetAt" @clear="clear" />
  </div>
</template>
