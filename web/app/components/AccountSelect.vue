<script setup lang="ts">
import type { AccountsDoc } from '~/composables/useAccounts'
import { accountChoices } from '~/utils/capacity'

/**
 * The Account choice of a launch (docs/plans/capacity.md, PR-5): the agent's default login on this server, then each account of its
 * adapter that is not retired, the adapter's default marked. The value is the account's id, `default` for the agent's own login;
 * empty until the server's view says which account a launch naming none takes.
 */
const model = defineModel<string>({ default: '' })
const props = defineProps<{ doc: AccountsDoc | null; adapter?: string; disabled?: boolean }>()
const items = computed(() => accountChoices(props.doc, props.adapter))
</script>

<template>
  <USelect v-model="model" :items="items" class="w-full" :disabled="disabled" placeholder="as the server resolves it" aria-label="Account" data-account-select />
</template>
