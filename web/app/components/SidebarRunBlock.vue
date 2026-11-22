<script setup lang="ts">
import type { ReplyTaken } from '~/utils/reply'
import { blockRows, type ListSection, type RunBlock, type SessionRow } from '~/utils/sidebar'

/**
 * A run as one block (design 3b): its header, then what of it waits on you (SidebarInboxRow), then its members joined by a line, an
 * exited member inside with Resume. In Needs you it shows the members that ask and one line for the rest ("and 2 more · 1 working · 1 finished", blockRows), a button that shows
 * them all for the visit. The members' and the header's actions pass up to the list.
 */
const props = withDefaults(defineProps<{ block: RunBlock; now: number; section?: ListSection; unfolded?: boolean; narrow?: boolean; panelFor?: string | null; needsDot?: boolean; open?: boolean; busy?: Set<string>; focusedId?: string | null; confirmingId?: string | null; big?: boolean }>(), { section: 'working', unfolded: false, narrow: false, panelFor: null, needsDot: true, open: false, busy: () => new Set(), focusedId: null, confirmingId: null, big: false })
const emit = defineEmits<{ shareRun: [block: RunBlock]; stopRun: [block: RunBlock]; share: [row: SessionRow]; stop: [row: SessionRow]; yard: [row: SessionRow]; openRun: [row: SessionRow]; answer: [row: SessionRow, index: number, question: string | undefined]; reply: [row: SessionRow, text: string, question: string | undefined, taken: ReplyTaken]; confirm: [id: string | null]; sheet: [target: SessionRow | RunBlock]; unfold: [runId: string]; answerOpen: [row: SessionRow] }>()
const shown = computed(() => blockRows(props.block, props.section, props.unfolded))
</script>

<template>
  <li class="flex flex-col gap-0.5" :data-sidebar-run-block="block.runId" :data-sidebar-row="`r:${block.runId}`" data-row-kind="run" :data-row-state="block.state">
    <SidebarRunHeader
      :block="block"
      :needs-dot="needsDot"
      :open="open"
      :busy="busy.has(`run:${block.runId}`)"
      :focused="focusedId === `r:${block.runId}`"
      :confirming="confirmingId === `r:${block.runId}`"
      @update:confirming="emit('confirm', $event ? `r:${block.runId}` : null)"
      @share="emit('shareRun', $event)"
      @stop="emit('stopRun', $event)"
      @sheet="emit('sheet', $event)"
    />
    <!-- The line joining the members is neutral, never a status colour: the state is each row's own. What of the run waits on you
         (its decisions, its tasks out of their attempts) comes first, before its members. -->
    <ol v-if="block.items.length || shown.members.length || shown.more" class="ml-4 flex flex-col gap-0.5 border-l border-default pl-1">
      <SidebarInboxRow v-for="it in block.items" :key="it.id" :row="it" :focused="focusedId === it.id" :big="big" />
      <SidebarSessionRow
        v-for="m in shown.members"
        :key="m.id"
        :row="m"
        :now="now"
        member
        :needs-dot="needsDot"
        :busy="busy.has(m.id)"
        :big="big"
        :focused="focusedId === `s:${m.id}`"
        :confirming="confirmingId === `s:${m.id}`"
        @update:confirming="emit('confirm', $event ? `s:${m.id}` : null)"
        @share="emit('share', $event)"
        @stop="emit('stop', $event)"
        @yard="emit('yard', $event)"
        @open-run="emit('openRun', $event)"
        @answer="(row, i, q) => emit('answer', row, i, q)"
        @reply="(row, text, q, taken) => emit('reply', row, text, q, taken)"
        @sheet="emit('sheet', $event)"
        :narrow="narrow"
        :panel-open="panelFor === `s:${m.id}`"
        @answer-open="emit('answerOpen', $event)"
      />
      <li v-if="shown.more" :data-sidebar-row="`m:${block.runId}`" data-row-kind="more" :data-row-focused="focusedId === `m:${block.runId}` ? '' : undefined">
        <button
          type="button"
          class="flex w-full min-w-0 items-center rounded-md px-2.5 py-1 text-left text-xs text-muted outline-none transition-colors hover:bg-canvas-hover focus-visible:ring-2 focus-visible:ring-primary"
          :class="[focusedId === `m:${block.runId}` && 'ring-2 ring-primary/40', big && 'min-h-11']"
          :title="shown.more.words"
          :aria-label="`Show every member of ${block.title}: ${shown.more.words}`"
          data-run-more
          @click="emit('unfold', block.runId)"
        >
          <span class="truncate">{{ shown.more.words }}</span>
        </button>
      </li>
    </ol>
  </li>
</template>
