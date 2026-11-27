<script setup lang="ts">
import { blockSubtitle, runOpen, type RunBlock } from '~/utils/sidebar'
import { rowMenuItems, stopQuestion } from '~/utils/sidebarActions'

/**
 * The header of a run block (design 3b, 3c): the play icon (amber while a member needs you, as the amber dot is), the run's own
 * name or its crew's, and when it started with how many agents. It opens the run page. `open` marks the run of the page open now
 * (the run page, or a member's page), which the list scrolls to. Hovering offers Share run, Stop run and Open run; Stop asks
 * first, in the row; a right-click or a long press opens the same menu. The list drives the keys: `focused` marks the header
 * they act on, `confirming` (a model: the hover Stop sets it too) shows the question.
 */
const props = withDefaults(defineProps<{ block: RunBlock; needsDot?: boolean; open?: boolean; busy?: boolean; focused?: boolean }>(), { needsDot: true, open: false, busy: false, focused: false })
const emit = defineEmits<{ share: [block: RunBlock]; stop: [block: RunBlock]; sheet: [block: RunBlock] }>()

// A touch long press (500 ms, design 3e) opens the header's sheet; the context menu is the mouse's.
const coarse = useMedia('(pointer: coarse)')
let press: number | undefined
function pressStart(e: PointerEvent) {
  if (e.pointerType !== 'touch') return
  window.clearTimeout(press)
  press = window.setTimeout(() => emit('sheet', props.block), 500)
}
function pressEnd() {
  window.clearTimeout(press)
}
const route = useRoute()
const router = useRouter()
const current = computed(() => runOpen(route.path, props.block.runId, route.query))
const amber = computed(() => props.block.state === 'needs' && props.needsDot)
const live = computed(() => !props.block.stoppedAt && props.block.state !== 'exited')
const confirming = defineModel<boolean>('confirming', { default: false })
const question = computed(() => stopQuestion({ kind: 'run', name: props.block.title }))
const menu = computed(() =>
  rowMenuItems(
    { kind: 'run', name: props.block.title, live: live.value },
    { open: () => router.push(`/runs/${encodeURIComponent(props.block.runId)}`), share: () => emit('share', props.block), stop: () => (confirming.value = true) },
  ),
)
function confirmStop() {
  confirming.value = false
  emit('stop', props.block)
}
</script>

<template>
  <UContextMenu :items="menu" :disabled="coarse">
    <div
      class="group relative flex flex-col rounded-md [-webkit-touch-callout:none]"
      :class="focused && 'ring-2 ring-primary/40'"
      :data-row-focused="focused ? '' : undefined"
      @pointerdown="pressStart"
      @pointerup="pressEnd"
      @pointercancel="pressEnd"
      @pointermove="pressEnd"
      @contextmenu="(e: Event) => coarse && e.preventDefault()"
    >
      <NuxtLink
        :to="`/runs/${encodeURIComponent(block.runId)}`"
        class="flex min-w-0 flex-1 items-center gap-2 rounded-md px-2.5 py-1.5 transition-colors"
        :class="current ? 'bg-default border border-default shadow-xs' : 'hover:bg-canvas-hover'"
        :aria-current="current ? 'page' : undefined"
        :title="`${block.title} · ${blockSubtitle(block)}`"
        data-sidebar-run-group
        :data-sidebar-run-open="open || current ? '' : undefined"
      >
        <UIcon name="i-lucide-play" class="size-4 flex-none" :class="amber ? 'text-warning' : 'text-muted'" :data-run-play="amber ? 'needs' : undefined" />
        <span class="flex min-w-0 flex-1 flex-col">
          <span class="flex min-w-0 items-center gap-1.5">
            <span class="truncate text-sm font-semibold text-highlighted">{{ block.title }}</span>
            <slot name="pill" />
            <ChatUnreadPill :run-id="block.runId" />
          </span>
          <template v-if="confirming">
            <span class="text-xs font-medium text-highlighted" data-run-stop-confirm>{{ question.title }}</span>
            <span class="text-[11px] leading-snug text-muted">{{ question.detail }}</span>
          </template>
          <span v-else class="truncate text-[11px] text-muted tabular-nums" :data-sidebar-run-subtitle="block.runId">{{ blockSubtitle(block) }}</span>
        </span>
      </NuxtLink>
      <span v-if="confirming" class="flex items-center justify-end gap-1 px-1.5 pb-1.5" data-run-stop-buttons>
        <UButton :label="question.keep" size="xs" color="neutral" variant="ghost" @click="confirming = false" />
        <UButton :label="question.confirm" icon="i-lucide-square" size="xs" color="error" :loading="busy" data-confirm-stop @click="confirmStop" />
      </span>
      <span
        v-else
        class="absolute right-1.5 top-1.5 flex items-center gap-0.5 rounded-md bg-default/95 p-0.5 opacity-0 shadow-xs ring ring-default transition-opacity focus-within:opacity-100 group-hover:opacity-100"
        data-run-actions
      >
        <UTooltip v-if="live" text="Share run">
          <UButton icon="i-lucide-share-2" size="xs" color="neutral" variant="ghost" :aria-label="`Share run ${block.title}`" data-run-share @click="emit('share', block)" />
        </UTooltip>
        <UTooltip v-if="live" text="Stop run">
          <UButton icon="i-lucide-square" size="xs" color="neutral" variant="ghost" :aria-label="`Stop run ${block.title}`" data-run-stop @click="confirming = true" />
        </UTooltip>
        <UTooltip text="Open run">
          <UButton icon="i-lucide-arrow-up-right" size="xs" color="neutral" variant="ghost" :aria-label="`Open run ${block.title}`" data-run-open @click="router.push(`/runs/${encodeURIComponent(block.runId)}`)" />
        </UTooltip>
      </span>
    </div>
  </UContextMenu>
</template>
