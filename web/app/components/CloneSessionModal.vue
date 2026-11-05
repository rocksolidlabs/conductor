<script setup lang="ts">
import { ApiError } from '~/composables/useApi'
import { SHARED_TREE_WARNING, cloneName, cloneNameError, cloneRefusal, inRunWorktree } from '~/utils/clone'

/**
 * Clone… (#434): the same session again under another name, in a new conversation. One dialog, in the layout, opened from a
 * sidebar row's menu, its sheet on a phone and the palette (useCloneModal). It asks only for the name, which must be no listed
 * session's: until it is, Clone is disabled and the field says why. Everything else is the source's, which the server reads:
 * its agent, its directory, its arguments and its yolo choice. A refusal is said here; a clone made opens.
 */
const clone = useCloneModal()
const api = useSessions()
const live = useAttention()
const toast = useToast()
const draft = ref('')
const saving = ref(false)
const refused = ref('')

// Every opening starts afresh, the name empty (decision 3 of #434).
watch(clone.opening, () => {
  if (!clone.open.value) return
  draft.value = ''
  refused.value = ''
  saving.value = false
})
watch(draft, () => (refused.value = ''))

const state = computed(() => cloneName(draft.value, live.sessions.value))
const error = computed(() => cloneNameError(state.value) || refused.value || undefined)
const sharedTree = computed(() => inRunWorktree(clone.target.value?.cwd))
const title = computed(() => `Clone ${clone.target.value?.name ?? ''}`)

async function save() {
  const t = clone.target.value
  if (!t || saving.value || state.value.problem) return
  // The opening this clone is sent from: its reply closes the dialog or shows its refusal only while that opening lasts.
  const sentFrom = clone.opening.value
  const current = () => clone.open.value && clone.opening.value === sentFrom
  saving.value = true
  try {
    const made = await api.clone(t.id, { name: state.value.name })
    toast.add({ title: `Cloned ${t.name}`, description: `${made.name} is a new session like ${t.name}`, color: 'success', icon: 'i-lucide-copy' })
    if (current()) clone.open.value = false
    await navigateTo(`/sessions/${made.id}`)
  } catch (e) {
    if (current()) refused.value = e instanceof ApiError ? cloneRefusal(e.code, e.message) : (e as Error).message
  } finally {
    if (clone.opening.value === sentFrom) saving.value = false
  }
}
</script>

<template>
  <UModal v-model:open="clone.open.value" :title="title" description="Its agent, directory, arguments and yolo choice, in a new conversation.">
    <template #body>
      <form class="flex flex-col gap-3" data-clone-dialog @submit.prevent="save">
        <UAlert v-if="sharedTree" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="SHARED_TREE_WARNING" data-clone-shared-tree />
        <UFormField label="Name" name="name" :error="error" :help="state.problem === 'empty' ? 'Give the clone a name no session has' : undefined">
          <UInput v-model="draft" aria-label="Clone's name" class="w-full" autofocus data-clone-input />
        </UFormField>
        <div class="flex justify-end gap-2">
          <UButton label="Cancel" color="neutral" variant="ghost" @click="clone.open.value = false" />
          <UButton type="submit" label="Clone" icon="i-lucide-copy" :loading="saving" :disabled="!!state.problem" data-clone-save />
        </div>
      </form>
    </template>
  </UModal>
</template>
