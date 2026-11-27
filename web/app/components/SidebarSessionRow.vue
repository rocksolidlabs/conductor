<script setup lang="ts">
import type { ReplyTaken } from '~/utils/reply'
import { agentInitials, sessionMeta } from '~/utils/sessions'
import { exitLine as exitLineOf, rowMeta, sessionOpen, type SessionRow } from '~/utils/sidebar'
import { sessionStatus } from '~/utils/status'
import { sessionTarget } from '~/utils/actions'
import { sessionResumeOffered } from '~/utils/runs'
import { rowMenuItems, rowPrompt, sessionLive, stopQuestion } from '~/utils/sidebarActions'
import { isPinned, pinSession, unpinSession } from '~/utils/pins'

/**
 * One session in the list (design 3b, 3c): the name on one line across the row, its status mark (StatusMark of sessionStatus) at
 * its end; what it asks when it asks; and a meta line: the agent's initials as a small chip, agent · where · how long, and the
 * status's word last (cut only once the rest is). The question reads whole (#187): wrapped over its lines, never cut. A hosted session's
 * row carries its machine with a laptop, in place of a heading. The path is the row's tooltip. An exited row says how it ended
 * and when, with Resume beside the link (a link holds no button). Hovering offers Share, Stop and More in place of the mark; Stop
 * asks first, in the row; a right-click or a long press opens the same menu as More. A prompt is answered in the row, below the
 * link: its choices as numbered buttons, or a reply field. The list drives the keys: `focused` marks the row they act on,
 * `confirming` (a model: the hover Stop sets it too) shows the question.
 */
const props = withDefaults(defineProps<{ row: SessionRow; now: number; member?: boolean; needsDot?: boolean; busy?: boolean; focused?: boolean; big?: boolean; narrow?: boolean; panelOpen?: boolean }>(), { member: false, needsDot: true, busy: false, focused: false, big: false, narrow: false, panelOpen: false })
const emit = defineEmits<{ answerOpen: [row: SessionRow]; share: [row: SessionRow]; stop: [row: SessionRow]; yard: [row: SessionRow]; openRun: [row: SessionRow]; answer: [row: SessionRow, index: number, question: string | undefined]; reply: [row: SessionRow, text: string, question: string | undefined, taken: ReplyTaken]; sheet: [row: SessionRow] }>()

// A touch long press (500 ms, design 3e) opens the row's sheet; the context menu is the mouse's and a coarse pointer's own is off.
const coarse = useMedia('(pointer: coarse)')
let press: number | undefined
function pressStart(e: PointerEvent) {
  if (e.pointerType !== 'touch') return
  window.clearTimeout(press)
  press = window.setTimeout(() => emit('sheet', props.row), 500)
}
function pressEnd() {
  window.clearTimeout(press)
}
const route = useRoute()
const router = useRouter()

const s = computed(() => props.row.session)
const status = computed(() => sessionStatus(s.value, props.now))
const current = computed(() => sessionOpen(route.path, s.value.id, route.query))
const meta = computed(() => rowMeta(s.value, props.member, props.now))
/** The meta line after the machine (which carries the laptop), the parts joined by middle dots as text, so it reads and copies as one line. */
const metaText = computed(() => (props.row.machine ? meta.value.slice(1).map((p) => ` · ${p}`).join('') : meta.value.join(' · ')))
const exitLine = computed(() => exitLineOf(props.row, props.now))
const live = computed(() => sessionLive(s.value.status))
const confirming = defineModel<boolean>('confirming', { default: false })
const question = computed(() => stopQuestion({ kind: 'session', name: s.value.name }))
const prompt = computed(() => rowPrompt(props.row))
/** In a narrow sidebar the question is two lines here and whole, with its controls, in the panel beside the row (SidebarRowPanel). */
const clamped = computed(() => props.narrow && !!props.row.prompt)

const target = computed(() => sessionTarget(s.value, live.value))
// Rename… opens the one dialog (#318), on the session as the row shows it; Clone… and Clone with options… theirs (#434).
const renameModal = useRenameModal()
const cloneModal = useCloneModal()
// An exited server session's row ends with Resume, outside the link; the hover actions stop short of it (#257).
const attention = useAttention()
const resumable = computed(() => props.row.state === 'exited' && s.value.kind === 'server' && sessionResumeOffered(s.value, attention.runOf))
// The Share tooltip, shown on hover or focus, would stay up once the share dialog closed and gave the focus back to the button:
// a click mutes it until the pointer leaves the button (#137).
const shareTip = ref(false)
const shareTipMuted = ref(false)
// More's menu is open while the list says so: a row drawn anew in another section (its turn ended, it asks) keeps it open.
const menuOpen = useRowMenu(() => `s:${s.value.id}`)
// Pin to top and Unpin (utils/pins.ts): the pinned sessions are the list's first group.
const ui = useUiState()
const pinned = computed(() => isPinned(ui.pins.value, s.value.id))
const menu = computed(() =>
  rowMenuItems({ ...target.value, pinned: pinned.value }, {
    pin: () => ui.setPins(pinSession(ui.pins.value, s.value)),
    unpin: () => ui.setPins(unpinSession(ui.pins.value, s.value.id)),
    open: () => router.push(`/sessions/${s.value.id}`),
    openRun: () => emit('openRun', props.row),
    share: () => emit('share', props.row),
    yard: () => emit('yard', props.row),
    rename: () => renameModal.show(s.value),
    clone: () => cloneModal.show(s.value),
    cloneWithOptions: () => void cloneModal.withOptions(s.value),
    stop: () => (confirming.value = true),
  }),
)

function confirmStop() {
  confirming.value = false
  emit('stop', props.row)
}
</script>

<template>
  <UContextMenu :items="menu" :disabled="coarse">
    <!-- The open session's box is the row's, so that a prompt answered below the link sits inside it. -->
    <li
      class="group relative flex flex-col rounded-md [-webkit-touch-callout:none]"
      :class="[current && 'bg-default border border-default shadow-xs', focused && 'ring-2 ring-primary/40']"
      :data-sidebar-row="`s:${s.id}`"
      data-row-kind="session"
      :data-row-state="row.state"
      :data-row-focused="focused ? '' : undefined"
      @pointerdown="pressStart"
      @pointerup="pressEnd"
      @pointercancel="pressEnd"
      @pointermove="pressEnd"
      @contextmenu="(e: Event) => coarse && e.preventDefault()"
    >
      <div class="flex items-center gap-1">
      <NuxtLink
        :to="`/sessions/${s.id}`"
        class="flex min-w-0 flex-1 flex-col gap-0.5 rounded-md px-2.5 py-1.5 transition-colors"
        :class="!current && 'hover:bg-canvas-hover'"
        :aria-current="current ? 'page' : undefined"
        :title="sessionMeta(s, now)"
      >
          <span class="flex min-w-0 items-center gap-1.5">
            <!-- An exited row is drawn whole, its name muted: never faded (the UX review's P16). -->
            <span class="truncate text-sm" :class="[row.state === 'needs' ? 'font-semibold' : 'font-medium', row.state === 'exited' && !current && 'text-muted']" data-row-name>{{ s.name }}</span>
            <EventMarkBadge :session-id="s.id" :finished="status.key === 'finished'" />
            <YoloBadge v-if="s.yolo" icon />
            <slot name="pill" />
            <!-- Unread chat (design 2g): a neutral pill, never a status colour. -->
            <ChatUnreadPill :session-id="s.id" />
            <!-- The mark gives way to the actions on hover; its word is its label and its title. -->
            <StatusMark :status="status" :quiet="!needsDot" class="ml-auto group-hover:opacity-0" />
          </span>
          <template v-if="confirming">
            <span class="text-xs font-medium text-highlighted" data-row-stop-confirm>{{ question.title }}</span>
            <span class="text-[11px] leading-snug text-muted">{{ question.detail }}</span>
          </template>
          <template v-else>
            <!-- The question wrapped over its lines, a long token (a command, a URL) breaking rather than overflowing (#187). In a
                 sidebar 280 px wide and wider it is whole here, its controls below; narrower, a question at the server's bound (500
                 bytes, session.MaxAttentionMessage) would be two dozen lines, so it is two here and whole in the panel beside the row,
                 which focus opens and the Answer button below opens with the focus in it (owner decision 7): never a clamp without
                 the panel, so the tail is one press away for the keyboard and a touch screen alike. -->
            <span v-if="row.prompt" class="break-words text-xs" :class="clamped && 'line-clamp-2'" :data-row-clamped="clamped ? '' : undefined" data-row-prompt>{{ row.prompt.message }}</span>
            <!-- The meta line: the initials as a chip, then the meta, then the status's word. The meta takes only the room the word
                 leaves (its basis 0), so it is cut first and the word only once the meta has none. -->
            <span class="flex min-w-0 items-center gap-1.5 text-[11px] text-muted" data-row-meta-line>
              <span
                class="flex-none rounded px-1 font-mono text-[10px] leading-4 font-semibold select-none"
                :class="row.state === 'exited' ? 'border border-dashed border-accented' : 'bg-default text-toned'"
                :title="s.agentId"
                data-row-initials
              >{{ agentInitials(s.agentId) }}</span>
              <span v-if="row.state === 'exited'" class="min-w-0 truncate tabular-nums" data-row-exit-line>{{ exitLine }}</span>
              <template v-else>
                <span class="flex min-w-0 flex-1 basis-0 items-center overflow-hidden whitespace-nowrap font-mono" data-row-meta>
                  <span v-if="row.machine" class="inline-flex flex-none items-center gap-1" :data-row-machine="row.machine">
                    <UIcon name="i-lucide-laptop" class="size-3 flex-none" :aria-label="`Hosted on ${row.machine}`" />{{ meta[0] }}
                  </span>
                  <!-- Spaces kept: the text starts with the dot that follows the machine. -->
                  <span class="overflow-hidden text-ellipsis whitespace-pre">{{ metaText }}</span>
                </span>
                <span class="min-w-0 truncate" :title="status.word" data-status-word>{{ status.word }}</span>
              </template>
            </span>
          </template>
      </NuxtLink>
      <ResumeButton v-if="resumable" :session="s" icon-only size="xs" class="relative z-10 mr-1.5" />
      </div>
      <!-- The confirm's buttons sit below the link, after its words, never over them; the hover actions beside the link: a link holds no button. -->
      <span v-if="confirming" class="flex items-center justify-end gap-1 px-1.5 pb-1.5" data-row-stop-buttons>
        <UButton :label="question.keep" size="xs" color="neutral" variant="ghost" @click="confirming = false" />
        <UButton :label="question.confirm" icon="i-lucide-square" size="xs" color="error" :loading="busy" data-confirm-stop @click="confirmStop" />
      </span>
      <!-- On a resumable row the bar is centred vertically, as the arrow beside the link is, so the … and the arrow share one centre
           and sit side by side at the edge (#331); a running row's bar sits at the top beside its first line. -->
      <span
        v-else
        class="absolute flex items-center gap-0.5 rounded-md bg-default/95 p-0.5 opacity-0 shadow-xs ring ring-default transition-opacity focus-within:opacity-100 group-hover:opacity-100"
        :class="resumable ? 'right-9 top-1/2 -translate-y-1/2' : 'right-1.5 top-1.5'"
        data-row-actions
      >
        <UTooltip v-if="live" text="Share" :open="shareTip && !shareTipMuted" @update:open="(o: boolean) => (shareTip = o)">
          <UButton icon="i-lucide-share-2" size="xs" color="neutral" variant="ghost" :aria-label="`Share ${s.name}`" data-row-share @click="shareTipMuted = true; emit('share', row)" @pointerleave="shareTipMuted = false" />
        </UTooltip>
        <UTooltip v-if="live" text="Stop">
          <UButton icon="i-lucide-square" size="xs" color="neutral" variant="ghost" :aria-label="`Stop ${s.name}`" data-row-stop @click="confirming = true" />
        </UTooltip>
        <UDropdownMenu v-model:open="menuOpen" :items="menu" :content="{ align: 'end' }">
          <IconButton icon="i-lucide-ellipsis" size="xs" color="neutral" variant="ghost" :label="`More for ${s.name}`" data-row-more />
        </UDropdownMenu>
      </span>
      <div v-if="prompt && !confirming && clamped" class="px-2.5 pb-1.5">
        <UButton
          label="Answer"
          icon="i-lucide-message-square-reply"
          size="xs"
          color="neutral"
          variant="outline"
          :aria-expanded="panelOpen"
          aria-haspopup="dialog"
          :aria-label="`Answer ${s.name}'s question`"
          data-row-answer-open
          @click="emit('answerOpen', row)"
        />
      </div>
      <SidebarPrompt :draft-key="row.session.id" v-else-if="prompt && !confirming" :prompt="prompt" :busy="busy" :big="big" @answer="(i, q) => emit('answer', row, i, q)" @reply="(text, q, taken) => emit('reply', row, text, q, taken)" />
    </li>
  </UContextMenu>
</template>
