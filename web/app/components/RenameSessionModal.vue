<script setup lang="ts">
/**
 * Rename a server session, running or ended (#318): one dialog, in the layout, opened from a sidebar row's menu (and its sheet on
 * a phone), the session page's menu and the palette (useRenameModal). The name is the session's from then on wherever it shows,
 * the events stream telling every page, and a resume starts under it. The field says what the server keeps (control characters
 * and the space around the name dropped) and asks for a shorter name rather than send one the server would cut; a refusal says why.
 */
const form = ref<HTMLFormElement>()
// The Rename button goes disabled as it saves and can hold the focus no longer: the field takes it, so the dialog keeps it all
// through the save. Its focus trap gives a focus taken from it back to the last thing focused in it, and a disabled button takes
// none: the row's menu, unmounting late on a loaded machine, gave its More button the focus behind the dialog, and the list there
// took Escape for its own.
const { rename, draft, saving, state, error, onFocus, save } = useRenameDialog(() =>
  form.value?.querySelector<HTMLInputElement>('[data-rename-input]')?.focus(),
)
</script>

<template>
  <UModal v-model:open="rename.open.value" title="Rename session" description="The name it shows everywhere; a resume starts under it.">
    <template #body>
      <form ref="form" class="flex flex-col gap-3" data-rename-dialog @submit.prevent="save">
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
