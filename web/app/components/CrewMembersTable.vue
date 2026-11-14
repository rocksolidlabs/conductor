<script setup lang="ts">
import type { AgentInfo } from '~/composables/useSessions'
import { agentIcon } from '~/utils/agentIcons'
import { agentItem } from '~/utils/agents'
import { argsFrom, memberNameError, startFrom, startValue, type DraftMember } from '~/utils/crews'
import { startWords } from '~/utils/crewWords'
import type { AccountsDoc } from '~/composables/useAccounts'
import { modelProblem } from '~/utils/capacity'
import { AT_LAUNCH, memberAccountItems } from '~/utils/crewLaunch'

/**
 * The members of a crew, one row each: name, agent, first prompt and when it
 * starts (at launch, after another member is done, when you press Start).
 * A row's menu adds the extra arguments line, moves the row (the order is
 * for display only) and removes it. `single` shows one row alone, for
 * adding a member to a run; `others` are the names outside the table (the
 * run's members), which a row may not take and may start after; `people`
 * the run's people, whose names a row may not take (a person is never a
 * member) and never starts after. A member of an agent whose tool's login Conductor can move (the
 * adapters `accounts` lists) has a second line: its account (the default at launch, its default login or
 * an account added in Settings) and its model (empty for the agent's default).
 */
const members = defineModel<DraftMember[]>({ required: true })
const props = withDefaults(defineProps<{ agents: AgentInfo[]; others?: string[]; people?: string[]; single?: boolean; host?: string; managed?: boolean; accounts?: AccountsDoc | null }>(), {
  others: () => [],
  people: () => [],
  single: false,
  host: '',
  managed: false,
  accounts: null,
})
const emit = defineEmits<{ add: []; addFromSession: [] }>()

// Every cell keeps its place in the one-column-per-field layout of a wide
// table and moves to a card layout when the table is narrow (a container
// query, so the slideover gets it too). Full class names: Tailwind scans them.
const cell = {
  name: 'col-start-1 row-start-1 @min-[52rem]:col-start-auto @min-[52rem]:row-start-auto',
  agent: 'col-start-2 row-start-1 @min-[52rem]:col-start-auto @min-[52rem]:row-start-auto',
  prompt: 'col-start-1 col-span-2 row-start-2 @min-[52rem]:col-start-auto @min-[52rem]:col-span-1 @min-[52rem]:row-start-auto',
  start: 'col-start-1 col-span-2 row-start-3 @min-[52rem]:col-start-auto @min-[52rem]:col-span-1 @min-[52rem]:row-start-auto',
  menu: 'col-start-3 row-start-1 @min-[52rem]:col-start-auto @min-[52rem]:row-start-auto',
}
// A managed crew's members have no Starts column: their tasks start them.
const cols = computed(() =>
  props.managed
    ? 'grid-cols-[minmax(0,1fr)_minmax(0,1fr)_1.5rem] gap-x-2.5 gap-y-2 @min-[52rem]:grid-cols-[7rem_10.5rem_minmax(8rem,1fr)_1.5rem]'
    : 'grid-cols-[minmax(0,1fr)_minmax(0,1fr)_1.5rem] gap-x-2.5 gap-y-2 @min-[52rem]:grid-cols-[7rem_10.5rem_minmax(8rem,1fr)_13rem_1.5rem]',
)

/** The agent select: an agent not installed on the server (`host` names it) is marked, never disabled. */
const agentItems = computed(() => props.agents.map((a) => agentItem(a, props.host)))

/** The select's own icon: its agent's, or the mark of an agent not installed on the server. */
function selectIcon(id: string): string {
  const a = agentOf(id)
  return a ? agentItem(a, props.host).icon : agentIcon(undefined)
}

function agentOf(id: string): AgentInfo | undefined {
  return props.agents.find((a) => a.id === id)
}

/** The agent select's items for a member, with its agent listed even when the catalog no longer has it. */
function agentsFor(m: DraftMember) {
  if (!m.agentId || agentOf(m.agentId)) return agentItems.value
  return [...agentItems.value, { label: `${m.agentId} (not in the catalog)`, value: m.agentId, icon: 'i-lucide-circle-help' }]
}

/** A row's member in labels: its name, or its place while it has none. */
function who(m: DraftMember, i: number): string {
  return m.name || `member ${i + 1}`
}

function taken(m: DraftMember): string[] {
  return [...props.others, ...members.value.filter((x) => x.key !== m.key).map((x) => x.name)]
}

function nameError(m: DraftMember): string {
  return memberNameError(m.name, taken(m), props.people)
}

/** At launch, after any other member with a usable name is done, or when you press Start; each with its icon. */
function startItems(m: DraftMember) {
  const names = taken(m).filter((n) => n && !memberNameError(n))
  const current = startValue(m.start)
  const item = (value: string) => {
    const w = startWords(startFrom(value))
    return { label: w.text, value, icon: w.icon }
  }
  const items = [item('immediately'), ...names.map((n) => item(`after:${n}`)), item('manual')]
  // A member it started after that is gone or renamed badly stays listed, so the select shows what is saved.
  if (!items.some((i) => i.value === current)) items.splice(items.length - 1, 0, item(current))
  return items
}

function update(key: number, patch: Partial<DraftMember>) {
  members.value = members.value.map((m) => (m.key === key ? { ...m, ...patch } : m))
}

/** Renames a member; the members that start after it follow the new name. */
function rename(key: number, name: string) {
  const old = members.value.find((m) => m.key === key)?.name
  members.value = members.value.map((m) => {
    if (m.key === key) return { ...m, name }
    if (old && m.start.when === 'after' && m.start.member === old) return { ...m, start: { when: 'after', member: name } }
    return m
  })
}

function setAgent(key: number, agentId: string) {
  // An agent that takes no extra arguments is refused at launch with some: they go with the switch.
  const takesArgs = agentOf(agentId)?.allowArgs ?? true
  // An account is its tool's: another tool's agent starts from the default at launch, and one whose login is not moved names no model.
  const was = members.value.find((m) => m.key === key)
  const login = was && agentOf(was.agentId)?.adapter === agentOf(agentId)?.adapter ? {} : { account: undefined, model: memberAccountItems(props.accounts, agentOf(agentId)?.adapter).length ? was?.model : undefined }
  update(key, takesArgs ? { agentId, ...login } : { agentId, args: undefined, argsText: '', ...login })
}

/** A member's Account choice, [] for an agent whose login is not one Conductor moves: such a member names none. */
function accountItems(m: DraftMember) {
  return memberAccountItems(props.accounts, agentOf(m.agentId)?.adapter, m.account)
}

function setAccount(key: number, value: string) {
  update(key, { account: value === AT_LAUNCH ? undefined : value })
}

function setArgs(key: number, text: string) {
  update(key, { argsText: text, args: argsFrom(text) })
}

/** Removes a member; the members that started after it start at launch. */
function remove(key: number) {
  const gone = members.value.find((m) => m.key === key)?.name
  members.value = members.value
    .filter((m) => m.key !== key)
    .map((m) => (m.start.when === 'after' && m.start.member === gone ? { ...m, start: { when: 'immediately' } } : m))
}

function move(key: number, delta: number) {
  const from = members.value.findIndex((m) => m.key === key)
  const to = from + delta
  if (from < 0 || to < 0 || to >= members.value.length) return
  const next = members.value.slice()
  const [m] = next.splice(from, 1)
  next.splice(to, 0, m!)
  members.value = next
}

// The extra arguments line shows for a member that has some, or once asked for from the row's menu.
const argsShown = ref(new Set<number>())
function showsArgs(m: DraftMember): boolean {
  return props.single || !!m.argsText || argsShown.value.has(m.key)
}
function toggleArgs(m: DraftMember) {
  const next = new Set(argsShown.value)
  if (showsArgs(m) && !m.argsText) next.delete(m.key)
  else next.add(m.key)
  argsShown.value = next
}

function menu(m: DraftMember, i: number) {
  const takesArgs = agentOf(m.agentId)?.allowArgs !== false
  return [
    [{ label: showsArgs(m) && !m.argsText ? 'Hide extra arguments' : 'Extra arguments', icon: 'i-lucide-terminal', disabled: !takesArgs, onSelect: () => toggleArgs(m) }],
    [
      { label: 'Move up', icon: 'i-lucide-arrow-up', disabled: i === 0, onSelect: () => move(m.key, -1) },
      { label: 'Move down', icon: 'i-lucide-arrow-down', disabled: i === members.value.length - 1, onSelect: () => move(m.key, 1) },
    ],
    [{ label: 'Remove member', icon: 'i-lucide-trash-2', color: 'error' as const, onSelect: () => remove(m.key) }],
  ]
}

// Prompts stay one line until focused, then grow with their text.
const focusedPrompt = ref<number | null>(null)
</script>

<template>
  <div class="@container overflow-hidden rounded-lg ring ring-default" data-crew-members>
    <div class="hidden border-b border-default bg-muted px-4 py-2.5 text-[11px] font-semibold uppercase tracking-wider text-muted @min-[52rem]:grid" :class="cols" aria-hidden="true">
      <span>Name</span>
      <span>Agent</span>
      <span v-if="managed">Role <span class="font-normal normal-case tracking-normal">· written into each task file</span></span>
      <span v-else>First prompt <span class="font-normal normal-case tracking-normal">· $GOAL is the goal</span></span>
      <span v-if="!managed">Starts</span>
      <span />
    </div>

    <div v-for="(m, i) in members" :key="m.key" class="flex flex-col gap-2 border-b border-default px-4 py-2.5 last:border-b-0" data-crew-member>
      <div class="grid items-start" :class="cols">
        <div :class="cell.name" class="flex min-w-0 flex-col gap-1">
          <UInput
            :model-value="m.name"
            placeholder="name"
            maxlength="40"
            autocapitalize="off"
            spellcheck="false"
            :color="nameError(m) ? 'error' : undefined"
            :highlight="!!nameError(m)"
            :aria-invalid="!!nameError(m)"
            :aria-label="`Name of ${who(m, i)}`"
            :ui="{ base: 'font-mono font-medium' }"
            class="w-full"
            @update:model-value="rename(m.key, String($event))"
          />
          <span v-if="nameError(m)" class="text-xs leading-snug text-error" data-name-error>{{ nameError(m) }}</span>
        </div>

        <USelect
          :class="cell.agent"
          :model-value="m.agentId"
          :items="agentsFor(m)"
          :icon="selectIcon(m.agentId)"
          :ui="{ content: 'w-max min-w-(--reka-select-trigger-width) max-w-[calc(100vw-2rem)]' }"
          placeholder="Agent"
          :aria-label="`Agent for ${who(m, i)}`"
          class="w-full"
          @update:model-value="setAgent(m.key, String($event))"
        />

        <UTextarea
          :class="cell.prompt"
          :model-value="m.prompt"
          :rows="focusedPrompt === m.key ? 3 : 1"
          :autoresize="focusedPrompt === m.key"
          :maxrows="12"
          maxlength="4000"
          :placeholder="managed ? 'Its role, in a line or two' : agentOf(m.agentId)?.allowArgs === false ? 'Shell input (optional)' : 'What it does first; $GOAL is the goal'"
          :title="agentOf(m.agentId)?.allowArgs === false ? 'Typed into the shell as one line once it is ready (optional)' : 'Typed into the agent as one line once it is ready. $GOAL is the crew\'s goal.'"
          :aria-label="`First prompt for ${who(m, i)}`"
          class="w-full"
          :ui="{ base: focusedPrompt === m.key ? 'resize-none' : 'resize-none overflow-hidden whitespace-nowrap text-ellipsis' }"
          @update:model-value="update(m.key, { prompt: String($event) })"
          @focus="focusedPrompt = m.key"
          @blur="focusedPrompt = focusedPrompt === m.key ? null : focusedPrompt"
        />

        <USelect
          v-if="!managed"
          :class="cell.start"
          :model-value="startValue(m.start)"
          :items="startItems(m)"
          :icon="startWords(m.start).icon"
          :aria-label="`When ${who(m, i)} starts`"
          class="w-full"
          :ui="{ base: startWords(m.start).muted ? 'text-muted' : undefined }"
          data-member-start
          @update:model-value="update(m.key, { start: startFrom(String($event)) })"
        />

        <div :class="cell.menu" class="flex h-8 items-center justify-end">
          <UDropdownMenu v-if="!single" :items="menu(m, i)" :content="{ align: 'end' }">
            <UButton icon="i-lucide-ellipsis" color="neutral" variant="ghost" size="xs" :aria-label="`${who(m, i)}: more`" data-member-menu />
          </UDropdownMenu>
        </div>
      </div>

      <div v-if="accountItems(m).length" class="flex flex-wrap items-start gap-2 pl-0 @min-[52rem]:pl-[18.5rem]" data-member-login>
        <USelect
          :model-value="m.account || AT_LAUNCH"
          :items="accountItems(m)"
          icon="i-lucide-key-round"
          size="sm"
          :aria-label="`Account for ${who(m, i)}`"
          class="w-full max-w-64"
          data-member-account
          @update:model-value="setAccount(m.key, String($event))"
        />
        <div class="flex w-full max-w-64 flex-col gap-1">
          <UInput
            :model-value="m.model ?? ''"
            placeholder="the agent's default model"
            maxlength="100"
            autocapitalize="off"
            spellcheck="false"
            size="sm"
            :color="modelProblem(m.model ?? '') ? 'error' : undefined"
            :highlight="!!modelProblem(m.model ?? '')"
            :aria-label="`Model for ${who(m, i)}`"
            :ui="{ base: 'font-mono text-xs' }"
            data-member-model
            @update:model-value="update(m.key, { model: String($event) || undefined })"
          />
          <span v-if="modelProblem(m.model ?? '')" class="text-xs leading-snug text-error" data-member-model-error>{{ modelProblem(m.model ?? '') }}</span>
        </div>
      </div>

      <div v-if="showsArgs(m)" class="flex items-center gap-2 pl-0 @min-[52rem]:pl-[18.5rem]">
        <UInput
          :model-value="m.argsText"
          :placeholder="agentOf(m.agentId)?.allowArgs === false ? 'this agent takes no extra arguments' : 'extra arguments, as on the command line'"
          :disabled="agentOf(m.agentId)?.allowArgs === false"
          autocapitalize="off"
          spellcheck="false"
          icon="i-lucide-terminal"
          size="sm"
          :aria-label="`Extra arguments for ${who(m, i)}`"
          :ui="{ base: 'font-mono text-xs' }"
          class="w-full max-w-xl"
          data-member-args
          @update:model-value="setArgs(m.key, String($event))"
        />
      </div>
    </div>

    <p v-if="!single && !members.length" class="px-4 py-4 text-sm text-muted">No members yet. A crew needs at least one to launch.</p>

    <div v-if="!single" class="flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-default px-4 py-2">
      <UButton label="Add member" icon="i-lucide-plus" color="secondary" variant="link" size="sm" class="px-0" :disabled="!agents.length || members.length >= 12" data-add-member @click="emit('add')" />
      <UButton label="Add from a running session" icon="i-lucide-plus" color="secondary" variant="link" size="sm" class="px-0" :disabled="members.length >= 12" @click="emit('addFromSession')" />
      <span class="ml-auto text-xs text-muted">Each prompt is typed into its agent as one line once it is ready</span>
    </div>
  </div>
</template>
