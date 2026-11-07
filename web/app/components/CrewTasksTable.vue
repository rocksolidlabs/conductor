<script setup lang="ts">
import { OWNER_NONE, TASK_LIMITS, draftTask, ownerChoices, taskRowProblems, type DraftCheck, type DraftMember, type DraftPerson, type DraftTask, type TaskShape } from '~/utils/crews'

/**
 * The tasks of a managed crew (#334): who does what, after which results,
 * checked how. A row is a task: its id, its member, its owner (a person of
 * the crew, accountable for the outcome; none falls back to the crew's
 * acceptance owner, which the "none" item names), its kind, the tasks it
 * needs, its instructions, its checks (a popover). What makes each attempt's
 * worktree usable is the project manifest's, under the table
 * (CrewManifestEditor). The server's rules are shown as they would be
 * refused (taskRowProblems); the server still decides.
 */
const tasks = defineModel<DraftTask[]>({ default: () => [] })
const props = defineProps<{
  members: DraftMember[]
  /** The crew's people: a task's owner is one of them. */
  people?: DraftPerson[]
  isolation: 'none' | 'worktree'
  /** The ids of the project manifest's steps and probes (manifestIds): a task's new check is given none of them. */
  taken?: readonly string[]
}>()

const shape = computed<TaskShape>(() => ({ tasks: tasks.value, isolation: props.isolation, members: props.members, people: props.people }))

// Every cell keeps its place in the one-column-per-field layout of a wide
// table and moves to a card layout when the table is narrow. Full class
// names: Tailwind scans them.
const cols = 'grid-cols-[minmax(0,1fr)_minmax(0,1fr)_1.5rem] gap-x-2.5 gap-y-2 @min-[64rem]:grid-cols-[8rem_8rem_7rem_6.5rem_10rem_minmax(9rem,1fr)_7rem_1.5rem]'
const cell = {
  id: 'col-start-1 row-start-1 @min-[64rem]:col-start-auto @min-[64rem]:row-start-auto',
  member: 'col-start-2 row-start-1 @min-[64rem]:col-start-auto @min-[64rem]:row-start-auto',
  owner: 'col-start-2 row-start-4 @min-[64rem]:col-start-auto @min-[64rem]:row-start-auto',
  kind: 'col-start-1 row-start-2 @min-[64rem]:col-start-auto @min-[64rem]:row-start-auto',
  needs: 'col-start-2 row-start-2 @min-[64rem]:col-start-auto @min-[64rem]:row-start-auto',
  instructions: 'col-start-1 col-span-2 row-start-3 @min-[64rem]:col-start-auto @min-[64rem]:col-span-1 @min-[64rem]:row-start-auto',
  checks: 'col-start-1 row-start-4 @min-[64rem]:col-start-auto @min-[64rem]:row-start-auto',
  menu: 'col-start-3 row-start-1 @min-[64rem]:col-start-auto @min-[64rem]:row-start-auto',
}

// A member is picked by its key, so its tasks follow its name; a task whose member is gone shows the last name, as not a member.
const memberItems = computed(() => props.members.filter((m) => m.name).map((m) => ({ label: m.name, value: memberValue(m) })))
function memberValue(m: { name: string; key?: number }): string {
  return m.key === undefined ? m.name : `#${m.key}`
}
function memberOf(t: DraftTask): string {
  const m = t.memberKey === undefined ? undefined : props.members.find((x) => x.key === t.memberKey)
  return m ? memberValue(m) : t.member
}
function pickMember(i: number, value: string) {
  const m = value.startsWith('#') ? props.members.find((x) => x.key === Number(value.slice(1))) : undefined
  update(i, m ? { member: m.name, memberKey: m.key } : { member: value, memberKey: undefined })
}
// An owner is picked by the person's key, so the task follows their name; one whose person is gone stays shown, as not a person, and
// the select stays enabled while it does (ownerChoices).
const owners = computed(() => tasks.value.map((t) => ownerChoices(t, props.people)))
function pickOwner(i: number, value: string) {
  if (value === OWNER_NONE) return update(i, { owner: undefined, ownerKey: undefined })
  const p = value.startsWith('#') ? props.people?.find((x) => `#${x.key}` === value) : undefined
  if (p) update(i, { owner: p.name, ownerKey: p.key })
}

const kindItems = [
  { label: 'work', value: 'work', icon: 'i-lucide-hammer' },
  { label: 'review', value: 'review', icon: 'i-lucide-search-check' },
]

function membersFor(t: DraftTask) {
  if (!t.member || memberOf(t) !== t.member || props.members.some((m) => m.key === undefined && m.name === t.member)) return memberItems.value
  return [...memberItems.value, { label: `${t.member} (not a member)`, value: t.member }]
}

/** The other tasks, by key: a rename never touches a need. */
function needsFor(t: DraftTask) {
  return tasks.value.filter((x) => x.key !== t.key).map((x) => ({ label: x.id || '(no id yet)', value: x.key }))
}

function who(t: DraftTask, i: number): string {
  return t.id || `task ${i + 1}`
}

function update(i: number, patch: Partial<DraftTask>) {
  tasks.value = tasks.value.map((t, j) => (j === i ? { ...t, ...patch } : t))
}

function setKind(i: number, kind: 'work' | 'review') {
  // A review has no checks: they go with the switch.
  update(i, kind === 'review' ? { kind, checks: undefined } : { kind })
}

/** Removes a task; the tasks that needed it no longer do. */
function remove(i: number) {
  const gone = tasks.value[i]?.key
  tasks.value = tasks.value.filter((_, j) => j !== i).map((t) => (gone !== undefined && t.needKeys.includes(gone) ? { ...t, needKeys: t.needKeys.filter((k) => k !== gone) } : t))
}

function setChecks(i: number, checks: DraftCheck[]) {
  update(i, { checks: checks.length ? checks : undefined })
}

function move(i: number, delta: number) {
  const to = i + delta
  if (to < 0 || to >= tasks.value.length) return
  const next = tasks.value.slice()
  const [t] = next.splice(i, 1)
  next.splice(to, 0, t!)
  tasks.value = next
}

function add() {
  const member = props.members.find((m) => m.name) ?? ''
  tasks.value = [...tasks.value, draftTask(member, tasks.value.map((t) => t.id))]
}

function problem(t: DraftTask): string {
  return taskRowProblems(t, shape.value)[0]?.replace(/^(Task [^:,]*|A task)[:,] ?/, '') ?? ''
}

function checksWords(t: DraftTask): string {
  const n = t.checks?.length ?? 0
  if (t.kind === 'review') return 'verdict'
  return n ? `${n} ${n === 1 ? 'check' : 'checks'}` : 'no checks'
}

function menu(t: DraftTask, i: number) {
  return [
    [
      { label: 'Move up', icon: 'i-lucide-arrow-up', disabled: i === 0, onSelect: () => move(i, -1) },
      { label: 'Move down', icon: 'i-lucide-arrow-down', disabled: i === tasks.value.length - 1, onSelect: () => move(i, 1) },
    ],
    [{ label: 'Remove task', icon: 'i-lucide-trash-2', color: 'error' as const, onSelect: () => remove(i) }],
  ]
}

const focused = ref<number | null>(null)
</script>

<template>
  <div class="flex flex-col gap-3" data-crew-tasks-section>
    <div class="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
      <span class="text-sm font-semibold text-highlighted">Tasks</span>
      <span class="min-w-0 text-[12.5px] text-muted">each member does its tasks as their needs are met, in a worktree of its own; the checks decide a result</span>
    </div>
    <div class="@container overflow-hidden rounded-lg ring ring-default" data-crew-tasks>
      <div class="hidden border-b border-default bg-muted px-4 py-2.5 text-[11px] font-semibold uppercase tracking-wider text-muted @min-[64rem]:grid" :class="cols" aria-hidden="true">
        <span>Id</span>
        <span>Member</span>
        <span>Owner</span>
        <span>Kind</span>
        <span>Needs</span>
        <span>Instructions</span>
        <span>Checks</span>
        <span />
      </div>

      <div v-for="(t, i) in tasks" :key="t.key" class="flex flex-col gap-2 border-b border-default px-4 py-2.5 last:border-b-0" data-crew-task :data-task-id="t.id">
        <div class="grid items-start" :class="cols">
          <UInput :class="cell.id" :model-value="t.id" placeholder="id" maxlength="40" autocapitalize="off" spellcheck="false" :aria-label="`Id of ${who(t, i)}`" :ui="{ base: 'font-mono font-medium' }" class="w-full" @update:model-value="update(i, { id: String($event) })" />
          <USelect :class="cell.member" :model-value="memberOf(t)" :items="membersFor(t)" placeholder="Member" :aria-label="`Member of ${who(t, i)}`" class="w-full" data-task-member @update:model-value="pickMember(i, String($event))" />
          <USelect
            :class="cell.owner"
            :model-value="owners[i]!.value"
            :items="owners[i]!.items"
            :disabled="owners[i]!.disabled"
            :title="owners[i]!.disabled ? 'Add people to the crew to name an owner' : owners[i]!.items.find((o) => o.value === owners[i]!.value)?.label"
            :ui="{ content: 'w-max min-w-(--reka-select-trigger-width) max-w-[calc(100vw-2rem)]' }"
            :aria-label="`Owner of ${who(t, i)}`"
            class="w-full"
            data-task-owner
            @update:model-value="pickOwner(i, String($event))"
          />
          <USelect :class="cell.kind" :model-value="t.kind" :items="kindItems" :aria-label="`Kind of ${who(t, i)}`" class="w-full" data-task-kind @update:model-value="setKind(i, $event as 'work' | 'review')" />
          <USelect :class="cell.needs" :model-value="t.needKeys" :items="needsFor(t)" multiple placeholder="nothing" :aria-label="`What ${who(t, i)} needs`" class="w-full" data-task-needs @update:model-value="update(i, { needKeys: $event as number[] })" />
          <UTextarea
            :class="cell.instructions"
            :model-value="t.instructions"
            :rows="focused === i ? 4 : 1"
            :autoresize="focused === i"
            :maxrows="16"
            :maxlength="TASK_LIMITS.instructions"
            :placeholder="t.kind === 'review' ? 'What to look for; the result reviewed is in the task file' : 'What to do; the goal and the inputs are in the task file'"
            :aria-label="`Instructions for ${who(t, i)}`"
            class="w-full"
            :ui="{ base: focused === i ? 'resize-none' : 'resize-none overflow-hidden whitespace-nowrap text-ellipsis' }"
            @update:model-value="update(i, { instructions: String($event) })"
            @focus="focused = i"
            @blur="focused = focused === i ? null : focused"
          />
          <div :class="cell.checks" class="flex h-8 items-center">
            <UPopover v-if="t.kind !== 'review'" :content="{ align: 'start' }">
              <UButton :label="checksWords(t)" icon="i-lucide-shield-check" color="neutral" variant="outline" size="xs" :aria-label="`Checks of ${who(t, i)}`" data-task-checks />
              <template #content>
                <div class="w-[32rem] max-w-[calc(100vw-2rem)] p-3">
                  <p class="mb-2 text-xs text-muted">Run against the result's commit, in a checkout of their own; every one must pass. Argv, never a shell line.</p>
                  <CrewCheckList :model-value="t.checks ?? []" what="check" :taken="taken" @update:model-value="setChecks(i, $event)" />
                </div>
              </template>
            </UPopover>
            <span v-else class="text-xs text-muted">{{ checksWords(t) }}</span>
          </div>
          <div :class="cell.menu" class="flex h-8 items-center justify-end">
            <UDropdownMenu :items="menu(t, i)" :content="{ align: 'end' }">
              <UButton icon="i-lucide-ellipsis" color="neutral" variant="ghost" size="xs" :aria-label="`${who(t, i)}: more`" data-task-menu />
            </UDropdownMenu>
          </div>
        </div>
        <span v-if="problem(t)" class="text-xs leading-snug text-error" data-task-error>{{ problem(t) }}</span>
      </div>

      <div v-if="!tasks.length" class="px-4 py-3 text-sm text-muted">No tasks yet: add one per thing a member delivers.</div>
    </div>
    <div class="flex flex-wrap gap-x-4 gap-y-1">
      <UButton label="Add task" icon="i-lucide-plus" color="secondary" variant="link" size="sm" class="px-0" :disabled="!members.length || tasks.length >= TASK_LIMITS.tasks" data-add-task @click="add" />
    </div>
  </div>
</template>
