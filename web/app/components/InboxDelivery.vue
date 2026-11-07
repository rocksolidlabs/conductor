<script setup lang="ts">
import { STEP_TITLES, stateLook, stateWords, type DeliveryStep } from '~/utils/inbox'
import { clockTime } from '~/utils/brakemanRole'

/**
 * An action's four steps on the decision page (docs/plans/decision-inbox.md, "The four steps"): the request was there, the response
 * was recorded, the execution owner took it and checked it, and the work resumed, or why not; each done, waiting, refused, does not
 * apply or unknown, with its time, its words and its receipt's link. They never merge, and a step never moves back in the page's
 * view (reconcileDelivery). What changed of the subject since is the item's `now`, shown beside them, never as a step.
 */
defineProps<{ steps: readonly DeliveryStep[]; now?: string; routeOf: (ref: string) => string | null }>()
</script>

<template>
  <div class="flex flex-col gap-2" data-inbox-delivery>
    <ol class="flex flex-col gap-1.5">
      <li v-for="s in steps" :key="s.step" class="flex items-start gap-2 text-sm" :data-step="s.step" :data-step-state="s.state">
        <UIcon :name="stateLook(s.state).icon" class="mt-0.5 size-4 shrink-0" :class="`text-${stateLook(s.state).color === 'neutral' ? 'muted' : stateLook(s.state).color}`" />
        <div class="min-w-0 flex-1">
          <p class="flex flex-wrap items-baseline gap-x-2">
            <span class="font-medium text-highlighted">{{ STEP_TITLES[s.step] }}</span>
            <UBadge :label="stateWords(s.state)" :color="stateLook(s.state).color" variant="subtle" size="sm" data-step-word />
            <span v-if="s.at" class="text-xs text-muted">{{ clockTime(s.at) }}</span>
          </p>
          <p v-if="s.words" class="text-xs text-muted text-pretty break-words" data-step-words>{{ s.words }}</p>
          <ULink v-if="s.receipt && routeOf(s.receipt)" :to="routeOf(s.receipt)!" class="text-xs text-primary break-all" data-step-receipt>{{ s.receipt }}</ULink>
          <span v-else-if="s.receipt" class="text-xs text-muted break-all" data-step-receipt>{{ s.receipt }}</span>
        </div>
      </li>
    </ol>
    <UAlert v-if="now" color="neutral" variant="subtle" icon="i-lucide-info" :title="`Now: ${now}`" data-inbox-now />
  </div>
</template>
