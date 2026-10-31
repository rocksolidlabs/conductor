<script setup lang="ts">
import { MAX_SESSION_NAME } from '~/utils/protocol'
import { renameDraft } from '~/utils/sessionName'

/**
 * Rename a server session, running or ended (#318): one dialog, in the layout, opened from a sidebar row's menu (and its sheet on
 * a phone), the session page's menu and the palette (useRenameModal). The name is the session's from then on wherever it shows,
 * the events stream telling every page, and a resume starts under it. The field says what the server keeps (control characters
 * and the space around the name dropped) and asks for a shorter name rather than send one the server would cut; a refusal says why.
 */
const rename = useRenameModal()
const api = useSessions()
const draft = ref('')
const saving = ref(false)
const refused = ref('')

// The name as it is, selected as the field first takes the focus: typing replaces it, a click after places the caret.
let selectNext = false
// Every opening starts afresh, on its session's name: one made while the dialog is open (Rename… on another session from the
// palette, over this dialog) included, which leaves open as it was and changes only the target and the opening.
watch(rename.opening, () => {
  if (!rename.open.value) return
  draft.value = rename.target.value?.name ?? ''
  refused.value = ''
  // A save still under way belongs to an earlier opening: this one is not waiting on it.
  saving.value = false
  selectNext = true
})
function onFocus(e: FocusEvent) {
  if (!selectNext) return
  selectNext = false
  ;(e.target as HTMLInputElement).select()
}
watch(draft, () => (refused.value = ''))

const state = computed(() => renameDraft(draft.value))
const error = computed(() => (state.value.problem === 'long' ? `Too long: a name is at most ${MAX_SESSION_NAME} bytes, and this one is ${state.value.bytes}` : refused.value || undefined))

async function save() {
  const t = rename.target.value
  if (!t || saving.value || state.value.problem) return
  // The name it has already: nothing to send.
  if (state.value.name === t.name) {
    rename.open.value = false
    return
  }
  // The opening this save is sent from: its reply closes the dialog or shows its refusal only while that opening lasts, never in
  // a dialog opened since (the save's dialog dismissed while it was under way, then Rename… on another session).
  const sentFrom = rename.opening.value
  const current = () => rename.open.value && rename.opening.value === sentFrom
  saving.value = true
  try {
    await api.rename(t.id, state.value.name)
    if (current()) rename.open.value = false
  } catch (e) {
    if (current()) refused.value = (e as Error).message
  } finally {
    if (rename.opening.value === sentFrom) saving.value = false
  }
}
</script>

<template>
  <UModal v-model:open="rename.open.value" title="Rename session" description="The name it shows everywhere; a resume starts under it.">
    <template #body>
      <form class="flex flex-col gap-3" data-rename-dialog @submit.prevent="save">
        <UFormField label="Name" name="name" :error="error">
          <UInput
            v-model="draft"
            aria-label="Session name"
            class="w-full"
            autofocus
            data-rename-input
            @focus="onFocus"
          />
        </UFormField>
        <div class="flex justify-end gap-2">
          <UButton label="Cancel" color="neutral" variant="ghost" @click="rename.open.value = false" />
          <UButton type="submit" label="Rename" :loading="saving" :disabled="!!state.problem" data-rename-save />
        </div>
      </form>
    </template>
  </UModal>
</template>
