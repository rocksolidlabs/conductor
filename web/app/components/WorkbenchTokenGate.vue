<script setup lang="ts">
const open = defineModel<boolean>('open', { default: false })
const admin = useWorkbenchToken()
const draft = ref('')

// The draft starts from the token this browser keeps, never from one only this page holds. Closed (Cancel, Escape, outside it), the
// ask ends, so the next refusal opens the gate again.
watch(open, (v) => {
  if (v) draft.value = admin.kept()
  else admin.cancel()
})
// A 401 anywhere opens the gate; a token the server has since accepted (a link's) closes it.
watch(
  () => admin.needsToken.value,
  (v) => {
    open.value = v
  },
)

function save() {
  admin.set(draft.value)
  open.value = false
}
</script>

<template>
  <UModal v-model:open="open" title="Workbench token" description="The token from CONDUCTOR_WORKBENCH_TOKEN (or the one printed at server start). It opens everything here and is kept in this browser only.">
    <template #body>
      <form class="flex flex-col gap-3" @submit.prevent="save">
        <UFormField label="Token" name="token">
          <UInput v-model="draft" type="password" autocomplete="off" placeholder="paste the workbench token" class="w-full" autofocus />
        </UFormField>
        <div class="flex justify-end gap-2">
          <UButton label="Cancel" color="neutral" variant="ghost" @click="open = false" />
          <UButton label="Save" type="submit" :disabled="!draft.trim()" />
        </div>
      </form>
    </template>
  </UModal>
</template>
