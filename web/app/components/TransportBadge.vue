<script setup lang="ts">
import type { TransportKind } from '~/utils/protocol'
import type { TransportState } from '~/utils/transport/types'

const props = defineProps<{ kind: TransportKind; state: TransportState; rtt?: number | null }>()

const name = computed(() => {
  switch (props.kind) {
    case 'ws':
      return 'WebSocket'
    case 'webrtc':
      return 'WebRTC direct'
    case 'relay':
      return 'Relay'
  }
  return props.kind
})

const label = computed(() => {
  switch (props.state) {
    case 'connecting':
      return 'connecting'
    case 'signaling':
      return 'negotiating WebRTC'
    case 'closed':
      return 'disconnected'
    case 'idle':
      return 'idle'
  }
  return props.rtt != null ? `${name.value} · ${props.rtt} ms` : name.value
})

const color = computed(() => {
  if (props.state === 'closed') return 'error'
  if (props.state !== 'open') return 'warning'
  // A direct connection is neutral: green is for a live agent (docs/design/system.md, Status).
  return 'neutral'
})

const icon = computed(() => {
  if (props.state !== 'open') return 'i-lucide-loader-circle'
  return props.kind === 'webrtc' ? 'i-lucide-radio' : props.kind === 'relay' ? 'i-lucide-route' : 'i-lucide-cable'
})
</script>

<template>
  <UBadge :data-transport-kind="kind" :data-transport-state="state" :label="label" :icon="icon" :color="color" variant="subtle" size="sm" class="font-mono" />
</template>
