<script setup lang="ts">
import { markHidden } from '~/utils/hiddenMarks'
import type { MenuRow } from '~/utils/chatBoxMenu'

/**
 * The chat box's list (UX3 R6, R11), above the box: a file for `@`, one of the agent's own commands for `/`. The box keeps the cursor
 * and names the active row (aria-activedescendant); ChatComposer moves it with the arrows and picks with Enter or Tab. A press on a
 * row picks it without taking the focus from the box. A file's path is cut in its middle, its name kept; a command is `/name` with
 * its hint and the agent's own words. A character a person cannot see is drawn as a mark. `foot` is one line under the rows, only
 * when there is something the person must know to go on.
 */
const props = defineProps<{ id: string; kind: 'file' | 'command'; rows: MenuRow[]; active: number; foot?: string }>()
const emit = defineEmits<{ pick: [index: number, trusted: boolean]; hover: [index: number] }>()

function parts(path: string): { dir: string; name: string } {
  const i = path.lastIndexOf('/')
  return i < 0 ? { dir: '', name: path } : { dir: path.slice(0, i + 1), name: path.slice(i + 1) }
}
const optionId = (i: number) => `${props.id}-${i}`
defineExpose({ optionId })
</script>

<template>
  <div class="absolute inset-x-3 bottom-full z-20 mb-1 overflow-hidden rounded-lg border border-default bg-default shadow-lg" data-chat-menu :data-chat-menu-kind="kind">
    <ul v-if="rows.length" :id="id" role="listbox" :aria-label="kind === 'file' ? 'Files' : 'Commands'" class="flex max-h-[22rem] flex-col overflow-y-auto py-1">
      <li
        v-for="(r, i) in rows"
        :id="optionId(i)"
        :key="r.key"
        role="option"
        :aria-selected="i === active"
        :aria-disabled="!r.pickable || undefined"
        class="flex min-h-9 min-w-0 cursor-pointer items-center gap-2 px-3 py-1.5 text-xs pointer-coarse:min-h-11"
        :class="[i === active ? 'bg-elevated' : '', r.pickable ? 'text-default' : 'cursor-default text-muted']"
        :data-chat-menu-row="r.value"
        :data-chat-menu-active="i === active || undefined"
        @mousedown.prevent
        @mousemove="emit('hover', i)"
        @click="(e: MouseEvent) => emit('pick', i, e.isTrusted)"
      >
        <template v-if="kind === 'file'">
          <UIcon name="i-lucide-file" class="size-3.5 flex-none text-muted" aria-hidden="true" />
          <span class="flex min-w-0 flex-1 font-mono" data-mono="path" :title="r.value">
            <span class="min-w-0 truncate text-muted"><MarkedText :pieces="markHidden(parts(r.value).dir).pieces" :breaks="false" /></span>
            <span class="flex-none"><MarkedText :pieces="markHidden(parts(r.value).name).pieces" :breaks="false" /></span>
          </span>
        </template>
        <template v-else>
          <span class="flex-none font-mono" data-mono="command"><MarkedText :pieces="markHidden(`/${r.value}`).pieces" :breaks="false" /></span>
          <span v-if="r.hint" class="flex-none font-mono text-muted" data-mono="command">{{ r.hint }}</span>
          <span v-if="r.words" class="ml-auto min-w-0 truncate text-muted" :title="r.words" data-chat-menu-words>{{ r.words }}</span>
        </template>
      </li>
    </ul>
    <p v-if="foot" class="px-3 py-1.5 text-[11px] text-muted" :class="rows.length ? 'border-t border-default' : ''" role="status" data-chat-menu-foot>{{ foot }}</p>
  </div>
</template>
