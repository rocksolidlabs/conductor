<script setup lang="ts">
import { nowTiles, sinceWords } from '~/utils/brakemanNow'
import { clockOfDay } from '~/utils/brakemanRows'
import { needsDotShown, sidebarModel } from '~/utils/sidebar'

/**
 * Now, on the Brakeman page (the Brakeman design review): Conductor's own counts, live, with what each is, composed from what the
 * sidebar counts (the same model, so the page and the sidebar never disagree) and the runs' tasks the page holds; under them, what
 * changed since the last briefing (GET /api/brakeman/since, read while the page is open). No agent turn runs for it, and it spends no
 * budget. On a phone the four counts alone, two by two.
 */
const bk = useBrakeman()
const live = useAttention()
const oneList = useNeedsYou()
const events = useEvents()
const now = useNow()

const model = computed(() => sidebarModel(live.sessions.value, live.runOf, oneList.items.value))
const tiles = computed(() =>
  nowTiles({ model: model.value, needs: oneList.list.value, runs: live.runs.value, sessions: live.sessions.value, brakemanSession: bk.state.value?.current?.sessionId, now: now.value }),
)
const needsDot = computed(() => needsDotShown(events.routes.value))
const since = computed(() => {
  const s = bk.sinceNow.value
  if (!s) return ''
  return sinceWords(s, clockOfDay, { run: (id) => live.runNames.value[id], session: (id) => live.sessions.value.find((x) => x.id === id)?.name }, now.value)
})
</script>

<template>
  <UCard :ui="{ header: 'px-4 py-3 max-sm:hidden', body: 'p-4 max-sm:p-0 max-sm:ring-0' }" class="max-sm:bg-transparent max-sm:ring-0" data-brakeman-now>
    <template #header>
      <SettingsCardTitle title="Now" description="Conductor's own count, live. No agent turn, no budget." />
    </template>
    <div class="flex flex-col gap-3">
      <ul class="grid grid-cols-2 gap-2 lg:grid-cols-4" aria-label="Now">
        <li v-for="t in tiles" :key="t.key" class="min-w-0">
          <NuxtLink
            :to="t.to"
            class="flex h-full min-h-12 min-w-0 flex-col justify-center gap-0.5 rounded-md px-3 py-2 ring ring-default hover:bg-elevated/50 focus-visible:outline-2 focus-visible:outline-primary"
            :data-now-tile="t.key"
            :data-now-count="t.count"
          >
            <span class="flex min-w-0 items-center gap-1.5 text-sm font-medium text-highlighted">
              <StatusMark v-if="t.mark" :status="t.mark" :quiet="t.key === 'needs' && !needsDot" />
              <span class="truncate">{{ t.words }}</span>
            </span>
            <span v-if="t.names" class="truncate text-xs text-muted max-sm:hidden" :title="t.names" data-now-names>{{ t.names }}</span>
          </NuxtLink>
        </li>
      </ul>
      <p v-if="since" class="text-xs text-muted max-sm:hidden" data-now-since :data-since-from="bk.sinceNow.value?.since.from">{{ since }}</p>
    </div>
  </UCard>
</template>
