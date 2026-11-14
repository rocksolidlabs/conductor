<script setup lang="ts">
import { followAsk } from '~/utils/workbenchGate'

const open = defineModel<boolean>('open', { default: false })
const admin = useWorkbenchToken()
const draft = ref('')

// A 401 anywhere opens the gate, also one met while a page of another layout was shown; every close ends the ask (followAsk).
followAsk(open, draft, admin)

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
