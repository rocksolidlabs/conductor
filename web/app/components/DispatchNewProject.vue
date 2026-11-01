<script setup lang="ts">
import type { CloseRequirement, ProjectReply, Tracker } from '~/composables/useDispatch'
import type { DispatchOrder } from '~/utils/dispatchOrder'
import { CLOSE_REQUIREMENTS, PROJECT_KEY_PATTERN, closeRequiresOf } from '~/utils/dispatch'
import { dispatchFailureWords } from '~/composables/useDispatchOps'

/**
 * New project (docs/plans/dispatch.md, "web/app"): its key (CND: one upper-case letter, then one to five upper-case letters or digits,
 * chosen now and never changed, its tickets' keys CND-1, CND-2…), its name, the folder of its repository (DirInput, completed from the
 * server's allowed roots; kept as text, used when something launches from it), and its tracker: Dispatch itself, or a pointer at
 * another one by its URL; and its close policy (PR-11), what a ticket needs before it is done: the owner's acceptance, a delivery link,
 * both or neither, ticked as the store's default is (the owner's acceptance for a project of Dispatch's, nothing for one tracked
 * elsewhere, which its tracker decides) until the person ticks otherwise; `verified` is not offered. A refusal (a key taken) shows in
 * the dialog. While the project is made the
 * dialog stays: Escape, a click outside, its close button and Cancel do nothing until the store has answered, so that answer closes the
 * opening it was made from and opens the project's page, never closing a later opening, or leaving its draft for that page. The project
 * made is handed on by its key (`created`), its reply's project's, else its receipt's: a creation whose read after the commit failed is
 * answered with its receipt alone, and is made all the same.
 */
const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ gate: DispatchOrder }>()
const emit = defineEmits<{ created: [key: string] }>()

const api = useDispatchApi()
const ops = useDispatchOps(props.gate)

const key = ref('')
const name = ref('')
const cwd = ref('')
const tracker = ref<Tracker>('native')
const trackerUrl = ref('')
/** The close policy ticked, and whether the person ticked it: until then it follows the tracker's default. */
const closeRequires = ref<CloseRequirement[]>(['acceptance'])
const policyTouched = ref(false)
const error = ref('')
const submitting = ref(false)

watch(open, (o) => {
  if (!o) return
  key.value = ''
  name.value = ''
  cwd.value = ''
  tracker.value = 'native'
  trackerUrl.value = ''
  closeRequires.value = ['acceptance']
  policyTouched.value = false
  error.value = ''
})

/** The store's default for the tracker, until the person ticks a requirement. */
watch(tracker, (t) => {
  if (!policyTouched.value) closeRequires.value = t === 'native' ? ['acceptance'] : []
})
function tick(r: CloseRequirement, on: boolean | 'indeterminate') {
  policyTouched.value = true
  closeRequires.value = closeRequiresOf(on === true ? [...closeRequires.value, r] : closeRequires.value.filter((x) => x !== r))
}

/** The key as typed, in upper case: a key is never lower case. */
function onKey(v: string | number) {
  key.value = String(v).toUpperCase().replace(/\s+/g, '')
}

const keyProblem = computed(() => (key.value && !PROJECT_KEY_PATTERN.test(key.value) ? 'One letter, then one to five letters or digits: CND, API2' : ''))
const urlProblem = computed(() => (tracker.value === 'external' && trackerUrl.value.trim() && !/^https?:\/\/[^\s/]+/i.test(trackerUrl.value.trim()) ? 'An http or https URL' : ''))
const invalid = computed(() => !PROJECT_KEY_PATTERN.test(key.value) || !name.value.trim() || (tracker.value === 'external' && (!trackerUrl.value.trim() || !!urlProblem.value)))
const trackerItems = [
  { label: "Dispatch's own", value: 'native', description: 'Its tickets are kept here' },
  { label: 'Elsewhere', value: 'external', description: 'A pointer at the tracker that keeps them' },
]

async function submit() {
  if (invalid.value || submitting.value) return
  submitting.value = true
  error.value = ''
  try {
    const body = {
      key: key.value,
      name: name.value.trim(),
      ...(cwd.value.trim() ? { cwd: cwd.value.trim() } : {}),
      tracker: tracker.value,
      ...(tracker.value === 'external' ? { trackerUrl: trackerUrl.value.trim() } : {}),
      closeRequires: closeRequiresOf(closeRequires.value),
    }
    const reply = await ops.run<ProjectReply>((op) => api.createProject({ ...op, ...body }), { thing: `project:${key.value}`, what: 'New project', raise: true })
    // Made: by the key its reply's project has, else the one its receipt names.
    const made = reply?.project?.key || reply?.receipt.key
    if (made) emit('created', made)
    open.value = false
  } catch (e) {
    error.value = dispatchFailureWords(e)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <UModal v-model:open="open" title="New project" description="A project holds tickets under its key: CND-1, CND-2 and on." :dismissible="!submitting" :close="{ disabled: submitting }" :ui="{ content: 'max-w-lg' }">
    <template #body>
      <form class="flex flex-col gap-4" data-new-project @submit.prevent="submit">
        <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" data-new-project-error />
        <div class="grid grid-cols-[8rem_minmax(0,1fr)] gap-3">
          <UFormField label="Key" name="key" :error="keyProblem || undefined">
            <UInput :model-value="key" placeholder="CND" maxlength="6" autofocus class="w-full font-mono" aria-label="Key" data-new-project-key @update:model-value="onKey" />
          </UFormField>
          <UFormField label="Name" name="name">
            <UInput v-model="name" placeholder="Conductor" maxlength="60" class="w-full" aria-label="Name" data-new-project-name />
          </UFormField>
        </div>
        <UFormField label="Repository folder" name="cwd" hint="optional">
          <DirInput v-model="cwd" placeholder="none" name="cwd" picker-title="The project's repository" />
        </UFormField>
        <UFormField label="Tracker" name="tracker">
          <URadioGroup v-model="tracker" :items="trackerItems" variant="card" size="sm" :ui="{ fieldset: 'grid grid-cols-1 gap-2 sm:grid-cols-2' }" data-new-project-tracker />
        </UFormField>
        <UFormField v-if="tracker === 'external'" label="Its URL" name="trackerUrl" :error="urlProblem || undefined">
          <UInput v-model="trackerUrl" placeholder="https://github.com/acme/api/issues" class="w-full" aria-label="The tracker's URL" />
        </UFormField>
        <UFormField label="To close a ticket" name="closeRequires" hint="nothing ticked: nothing required" data-new-project-policy>
          <div class="flex flex-col gap-2">
            <UCheckbox
              v-for="r in CLOSE_REQUIREMENTS"
              :key="r.value"
              :model-value="closeRequires.includes(r.value)"
              :label="r.label"
              :description="r.description"
              :data-close-requirement="r.value"
              @update:model-value="(on: boolean | 'indeterminate') => tick(r.value, on)"
            />
          </div>
        </UFormField>
        <button type="submit" class="hidden" />
      </form>
    </template>
    <template #footer>
      <div class="flex w-full justify-end gap-2">
        <UButton label="Cancel" color="neutral" variant="ghost" :disabled="submitting" @click="open = false" />
        <UButton label="Make the project" icon="i-lucide-plus" :loading="submitting" :disabled="invalid" data-new-project-submit @click="submit" />
      </div>
    </template>
  </UModal>
</template>
