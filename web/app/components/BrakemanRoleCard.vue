<script setup lang="ts">
import type { BrakemanRole, RoleLogRow } from '~/utils/brakeman'
import { clockTime, pauseAction, retryWords, roleWords, rowWords } from '~/utils/brakemanRole'

/**
 * Brakeman's background role on the Brakeman page (docs/plans/brakeman.md, BK3, PR-15): its state in words (an unavailable role says
 * why and what tries again), Pause and Resume, its durable notes with Clear, and its log of wakes, newest first, each with its source,
 * what it was about, the batch it joined and the request that batch made. The role and its log are read again on each of its
 * `brakeman` events; nothing here takes the focus or moves the page when the role changes.
 */
const props = defineProps<{ windowSeconds?: number; background: boolean }>()

const bk = useBrakeman()
const toast = useToast()

const role = ref<BrakemanRole | null>(null)
const log = ref<RoleLogRow[]>([])
const busy = ref(false)
const problem = ref('')

/** How many rows of the log the page shows. */
const LOG_ROWS = 50

async function read() {
  try {
    const out = await bk.role(LOG_ROWS)
    role.value = out.role
    log.value = out.log
    problem.value = ''
  } catch (e) {
    problem.value = (e as Error).message
  }
}

onMounted(read)
watch(() => bk.roleSeq.value, () => void read())

const action = computed(() => pauseAction(role.value ?? undefined))
const tone = computed(() => {
  switch (role.value?.state) {
    case 'unavailable':
      return 'warning' as const
    case 'briefing':
    case 'gathering':
      return 'primary' as const
  }
  return 'neutral' as const
})

async function toggle() {
  const a = action.value
  if (!a) return
  busy.value = true
  try {
    role.value = { ...(role.value ?? { state: '' }), ...(await bk.roleAction(a)) }
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
</script>

<template>
  <UCard :ui="{ header: 'px-4 py-3', body: 'p-4' }" data-brakeman-role>
    <template #header>
      <SettingsCardTitle title="In the background" description="Woken by events and briefing times, a batch a turn, then a cooldown; its turns page no one." />
    </template>
    <div class="flex flex-col gap-4 text-sm">
      <p v-if="!props.background" class="text-muted" data-role-off>
        Brakeman runs only when asked. Turn on "Wake Brakeman on events" in <NuxtLink to="/settings" class="underline">Settings</NuxtLink> to let a failed task, a waiting question or a briefing time wake it.
      </p>
      <UAlert v-if="problem" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="problem" />
      <div v-if="role" class="flex flex-wrap items-center gap-3" :data-role-state="role.state">
        <UBadge :label="role.state" :color="tone" variant="subtle" />
        <span class="text-muted" data-role-words>{{ roleWords(role, props.windowSeconds) }}</span>
        <UButton
          v-if="action"
          :label="action === 'pause' ? 'Pause' : 'Resume'"
          :icon="action === 'pause' ? 'i-lucide-pause' : 'i-lucide-play'"
          size="sm"
          color="neutral"
          variant="outline"
          class="ml-auto"
          :loading="busy"
          :data-role-action="action"
          @click="toggle"
        />
      </div>
      <p v-if="role && retryWords(role)" class="text-xs text-muted" data-role-retry>{{ retryWords(role) }}</p>

      <section class="flex flex-col gap-2" data-role-notes>
        <div class="flex items-center gap-2">
          <h3 class="text-xs font-semibold text-highlighted">Notes</h3>
          <span class="text-xs text-muted">What you kept from briefs, given to every turn.</span>
          <UButton v-if="role?.notes?.length" label="Clear" size="xs" color="neutral" variant="ghost" class="ml-auto" :loading="busy" data-role-clear-notes @click="clear" />
        </div>
        <p v-if="!role?.notes?.length" class="text-xs text-muted">None kept.</p>
        <ul v-else class="flex flex-col gap-1">
          <li v-for="(n, i) in role.notes" :key="i" class="text-xs" data-role-note>
            {{ n.text }} <span v-if="n.from" class="text-muted">({{ n.from }})</span>
          </li>
        </ul>
      </section>

      <section class="flex flex-col gap-2" data-role-log>
        <h3 class="text-xs font-semibold text-highlighted">Wakes</h3>
        <p v-if="!log.length" class="text-xs text-muted">Nothing has woken it yet.</p>
        <ul v-else class="flex flex-col gap-1">
          <li v-for="(row, i) in log" :key="`${row.at}-${i}`" class="flex flex-wrap gap-x-3 text-xs" :data-role-row="row.source" :data-role-batch="row.batch" :data-role-request="row.request" :data-role-held="row.held">
            <span class="text-muted tabular-nums">{{ clockTime(row.at) }}</span>
            <span>{{ rowWords(row).what }}</span>
            <span v-if="rowWords(row).fate" class="text-muted">{{ rowWords(row).fate }}</span>
          </li>
        </ul>
      </section>
    </div>
  </UCard>
</template>
