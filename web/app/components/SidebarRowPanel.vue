<script setup lang="ts">
import type { ReplyTaken } from '~/utils/reply'
import type { SessionRow } from '~/utils/sidebar'
import { rowPrompt } from '~/utils/sidebarActions'

/**
 * A row's question whole, beside it (the UX review's P5, owner decision 7): in a sidebar narrower than 280 px the row shows two lines
 * of its question, and this panel, to the right of the sidebar, holds the session's name, the whole question and the row's own prompt
 * controls (SidebarPrompt), their labels wrapped rather than cut. It is not modal: the list stays live and in reach of assistive
 * technology, and it never takes the focus by itself; the list moves the focus into it when the row's Answer is pressed. Its keys go
 * to the list (`keys`): Escape closes it with the focus back on the row, a digit answers. One panel for the list, anchored to the row
 * it is for (`anchor`); it carries `data-row-panel` and `data-panel-for="s:<id>"`.
 */
const props = withDefaults(defineProps<{ row: SessionRow; anchor: HTMLElement | null; busy?: boolean }>(), { busy: false })
const emit = defineEmits<{ answer: [row: SessionRow, index: number, question: string | undefined]; reply: [row: SessionRow, text: string, question: string | undefined, taken: ReplyTaken]; keys: [e: KeyboardEvent]; leave: [e: FocusEvent]; shown: [] }>()
const prompt = computed(() => rowPrompt(props.row))
const body = useTemplateRef<HTMLElement>('body')
// Drawn: the list may move the focus into it now.
watch(body, (el) => el && emit('shown'), { flush: 'post' })

/** Moves the focus to the panel's first control: a choice, or the reply field. */
function focusFirst() {
  body.value?.querySelector<HTMLElement>('[data-row-answer] button:not([disabled]), [data-row-answer] input:not([disabled])')?.focus()
}
defineExpose({ focusFirst })

/** The panel takes no focus by itself as it opens, and gives none back as it closes: the list does both. */
const content = { side: 'right' as const, align: 'start' as const, sideOffset: 12, onOpenAutoFocus: (e: Event) => e.preventDefault(), onCloseAutoFocus: (e: Event) => e.preventDefault() }
</script>

<template>
  <UPopover :open="!!anchor && !!prompt" :reference="anchor ?? undefined" :dismissible="false" :content="content" :ui="{ content: 'w-80 max-w-[calc(100vw-2rem)]' }">
    <template #content>
      <div ref="body" class="flex flex-col gap-1.5 pt-2.5" data-row-panel :data-panel-for="`s:${row.id}`" @keydown="emit('keys', $event)" @focusout="emit('leave', $event)">
        <p class="truncate px-3 text-sm font-semibold text-highlighted" :title="row.session.name" data-panel-name>{{ row.session.name }}</p>
        <p class="max-h-[50vh] overflow-y-auto px-3 text-xs break-words" data-panel-question>{{ row.prompt?.message }}</p>
        <SidebarPrompt :draft-key="row.session.id" v-if="prompt" :prompt="prompt" :busy="busy" wrap @answer="(i, q) => emit('answer', row, i, q)" @reply="(text, q, taken) => emit('reply', row, text, q, taken)" />
      </div>
    </template>
  </UPopover>
</template>
