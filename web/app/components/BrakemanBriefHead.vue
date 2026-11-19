<script setup lang="ts">
import type { BrakemanBrief, BrakemanDigest, BrakemanRequest } from '~/utils/brakeman'
import { briefAbout, kindWords } from '~/utils/brakeman'
import { relativeTime } from '~/utils/sessions'

/**
 * A brief's head: "new" while it was not opened when drawn, the kind's words, how long ago it was kept, and what it is about by name
 * (an asked request's subjects; a wake request's runs as its brief cites them, at most three and then "+N"). On a phone the line is the
 * age and the kind alone, and the head's actions give way to the `phone` slot's (Share, as an icon a thumb can tap).
 */
const props = defineProps<{ request: BrakemanRequest; brief: BrakemanBrief; digest?: BrakemanDigest; fresh?: boolean }>()

const live = useAttention()
const now = useNow()
const names = {
  session: (id: string) => live.sessions.value.find((s) => s.id === id)?.name,
  run: (id: string) => live.runNames.value[id],
}
const about = computed(() => briefAbout(props.request, { digest: props.digest, items: props.brief.items }, names))
const age = computed(() => (props.request.briefAt ? relativeTime(props.request.briefAt, now.value, { suffix: true }) : ''))
</script>

<template>
  <div class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1" data-brief-head>
    <UBadge v-if="fresh" label="new" color="primary" variant="subtle" size="sm" data-brief-new />
    <p class="flex min-w-0 flex-1 flex-wrap items-center gap-x-1.5 text-xs text-muted">
      <span data-brief-kind>{{ kindWords(request.kind) }}</span>
      <template v-if="age">
        <span aria-hidden="true">·</span>
        <span data-brief-age>{{ age }}</span>
      </template>
      <template v-if="about.length">
        <span class="max-sm:hidden" aria-hidden="true">·</span>
        <span class="min-w-0 break-words max-sm:hidden" data-brief-about>{{ about.join(', ') }}</span>
      </template>
    </p>
    <div v-if="$slots.default" class="flex flex-none items-center gap-1 max-sm:hidden">
      <slot />
    </div>
    <div v-if="$slots.phone" class="flex flex-none items-center sm:hidden" data-brief-head-phone>
      <slot name="phone" />
    </div>
  </div>
</template>
