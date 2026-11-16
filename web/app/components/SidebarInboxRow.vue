<script setup lang="ts">
import { kindLook } from '~/utils/inbox'
import { itemWords, type ItemRow } from '~/utils/sidebar'

/**
 * A row of Needs you for what waits on a person that is no session's question (the UX review's P3): a plan waiting for approval, a
 * task out of its attempts, a run's decision. Its kind's mark and word (kindLook), the title on one line and the question or brief on
 * one line, each cut with its whole text in its title. A decision opens its card, in place; a plan or a task opens on the Needs you
 * page. The list drives the keys: `focused` marks the row they act on; Enter on the row is the list's (it opens the same).
 */
const props = withDefaults(defineProps<{ row: ItemRow; focused?: boolean; big?: boolean }>(), { focused: false, big: false })
const decisionModal = useDecisionModal()
const router = useRouter()

const look = computed(() => kindLook(props.row.item.kind))
const words = computed(() => itemWords(props.row.item))
const decision = computed(() => (props.row.item.kind === 'decision' ? props.row.item.decision?.id : undefined))

function open() {
  if (decision.value) decisionModal.open(decision.value)
  else router.push(`/decisions?item=${encodeURIComponent(props.row.item.ref)}`)
}
</script>

<template>
  <li :data-sidebar-row="row.id" data-row-kind="item" :data-row-focused="focused ? '' : undefined">
    <button
      type="button"
      class="flex w-full min-w-0 items-start gap-2 rounded-md px-2.5 py-1.5 text-left outline-none transition-colors hover:bg-canvas-hover focus-visible:ring-2 focus-visible:ring-primary"
      :class="[focused && 'ring-2 ring-primary/40', big && 'min-h-11']"
      :aria-label="`${look.word}: ${words.title}${words.detail ? `. ${words.detail}` : ''}`"
      :data-sidebar-item="row.item.ref"
      :data-sidebar-item-kind="row.item.kind"
      :data-sidebar-decision="decision"
      @click="open"
    >
      <UIcon :name="look.icon" class="mt-0.5 size-4 flex-none text-warning" aria-hidden="true" />
      <span class="flex min-w-0 flex-1 flex-col gap-0.5">
        <span class="truncate text-sm font-semibold" :title="words.title" data-item-title>{{ words.title }}</span>
        <span class="flex min-w-0 items-center gap-1.5 text-[11px] text-muted">
          <span class="flex-none font-medium" data-item-kind-word>{{ look.word }}</span>
          <span v-if="words.detail" class="min-w-0 truncate" :title="words.detail" data-item-detail>{{ words.detail }}</span>
        </span>
      </span>
    </button>
  </li>
</template>
