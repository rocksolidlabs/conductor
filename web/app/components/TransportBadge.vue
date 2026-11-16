<script setup lang="ts">
import type { TransportKind } from '~/utils/protocol'
import type { TransportState } from '~/utils/transport/types'
import { transportLook } from '~/utils/transport/look'

defineOptions({ inheritAttrs: false })

/** `compact` (the session page): a healthy connection is its icon alone, its words the label and the tooltip. */
const props = defineProps<{ kind: TransportKind; state: TransportState; rtt?: number | null; compact?: boolean }>()

const look = computed(() => transportLook(props.kind, props.state, props.rtt, props.compact === true))
</script>

<template>
  <UTooltip v-if="!look.showWords" :text="look.words">
    <UBadge v-bind="$attrs" :data-transport-kind="kind" :data-transport-state="state" :aria-label="look.words" role="img" :icon="look.icon" :color="look.color" variant="subtle" size="sm" class="font-mono" />
  </UTooltip>
  <UBadge v-else v-bind="$attrs" :data-transport-kind="kind" :data-transport-state="state" :label="look.words" :icon="look.icon" :color="look.color" variant="subtle" size="sm" class="font-mono" />
</template>
