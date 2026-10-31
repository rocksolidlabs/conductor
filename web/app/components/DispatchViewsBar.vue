<script setup lang="ts">
import type { DropdownMenuItem } from '@nuxt/ui'
import type { DispatchView, ViewKind, ViewReply } from '~/composables/useDispatch'
import { ApiError } from '~/composables/useApi'
import { dispatchFailureWords } from '~/composables/useDispatchOps'
import { viewsWatch } from '~/composables/useDispatchLive'
import { validProjectKey } from '~/utils/dispatch'
import { filterFromView, filterToView, sameFilter, type DispatchFilter, type FilterNames } from '~/utils/dispatchQuery'

/**
 * A project's saved views as chips (docs/plans/dispatch.md, "web/app"): the project's own and every project's, by name, each a list,
 * a board or a roadmap with its filter, sort and grouping; a chip applies its view (the filter written to the link, utils/dispatchQuery.ts,
 * and the view shown), and is marked while the page shows what it saved. Save keeps the filter shown as a view of this project or of
 * every project; a chip's menu renames or deletes it, each one operation on the revision shown. The views are read through the bar's
 * ordering gate as it opens, and again after the window on a `view` event of the project or of every project (`project` ""), and on a
 * snapshot, which forgets what the gate knew; a read that fails is asked again. Nothing polls. On a phone the chips scroll sideways.
 */
const props = defineProps<{
  project: string
  filter: DispatchFilter
  kind: ViewKind
  names: FilterNames
  /** Nothing of an archived project can be saved, renamed or deleted: the store would refuse it. */
  archived?: boolean
}>()
const emit = defineEmits<{ apply: [filter: DispatchFilter, kind: ViewKind] }>()

const { api, gate, read } = useDispatch()
const ops = useDispatchOps(gate)
const admin = useWorkbenchToken()

const views = ref<DispatchView[]>([])
const loaded = ref(false)

/** Reads the views the project's pages offer: its own and every project's. */
async function readViews(): Promise<void> {
  const key = props.project
  if (!validProjectKey(key)) return
  try {
    await read(
      `views:${key}`,
      () => api.views(key),
      (v) => {
        if (key !== props.project) return
        views.value = v
        loaded.value = true
      },
    )
  } catch (e) {
    // A project gone is the page's to say; reading again does not change it.
    if (!(e instanceof ApiError && e.status === 404)) throw e
  }
}

/** The views read through the live store, so that a read that fails is asked again after the window, doubled, until one answers. */
function load(): void {
  if (admin.hasToken.value) live.ask(readViews)
}
const live = useDispatchLive({ ...viewsWatch(() => props.project, readViews), reset: () => gate.reset() }, load)
watch(() => admin.token.value, load)
watch(
  () => props.project,
  (_, before) => {
    gate.drop(`views:${before}`)
    views.value = []
    loaded.value = false
    load()
  },
)

/** Whether the page shows what a view saved: its kind, and its filter, sort and grouping. */
function applied(v: DispatchView): boolean {
  return v.kind === props.kind && sameFilter(filterFromView(v.query, props.names), props.filter)
}
/** The ids of the views the page shows applied, worked out once per change of the views, the filter or the kind. */
const appliedIds = computed(() => new Set(views.value.filter(applied).map((v) => v.id)))
function apply(v: DispatchView) {
  emit('apply', filterFromView(v.query, props.names), v.kind)
}

const KIND_ICONS: Record<ViewKind, string> = { list: 'i-lucide-list', board: 'i-lucide-square-kanban', roadmap: 'i-lucide-map' }

// Save and rename: one dialog, a name and, for a new view, whose it is.
const dialogOpen = ref(false)
/** The view renamed; none while a new one is saved. */
const renaming = ref<DispatchView | null>(null)
const name = ref('')
const scope = ref<'project' | 'every'>('project')
const dialogError = ref('')
const submitting = ref(false)
/** The filter as a view keeps it: null when it names a label or a milestone the project lacks, which matches nothing. */
const query = computed(() => filterToView(props.filter, props.names))
/** A view of every project that names this project's labels or milestone: elsewhere they match nothing, which the dialog says. */
const narrowsByProject = computed(() => scope.value === 'every' && !!(props.filter.labels.length || props.filter.milestone))
const MAX_NAME = 60
const nameInvalid = computed(() => !name.value.trim() || Array.from(name.value.trim()).length > MAX_NAME)
const scopeItems = computed(() => [
  { label: `This project (${props.project})`, value: 'project' },
  { label: 'Every project', value: 'every', description: "Offered on every project's pages" },
])

// While a save or a rename is on its way the dialog stays as it is: it cannot be dismissed, cancelled or opened again, so the answer
// closes the opening it was made from and a draft of a newer one is never lost to it (DispatchNewTicket.vue does the same).
function openSave() {
  if (props.archived || !query.value || submitting.value) return
  renaming.value = null
  name.value = ''
  scope.value = 'project'
  dialogError.value = ''
  dialogOpen.value = true
}
function openRename(v: DispatchView) {
  if ((props.archived && v.projectId) || submitting.value) return
  renaming.value = v
  name.value = v.name
  dialogError.value = ''
  dialogOpen.value = true
}

async function submit() {
  if (nameInvalid.value || submitting.value) return
  submitting.value = true
  dialogError.value = ''
  try {
    const v = renaming.value
    const n = name.value.trim()
    if (v) {
      if (n !== v.name) await ops.run<ViewReply>((op) => api.editView(v.id, { ...op, name: n }), { thing: `view:${v.id}`, what: v.name, expectedRev: v.rev, raise: true })
    } else {
      const q = query.value
      if (!q) return
      await ops.run<ViewReply>((op) => api.createView({ ...op, ...(scope.value === 'project' ? { project: props.project } : {}), name: n, kind: props.kind, query: q }), { thing: `new-view:${props.project}`, what: 'Save the view', raise: true })
    }
    dialogOpen.value = false
  } catch (e) {
    dialogError.value = dispatchFailureWords(e)
  } finally {
    submitting.value = false
    // The bar shows what the store holds now; its event reads it again too.
    void readViews().catch(() => {})
  }
}

// Delete: asked first, since a view deleted has no undelete.
const deleting = ref<DispatchView | null>(null)
const deleteOpen = ref(false)
function askDelete(v: DispatchView) {
  if (props.archived && v.projectId) return
  deleting.value = v
  deleteOpen.value = true
}
async function confirmDelete() {
  const v = deleting.value
  if (!v) return
  deleteOpen.value = false
  await ops.run<ViewReply>((op) => api.deleteView(v.id, op), { thing: `view:${v.id}`, what: v.name, expectedRev: v.rev, done: `${v.name} deleted` })
  void readViews().catch(() => {})
}

function menu(v: DispatchView): DropdownMenuItem[][] {
  const locked = props.archived && !!v.projectId
  return [
    [{ label: 'Apply', icon: KIND_ICONS[v.kind], onSelect: () => apply(v) }],
    [
      { label: 'Rename…', icon: 'i-lucide-pencil', disabled: locked, onSelect: () => openRename(v) },
      { label: 'Delete…', icon: 'i-lucide-trash-2', color: 'error', disabled: locked, onSelect: () => askDelete(v) },
    ],
  ]
}

defineExpose({ views, openSave, apply, applied })
</script>

<template>
  <div class="flex min-w-0 items-center gap-1.5" data-views-bar :data-loaded="loaded ? '' : undefined">
    <div class="flex min-w-0 flex-1 items-center gap-1.5 overflow-x-auto py-0.5 [scrollbar-width:thin]" data-views-chips>
      <UIcon name="i-lucide-bookmark" class="size-4 flex-none text-muted" aria-hidden="true" />
      <span v-if="loaded && !views.length" class="flex-none text-xs text-dimmed">No saved views yet.</span>
      <div v-for="v in views" :key="v.id" class="flex flex-none items-center" :data-view-chip="v.id" :data-view-name="v.name" :data-view-kind="v.kind" :data-view-every="v.projectId ? undefined : ''" :data-active="appliedIds.has(v.id) ? '' : undefined">
        <UButton
          size="xs"
          :color="appliedIds.has(v.id) ? 'primary' : 'neutral'"
          :variant="appliedIds.has(v.id) ? 'soft' : 'outline'"
          :icon="v.projectId ? KIND_ICONS[v.kind] : 'i-lucide-globe'"
          :aria-pressed="appliedIds.has(v.id)"
          :title="v.projectId ? `A ${v.kind} of ${project}` : `A ${v.kind} of every project`"
          class="max-w-48 rounded-r-none"
          data-view-apply
          @click="apply(v)"
        >
          <span class="truncate">{{ v.name }}</span>
        </UButton>
        <UDropdownMenu :items="menu(v)" :content="{ align: 'start' }">
          <UButton size="xs" :color="appliedIds.has(v.id) ? 'primary' : 'neutral'" :variant="appliedIds.has(v.id) ? 'soft' : 'outline'" icon="i-lucide-chevron-down" class="-ml-px rounded-l-none" :aria-label="`${v.name}: its actions`" :data-view-menu="v.id" />
        </UDropdownMenu>
      </div>
    </div>
    <UButton
      size="xs"
      color="neutral"
      variant="ghost"
      icon="i-lucide-bookmark-plus"
      class="flex-none"
      :disabled="archived || !query"
      :title="archived ? 'The project is archived' : !query ? 'The filter names a label or a milestone this project lacks' : 'Save the filter as a view'"
      data-view-save
      @click="openSave"
    >
      <span class="hidden sm:inline">Save view</span>
    </UButton>
  </div>

  <UModal v-model:open="dialogOpen" :dismissible="!submitting" :close="{ disabled: submitting }" :title="renaming ? `Rename ${renaming.name}` : 'Save the view'" :description="renaming ? 'Its filter, sort and kind stay as they are.' : `The filter, sort and ${kind} shown, under a name.`" :ui="{ content: 'max-w-md' }">
    <template #body>
      <form class="flex flex-col gap-4" data-view-dialog @submit.prevent="submit">
        <UAlert v-if="dialogError" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="dialogError" data-view-error />
        <UFormField label="Name" name="name" :error="Array.from(name.trim()).length > MAX_NAME ? `At most ${MAX_NAME} characters` : undefined">
          <UInput v-model="name" placeholder="My open bugs" autofocus class="w-full" aria-label="The view's name" data-view-name-input />
        </UFormField>
        <UFormField v-if="!renaming" label="Offered on" name="scope">
          <URadioGroup v-model="scope" :items="scopeItems" data-view-scope />
        </UFormField>
        <p v-if="!renaming && narrowsByProject" class="text-xs text-muted">Its labels and milestone are {{ project }}'s: on another project's page they match nothing.</p>
        <button type="submit" class="hidden" />
      </form>
    </template>
    <template #footer>
      <div class="flex w-full justify-end gap-2">
        <UButton label="Cancel" color="neutral" variant="ghost" :disabled="submitting" @click="dialogOpen = false" />
        <UButton :label="renaming ? 'Rename' : 'Save'" :icon="renaming ? 'i-lucide-pencil' : 'i-lucide-bookmark-plus'" :loading="submitting" :disabled="nameInvalid" data-view-submit @click="submit" />
      </div>
    </template>
  </UModal>

  <UModal v-model:open="deleteOpen" :title="deleting ? `Delete ${deleting.name}?` : 'Delete the view?'" description="A saved view deleted is gone for every browser; its tickets are not touched." :ui="{ content: 'max-w-md' }">
    <template #footer>
      <div class="flex w-full justify-end gap-2" data-view-delete-dialog>
        <UButton label="Cancel" color="neutral" variant="ghost" @click="deleteOpen = false" />
        <UButton label="Delete" icon="i-lucide-trash-2" color="error" data-view-delete-confirm @click="confirmDelete" />
      </div>
    </template>
  </UModal>
</template>
