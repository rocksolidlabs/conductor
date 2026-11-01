<script setup lang="ts">
import type { DispatchLabel, DispatchMilestone, LabelColor, LabelReply, MilestoneEdit, MilestoneReply, ProjectRead } from '~/composables/useDispatch'
import type { DispatchOrder } from '~/utils/dispatchOrder'
import { LABEL_COLOR_CHOICES, labelDot } from '~/utils/dispatch'

/**
 * A project's labels and milestones (docs/plans/dispatch.md, PR-10), managed from its page: a label made, renamed, recoloured with one
 * of the twelve names the UI maps to the brand's scales (never a value of the client's own) and deleted; a milestone made, renamed,
 * given a target date and a description, closed and opened again, and deleted. Each change is one operation on the revision shown
 * (useDispatchOps); a delete leaves every ticket that had the label or the milestone, which the open pages of those tickets see by the
 * project's events. The page reads the project again once an operation is done (`changed`) and on every event of the project. A rename
 * and a milestone's edit are made against what their editor opened on: only the fields changed from it are sent, on its revision, so
 * another person's edit of a field left alone is kept; one of a field edited here is said, the draft kept, and a second Save replaces it.
 * Their editors stay open while a save goes: its answer closes the editor it was made from only when nothing more was typed there, and
 * otherwise leaves the newer text, made against what was saved at the revision its receipt names (an editor opened since is left be).
 * One save goes at a time from an editor: its Save is off, and Enter sends nothing, until the one on its way is answered, so a second
 * never judges what is typed against what the first has not yet committed.
 */
const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ project: string; read: ProjectRead; gate: DispatchOrder; archived: boolean }>()
const emit = defineEmits<{ changed: [] }>()

const api = useDispatchApi()
const ops = useDispatchOps(props.gate)

const labels = computed(() => [...props.read.labels].sort((a, b) => a.name.localeCompare(b.name)))
const milestones = computed(() => {
  const order = (m: DispatchMilestone) => (m.state === 'closed' ? 2 : m.target ? 0 : 1)
  return [...props.read.milestones].sort((a, b) => order(a) - order(b) || a.target.localeCompare(b.target) || a.name.localeCompare(b.name))
})
const colorItems = LABEL_COLOR_CHOICES.map((c) => ({ label: c.label, value: c.value, dot: c.dot }))

/** Each operation answers with its reply once done (null when not); the page reads the project again after one that was. */
async function done<R>(p: Promise<R | null>): Promise<R | null> {
  const r = await p
  if (r) emit('changed')
  return r
}

// ---------------------------------------------------------------------------
// The labels

const newLabel = ref('')
const newColor = ref<LabelColor>('blue')
async function makeLabel() {
  const name = newLabel.value.trim()
  if (!name || props.archived) return
  const color = newColor.value
  if (await done(ops.run<LabelReply>((op) => api.createLabel(props.project, { ...op, name, color }), { thing: `label:new:${props.project}`, what: `The label ${name}` }))) newLabel.value = ''
}

const renaming = ref('')
const renameDraft = ref('')
/** Counts the renames opened: a save's answer acts on the rename only while it is the opening the save was made in. */
let renameOpenings = 0
const renameOpened = ref(0)
/** The label as the rename opened on it, or as another person's rename left it once said: what the rename is made against. */
const renameBase = ref({ name: '', rev: 0 })
/** Another person's name for the label, given while this one was typed: said until the rename closes. */
const renameTheirs = ref('')
/** The rename opening whose save is on its way (0: none): it sends nothing more until that one is answered. */
const renameSaving = ref(0)
function startRename(l: DispatchLabel) {
  renaming.value = l.id
  renameOpened.value = ++renameOpenings
  renameDraft.value = l.name
  renameBase.value = { name: l.name, rev: l.rev }
  renameTheirs.value = ''
}
async function rename(l: DispatchLabel) {
  if (renameSaving.value && renameSaving.value === renameOpened.value) return
  const name = renameDraft.value.trim()
  const base = renameBase.value
  if (l.rev !== base.rev) {
    // Changed since the rename opened: another name is said and not replaced unseen; a recolour is no matter to it.
    renameBase.value = { name: l.name, rev: l.rev }
    if (l.name !== base.name) {
      renameTheirs.value = l.name
      return
    }
  }
  if (!name || name === l.name) {
    renaming.value = ''
    return
  }
  const opened = renameOpened.value
  const typed = renameDraft.value
  renameSaving.value = opened
  let reply: LabelReply | null
  try {
    reply = await done(ops.run<LabelReply>((op) => api.editLabel(props.project, l.id, { ...op, name }), { thing: `label:${l.id}`, what: `The label ${l.name}`, expectedRev: l.rev }))
  } finally {
    if (renameSaving.value === opened) renameSaving.value = 0
  }
  if (!reply || renaming.value !== l.id || renameOpened.value !== opened) return
  if (renameDraft.value === typed) {
    renaming.value = ''
    return
  }
  // Typed on while it was saved: the newer name stays, made against the one saved.
  renameBase.value = { name, rev: reply.receipt.rev ?? renameBase.value.rev }
  renameTheirs.value = ''
}
function recolor(l: DispatchLabel, color: unknown) {
  if (typeof color !== 'string' || color === l.color) return
  void done(ops.run<LabelReply>((op) => api.editLabel(props.project, l.id, { ...op, color: color as LabelColor }), { thing: `label:${l.id}`, what: `The label ${l.name}`, expectedRev: l.rev }))
}

const confirming = ref('')
function deleteLabel(l: DispatchLabel) {
  confirming.value = ''
  void done(ops.run<LabelReply>((op) => api.deleteLabel(props.project, l.id, op), { thing: `label:${l.id}`, what: `The label ${l.name}`, expectedRev: l.rev, done: `The label ${l.name} deleted` }))
}

// ---------------------------------------------------------------------------
// The milestones

const newMilestone = ref('')
const newTarget = ref('')
async function makeMilestone() {
  const name = newMilestone.value.trim()
  if (!name || props.archived) return
  const target = newTarget.value
  if (await done(ops.run<MilestoneReply>((op) => api.createMilestone(props.project, { ...op, name, ...(target ? { target } : {}) }), { thing: `milestone:new:${props.project}`, what: `The milestone ${name}` }))) {
    newMilestone.value = ''
    newTarget.value = ''
  }
}

function editMilestone(m: DispatchMilestone, change: MilestoneEdit): Promise<MilestoneReply | null> {
  return done(ops.run<MilestoneReply>((op) => api.editMilestone(props.project, m.id, { ...op, ...change }), { thing: `milestone:${m.id}`, what: `The milestone ${m.name}`, expectedRev: m.rev }))
}

/** The milestone whose name and description are being edited, and the drafts. */
const editingMilestone = ref('')
/** Counts the milestone editors opened: a save's answer acts on the editor only while it is the opening the save was made in. */
let milestoneOpenings = 0
const milestoneOpened = ref(0)
const milestoneName = ref('')
const milestoneDescription = ref('')
/** The milestone as its editor opened on it, or as another person's edit left it once taken in: what a save is made against. */
const milestoneBase = ref({ name: '', description: '', rev: 0 })
/** Another person's edit of a field edited here, made meanwhile: said until the editor closes. */
const milestoneTheirs = ref<{ name?: string; description?: string } | null>(null)
/** The milestone editor's opening whose save is on its way (0: none): it sends nothing more until that one is answered. */
const milestoneSaving = ref(0)
function startMilestone(m: DispatchMilestone) {
  editingMilestone.value = m.id
  milestoneOpened.value = ++milestoneOpenings
  milestoneName.value = m.name
  milestoneDescription.value = m.description
  milestoneBase.value = { name: m.name, description: m.description, rev: m.rev }
  milestoneTheirs.value = null
}
/**
 * Saves the fields changed from what the editor opened on, on its revision. When another person's edit came meanwhile, a field left
 * alone here takes theirs (its draft too), and one edited here that they changed as well is said and not sent: the next Save replaces
 * it, knowingly.
 */
async function saveMilestone(m: DispatchMilestone) {
  if (milestoneSaving.value && milestoneSaving.value === milestoneOpened.value) return
  const base = milestoneBase.value
  const name = milestoneName.value.trim()
  const nameEdited = !!name && name !== base.name
  const descriptionEdited = milestoneDescription.value !== base.description
  if (m.rev !== base.rev) {
    const theirs: { name?: string; description?: string } = {}
    if (m.name !== base.name) {
      if (nameEdited) theirs.name = m.name
      else milestoneName.value = m.name
    }
    if (m.description !== base.description) {
      if (descriptionEdited) theirs.description = m.description
      else milestoneDescription.value = m.description
    }
    milestoneBase.value = { name: m.name, description: m.description, rev: m.rev }
    if (Object.keys(theirs).length) {
      milestoneTheirs.value = theirs
      return
    }
  }
  const change: MilestoneEdit = {}
  if (nameEdited && name !== m.name) change.name = name
  if (descriptionEdited && milestoneDescription.value !== m.description) change.description = milestoneDescription.value
  if (!Object.keys(change).length) {
    editingMilestone.value = ''
    return
  }
  const opened = milestoneOpened.value
  const typed = { name: milestoneName.value, description: milestoneDescription.value }
  const saved = { name: change.name ?? milestoneBase.value.name, description: change.description ?? milestoneBase.value.description }
  milestoneSaving.value = opened
  let reply: MilestoneReply | null
  try {
    reply = await editMilestone(m, change)
  } finally {
    if (milestoneSaving.value === opened) milestoneSaving.value = 0
  }
  if (!reply || editingMilestone.value !== m.id || milestoneOpened.value !== opened) return
  if (milestoneName.value === typed.name && milestoneDescription.value === typed.description) {
    editingMilestone.value = ''
    return
  }
  // Typed on while it was saved: the newer text stays, made against what was saved.
  milestoneBase.value = { ...saved, rev: reply.receipt.rev ?? milestoneBase.value.rev }
  milestoneTheirs.value = null
}
function setTarget(m: DispatchMilestone, target: string) {
  if (target !== m.target) void editMilestone(m, { target })
}
function deleteMilestone(m: DispatchMilestone) {
  confirming.value = ''
  void done(ops.run<MilestoneReply>((op) => api.deleteMilestone(props.project, m.id, op), { thing: `milestone:${m.id}`, what: `The milestone ${m.name}`, expectedRev: m.rev, done: `The milestone ${m.name} deleted` }))
}

/** A milestone's description's bound (the store's), in bytes. */
const MAX_MILESTONE_DESCRIPTION = 4 << 10
const milestoneOver = computed(() => new TextEncoder().encode(milestoneDescription.value).length > MAX_MILESTONE_DESCRIPTION)
</script>

<template>
  <USlideover v-model:open="open" :title="`${project}: labels and milestones`" description="What its tickets are labelled with, and the milestones they are planned for." :ui="{ content: 'max-w-lg' }">
    <template #body>
      <div class="flex flex-col gap-8" data-project-settings>
        <!-- The labels. -->
        <section class="flex flex-col gap-3" data-project-labels :data-count="labels.length">
          <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">Labels</h3>
          <p v-if="!labels.length" class="text-sm text-dimmed">None yet.</p>
          <ul class="flex flex-col gap-1.5">
            <li v-for="l in labels" :key="l.id" class="flex flex-col gap-1.5" :data-project-label="l.name" :data-label-color="l.color" :data-label-rev="l.rev">
              <div class="flex min-w-0 items-center gap-2">
                <USelect :model-value="l.color" :items="colorItems" :disabled="archived" size="xs" class="w-28 flex-none" :aria-label="`The colour of ${l.name}`" data-label-color-pick @update:model-value="(c: unknown) => recolor(l, c)">
                  <template #leading><span class="size-2.5 rounded-full" :class="labelDot(l.color)" /></template>
                  <template #item-leading="{ item }"><span class="size-2.5 rounded-full" :class="item.dot" /></template>
                </USelect>
                <form v-if="renaming === l.id" class="flex min-w-0 flex-1 items-center gap-1.5" @submit.prevent="rename(l)">
                  <UInput v-model="renameDraft" size="xs" maxlength="40" autofocus class="min-w-0 flex-1" aria-label="The label's name" data-label-rename-input @keydown.escape.stop="renaming = ''" />
                  <UButton type="submit" label="Save" size="xs" :loading="renameSaving === renameOpened" :disabled="renameSaving === renameOpened" data-label-rename-save />
                </form>
                <span v-else class="min-w-0 flex-1 truncate text-sm" data-label-name>{{ l.name }}</span>
                <template v-if="!archived && renaming !== l.id">
                  <UButton icon="i-lucide-pencil" size="xs" color="neutral" variant="ghost" :aria-label="`Rename ${l.name}`" data-label-rename @click="startRename(l)" />
                  <UButton icon="i-lucide-trash-2" size="xs" color="neutral" variant="ghost" :aria-label="`Delete ${l.name}`" data-label-delete @click="confirming = l.id" />
                </template>
              </div>
              <p v-if="renaming === l.id && renameTheirs" class="text-xs text-warning" data-label-theirs>It was renamed {{ renameTheirs }} while you typed: Save again to replace that with yours, or Escape to keep it.</p>
              <div v-if="confirming === l.id" class="flex flex-wrap items-center gap-2 text-sm" data-label-confirm>
                <span class="text-muted">Delete {{ l.name }}? Every ticket that has it loses it.</span>
                <UButton label="Delete" color="error" variant="soft" size="xs" data-label-delete-confirm @click="deleteLabel(l)" />
                <UButton label="Keep it" color="neutral" variant="ghost" size="xs" @click="confirming = ''" />
              </div>
            </li>
          </ul>
          <form v-if="!archived" class="flex items-center gap-2" data-label-new @submit.prevent="makeLabel">
            <USelect v-model="newColor" :items="colorItems" size="sm" class="w-28 flex-none" aria-label="The new label's colour" data-label-new-color>
              <template #leading><span class="size-2.5 rounded-full" :class="labelDot(newColor)" /></template>
              <template #item-leading="{ item }"><span class="size-2.5 rounded-full" :class="item.dot" /></template>
            </USelect>
            <UInput v-model="newLabel" placeholder="A new label…" maxlength="40" size="sm" class="min-w-0 flex-1" aria-label="The new label's name" data-label-new-name />
            <UButton type="submit" label="Make" icon="i-lucide-plus" size="sm" :disabled="!newLabel.trim()" data-label-new-submit />
          </form>
        </section>

        <!-- The milestones. -->
        <section class="flex flex-col gap-3" data-project-milestones :data-count="milestones.length">
          <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">Milestones</h3>
          <p v-if="!milestones.length" class="text-sm text-dimmed">None yet.</p>
          <ul class="flex flex-col gap-3">
            <li v-for="m in milestones" :key="m.id" class="flex flex-col gap-2 rounded-md p-3 ring ring-default" :data-project-milestone="m.name" :data-milestone-state="m.state" :data-milestone-rev="m.rev">
              <div class="flex min-w-0 flex-wrap items-center gap-2">
                <UIcon :name="m.state === 'closed' ? 'i-lucide-milestone' : 'i-lucide-flag'" class="size-4 flex-none text-muted" />
                <span class="min-w-0 flex-1 truncate text-sm font-medium" :class="m.state === 'closed' ? 'text-muted line-through' : 'text-highlighted'" data-milestone-name>{{ m.name }}</span>
                <UInput type="date" :model-value="m.target" :disabled="archived" size="xs" class="w-36" :aria-label="`The target of ${m.name}`" data-milestone-target @change="(e: Event) => setTarget(m, (e.target as HTMLInputElement).value)" />
              </div>
              <p v-if="m.description && editingMilestone !== m.id" class="whitespace-pre-wrap text-xs text-muted">{{ m.description }}</p>
              <form v-if="editingMilestone === m.id" class="flex flex-col gap-2" @submit.prevent="saveMilestone(m)">
                <div v-if="milestoneTheirs" class="flex flex-col gap-1 rounded-md p-2 text-xs text-warning ring ring-warning/40" data-milestone-theirs>
                  <span>It was changed while you edited. Save again to replace it with yours, or Cancel to keep it.</span>
                  <span v-if="milestoneTheirs.name !== undefined" data-milestone-theirs-name>Its name now: {{ milestoneTheirs.name }}</span>
                  <span v-if="milestoneTheirs.description !== undefined" class="whitespace-pre-wrap" data-milestone-theirs-description>Its description now: {{ milestoneTheirs.description || '(none)' }}</span>
                </div>
                <UInput v-model="milestoneName" size="sm" maxlength="60" aria-label="The milestone's name" data-milestone-name-input />
                <UTextarea v-model="milestoneDescription" :rows="3" autoresize size="sm" placeholder="What it is for…" aria-label="The milestone's description" data-milestone-description-input />
                <div class="flex items-center gap-2">
                  <span v-if="milestoneOver" class="text-xs text-error">More than the 4 KiB a milestone's description may hold</span>
                  <div class="flex-1" />
                  <UButton label="Cancel" color="neutral" variant="ghost" size="xs" @click="editingMilestone = ''" />
                  <UButton type="submit" label="Save" size="xs" :loading="milestoneSaving === milestoneOpened" :disabled="milestoneOver || !milestoneName.trim() || milestoneSaving === milestoneOpened" data-milestone-save />
                </div>
              </form>
              <div v-else-if="!archived" class="flex flex-wrap items-center gap-1">
                <UButton label="Edit" icon="i-lucide-pencil" size="xs" color="neutral" variant="ghost" data-milestone-edit @click="startMilestone(m)" />
                <UButton :label="m.state === 'closed' ? 'Open again' : 'Close'" :icon="m.state === 'closed' ? 'i-lucide-rotate-ccw' : 'i-lucide-check'" size="xs" color="neutral" variant="ghost" data-milestone-state-toggle @click="editMilestone(m, { state: m.state === 'closed' ? 'open' : 'closed' })" />
                <UButton label="Delete" icon="i-lucide-trash-2" size="xs" color="neutral" variant="ghost" data-milestone-delete @click="confirming = m.id" />
              </div>
              <div v-if="confirming === m.id" class="flex flex-wrap items-center gap-2 text-sm" data-milestone-confirm>
                <span class="text-muted">Delete {{ m.name }}? Every ticket planned for it is left with none.</span>
                <UButton label="Delete" color="error" variant="soft" size="xs" data-milestone-delete-confirm @click="deleteMilestone(m)" />
                <UButton label="Keep it" color="neutral" variant="ghost" size="xs" @click="confirming = ''" />
              </div>
            </li>
          </ul>
          <form v-if="!archived" class="flex flex-wrap items-center gap-2" data-milestone-new @submit.prevent="makeMilestone">
            <UInput v-model="newMilestone" placeholder="A new milestone…" maxlength="60" size="sm" class="min-w-0 flex-1" aria-label="The new milestone's name" data-milestone-new-name />
            <UInput v-model="newTarget" type="date" size="sm" class="w-36" aria-label="The new milestone's target" data-milestone-new-target />
            <UButton type="submit" label="Make" icon="i-lucide-plus" size="sm" :disabled="!newMilestone.trim()" data-milestone-new-submit />
          </form>
        </section>
      </div>
    </template>
  </USlideover>
</template>
