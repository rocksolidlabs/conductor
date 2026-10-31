<script setup lang="ts">
import type { DispatchLabel, DispatchMilestone, TicketSummary } from '~/composables/useDispatch'
import type { AgentInfo } from '~/composables/useSessions'
import { PICK_WORDS, PRIORITIES, TICKET_STATUSES, assigneeOf, labelDot, pickAdds, pickHeld, statusWord, targetWords, typedPick, type PickField } from '~/utils/dispatch'
import { DispatchOrder, through } from '~/utils/dispatchOrder'

/**
 * A picker of one of a ticket's fields (docs/plans/dispatch.md, Owner decisions, 10): what the list's letters open (S status, P priority,
 * A assignees, L labels, M milestone, O owner) for the focused ticket or the tickets selected with X, and what the palette's rows open.
 * A search narrows the choices as it is typed; ↑ ↓ move, Enter picks, a click picks, Escape closes; a choice Tab focuses is the one Enter picks. The choices every ticket holds are
 * ticked; picking an assignee or a label every one holds takes it away, else adds it. A name typed that the directory lacks is a choice
 * of its own for the owner and the assignees (people are names until identity exists: the store adds it). The picker only says what
 * was picked (`pick`); its host makes the edits, one operation per ticket.
 */
const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{
  field: PickField
  tickets: readonly TicketSummary[]
  labels?: readonly DispatchLabel[]
  milestones?: readonly DispatchMilestone[]
  agents?: readonly AgentInfo[]
}>()
const emit = defineEmits<{ pick: [value: string | number, adds: boolean] }>()

interface Choice {
  value: string | number
  label: string
  icon?: string
  /** A label's colour, as a dot in place of an icon. */
  dot?: string
  /** Every ticket holds it. */
  held: boolean
}

const query = ref('')
const cursor = ref(0)
const input = useTemplateRef<{ inputRef?: HTMLInputElement }>('input')

// The directory of people, read whole each time a picker of people opens, through a gate of its own: a slow reply never replaces a newer one.
const api = useDispatchApi()
const people = ref<string[]>([])
const gate = new DispatchOrder()
async function loadPeople() {
  try {
    await through(gate, 'people', () => api.people(), (list) => (people.value = list.map((p) => p.name)))
  } catch {
    // The names read before stay offered; a typed name still picks.
  }
}
watch(open, (o) => {
  if (!o) return
  query.value = ''
  cursor.value = 0
  if (props.field === 'owner' || props.field === 'assignees') void loadPeople()
  void nextTick(() => input.value?.inputRef?.focus())
})

const held = computed(() => new Set(pickHeld(props.field, props.tickets)))

/** An actor in words: a person's name, or an agent's from the catalog. */
function actorName(actor: string): string {
  const a = assigneeOf(actor)
  return a.kind === 'agent' ? (props.agents?.find((x) => x.id === a.name)?.name ?? a.name) : a.name
}

/** Every choice of the field, before the search. */
const choices = computed<Choice[]>(() => {
  const h = held.value
  switch (props.field) {
    case 'status':
      return TICKET_STATUSES.map((s) => ({ value: s, label: statusWord(s).label, icon: statusWord(s).icon, held: h.has(s) }))
    case 'priority':
      return [...PRIORITIES].reverse().map((p) => ({ value: p.value, label: p.label, icon: p.icon, held: h.has(p.value) }))
    case 'labels':
      return (props.labels ?? []).filter((l) => !l.deletedAt).map((l) => ({ value: l.id, label: l.name, dot: labelDot(l.color), held: h.has(l.id) }))
    case 'milestone': {
      const live = (props.milestones ?? []).filter((m) => !m.deletedAt)
      return [{ value: '', label: 'No milestone', icon: 'i-lucide-minus', held: h.has('') }, ...live.map((m) => ({ value: m.id, label: `${m.name}${m.target ? ` (due ${m.target})` : ''}${m.state === 'closed' ? ' · closed' : ''}`, icon: 'i-lucide-milestone', held: h.has(m.id) }))]
    }
    case 'owner': {
      const names = [...new Set([...people.value, ...props.tickets.map((t) => t.owner).filter(Boolean)])].sort((a, b) => a.localeCompare(b))
      return [{ value: '', label: 'No owner', icon: 'i-lucide-user-x', held: h.has('') }, ...names.map((n) => ({ value: n, label: n, icon: 'i-lucide-user', held: h.has(n) }))]
    }
    case 'assignees': {
      const persons = [...new Set([...people.value.map((n) => `person:${n}`), ...props.tickets.flatMap((t) => t.assignees.filter((a) => a.startsWith('person:')))])].sort((a, b) => a.localeCompare(b))
      const agents = (props.agents ?? []).map((a) => `agent:${a.id}`)
      for (const a of props.tickets.flatMap((t) => t.assignees)) if (a.startsWith('agent:') && !agents.includes(a)) agents.push(a)
      return [
        ...persons.map((a) => ({ value: a, label: actorName(a), icon: 'i-lucide-user', held: h.has(a) })),
        ...agents.map((a) => ({ value: a, label: actorName(a), icon: 'i-lucide-bot', held: h.has(a) })),
      ]
    }
  }
  return []
})

/**
 * The choices the search leaves, and a name typed that no choice is, offered as one (the owner, the assignees): a person no choice is,
 * by value (typedPick), so an agent whose name is the name typed leaves the person offered.
 */
const shown = computed<Choice[]>(() => {
  const q = query.value.trim().toLowerCase()
  const out = q ? choices.value.filter((c) => c.label.toLowerCase().includes(q)) : choices.value
  if (props.field === 'owner' || props.field === 'assignees') {
    const value = typedPick(props.field, query.value, choices.value.map((c) => String(c.value)))
    const name = query.value.trim().slice(0, 60)
    if (value !== null) out.push(props.field === 'owner' ? { value, label: `Make ${name} the owner`, icon: 'i-lucide-user-plus', held: false } : { value, label: `Assign ${name}`, icon: 'i-lucide-user-plus', held: false })
  }
  return out
})
watch(shown, (s) => {
  if (cursor.value >= s.length) cursor.value = Math.max(0, s.length - 1)
})

const title = computed(() => `${PICK_WORDS[props.field].title} of ${targetWords(props.tickets.map((t) => t.key))}`)
const placeholder = computed(() => (props.field === 'owner' || props.field === 'assignees' ? 'A name…' : `${PICK_WORDS[props.field].title}…`))

function choose(c: Choice | undefined) {
  if (!c || !props.tickets.length) return
  const adds = props.field === 'assignees' || props.field === 'labels' ? pickAdds(props.field, props.tickets, String(c.value)) : true
  open.value = false
  emit('pick', c.value, adds)
}

const list = useTemplateRef<HTMLElement>('list')
function onKey(e: KeyboardEvent) {
  if (e.isComposing) return
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault()
    const n = shown.value.length
    if (!n) return
    cursor.value = (cursor.value + (e.key === 'ArrowDown' ? 1 : -1) + n) % n
    void nextTick(() => list.value?.querySelector('[data-pick-cursor]')?.scrollIntoView({ block: 'nearest' }))
  } else if (e.key === 'Enter') {
    e.preventDefault()
    choose(shown.value[cursor.value])
  }
}
</script>

<template>
  <UModal v-model:open="open" :title="title" :description="`Pick the ${PICK_WORDS[field].title.toLowerCase()}: one change per ticket.`" :ui="{ content: 'sm:max-w-md sm:top-[12vh] sm:translate-y-0' }">
    <template #content>
      <div class="flex max-h-[70vh] min-h-0 flex-col" data-picker :data-picker-field="field" @keydown="onKey">
        <div class="flex items-center gap-2 border-b border-default px-3 py-2">
          <UIcon :name="PICK_WORDS[field].icon" class="size-4 flex-none text-muted" />
          <span class="flex-none truncate text-sm font-medium" data-picker-title>{{ title }}</span>
          <UInput ref="input" v-model="query" :placeholder="placeholder" variant="none" size="md" class="min-w-0 flex-1" :aria-label="placeholder" data-picker-input />
          <UKbd value="escape" size="sm" />
        </div>
        <div ref="list" role="listbox" :aria-label="title" class="min-h-0 flex-1 overflow-y-auto py-1" data-picker-list>
          <p v-if="!shown.length" class="px-4 py-5 text-center text-sm text-muted" data-picker-empty>{{ field === 'labels' ? "No label matches. A project's labels are made on its page." : 'Nothing matches.' }}</p>
          <button
            v-for="(c, i) in shown"
            :key="String(c.value)"
            type="button"
            role="option"
            :aria-selected="i === cursor ? 'true' : 'false'"
            class="flex w-full items-center gap-2.5 px-3 py-1.5 text-left text-sm outline-none"
            :class="i === cursor ? 'bg-elevated' : 'hover:bg-elevated/60'"
            :data-pick-value="String(c.value)"
            :data-pick-held="c.held ? '' : undefined"
            :data-pick-cursor="i === cursor ? '' : undefined"
            @click="choose(c)"
            @focus="cursor = i"
            @mousemove="cursor = i"
          >
            <span v-if="c.dot" class="mx-0.5 size-2.5 flex-none rounded-full" :class="c.dot" />
            <UIcon v-else-if="c.icon" :name="c.icon" class="size-4 flex-none text-muted" />
            <span class="min-w-0 flex-1 truncate" data-pick-label>{{ c.label }}</span>
            <UIcon v-if="c.held" name="i-lucide-check" class="size-4 flex-none text-primary" aria-label="held" />
          </button>
        </div>
      </div>
    </template>
  </UModal>
</template>
