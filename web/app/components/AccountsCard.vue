<script setup lang="ts">
import type { AgentInfo } from '~/composables/useSessions'
import type { Account, AccountsAdapter } from '~/composables/useAccounts'

/**
 * Settings, Accounts (docs/plans/capacity.md, PR-5): one block per agent tool whose login folder Conductor can move (Claude Code,
 * Codex), with its default login on this server and the accounts added, each a label and the folder that login lives in. Add,
 * rename, set as default, retire (kept for the work bound to it, out of every choice) and remove (refused while anything holds it,
 * with what does). Each account's last reading, and Read now, through an installed agent of that tool. Every change is the server's
 * route, sent with the revision read; nothing here is part of the page's Save. Conductor keeps no credential and reads nothing in a
 * folder. A server that does not own the accounts shows them and changes nothing.
 */
const { store, doc, available, busy, problem } = useAccounts()
const sessions = useSessions()
const agents = ref<AgentInfo[]>([])
const error = ref('')
const notice = ref('')

// Another workbench token empties the store (useAccounts): the card reads the accounts again with it.
watch(useWorkbenchToken().token, () => void store.load())
onMounted(async () => {
  await store.load()
  try {
    agents.value = (await sessions.catalogInfo()).agents
  } catch {
    /* no catalog: no reading is offered, the accounts still are */
  }
})

const owner = computed(() => doc.value?.owner !== false)

/** The installed agent a reading of an adapter's account is asked through: the last reading's, the adapter's namesake, else the first. */
function readerAgent(ad: AccountsAdapter, a?: Account): string {
  const of = agents.value.filter((x) => x.adapter === ad.adapter && x.available !== false)
  const named = (id?: string) => (id && of.some((x) => x.id === id) ? id : '')
  return named(a?.reading?.agent) || named(ad.reading?.agent) || named(ad.adapter) || of[0]?.id || ''
}

/** The agents whose tool's login folder Conductor cannot move: their own login only, their usage unknown. */
const others = computed(() => {
  const moved = new Set(doc.value?.adapters.map((a) => a.adapter) ?? [])
  return agents.value.filter((a) => a.available !== false && !moved.has(a.adapter ?? '')).map((a) => a.name)
})

function accountsOf(adapter: string): Account[] {
  const list = doc.value?.accounts.filter((a) => a.adapter === adapter) ?? []
  return [...list.filter((a) => !a.retired), ...list.filter((a) => a.retired)]
}

async function run(what: () => Promise<void>, done?: string) {
  error.value = ''
  notice.value = ''
  try {
    await what()
    if (done) notice.value = done
  } catch (e) {
    error.value = (e as Error).message
  }
}

// Adding: the tool, a label and the folder its login lives in.
const add = reactive({ adapter: '', label: '', folder: '' })
watch(
  () => doc.value?.adapters.map((a) => a.adapter).join(','),
  () => {
    if (!doc.value?.adapters.some((a) => a.adapter === add.adapter)) add.adapter = doc.value?.adapters[0]?.adapter ?? ''
  },
  { immediate: true },
)
const adapterItems = computed(() => (doc.value?.adapters ?? []).map((a) => ({ label: a.name, value: a.adapter })))
async function addAccount() {
  if (!add.adapter || !add.label.trim() || !add.folder.trim()) return
  const label = add.label.trim()
  await run(async () => {
    await store.add(add.adapter, label, add.folder)
    add.label = ''
    add.folder = ''
  }, `${label} added`)
}

// Renaming one account at a time, in place.
const renaming = ref<{ id: string; label: string } | null>(null)
async function saveRename() {
  const r = renaming.value
  if (!r || !r.label.trim()) return
  await run(async () => {
    await store.rename(r.id, r.label)
    renaming.value = null
  })
}

// Removing asks once more, in place.
const removing = ref('')
</script>

<template>
  <UCard v-if="available !== false" :ui="{ header: 'px-4 py-3 sm:px-5', body: 'p-4 sm:p-5' }" data-accounts-card>
    <template #header><SettingsCardTitle title="Accounts" description="The logins your agents use on this server, each by the folder it lives in. Conductor keeps no credential and reads nothing in a folder." /></template>
    <div class="flex flex-col gap-4">
      <UAlert v-if="!owner" color="warning" variant="subtle" icon="i-lucide-lock" title="Another server over this data directory keeps the accounts: they are shown here, and changed there" :description="doc?.why" data-accounts-not-owner />
      <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" data-accounts-error />
      <UAlert v-if="notice" color="success" variant="subtle" icon="i-lucide-check" :title="notice" data-accounts-notice />
      <UAlert
        v-if="problem"
        color="error"
        variant="subtle"
        icon="i-lucide-circle-alert"
        :title="doc ? 'The accounts could not be read again: what is shown is the last read' : 'The accounts could not be read'"
        :description="problem"
        :actions="[{ label: 'Try again', color: 'neutral', variant: 'outline', onClick: () => store.load() }]"
        data-accounts-problem
      />
      <p v-else-if="!doc && available === null" class="text-sm text-muted">Reading the accounts…</p>

      <section v-for="ad in doc?.adapters ?? []" :key="ad.adapter" class="flex flex-col gap-2" :data-accounts-adapter="ad.adapter">
        <h3 class="text-sm font-semibold">{{ ad.name }}</h3>
        <ul class="flex flex-col divide-y divide-default rounded-md border border-default">
          <li class="flex flex-col gap-1.5 p-3" data-account-row="default">
            <div class="flex flex-wrap items-center gap-2">
              <span class="text-sm font-medium">Default login</span>
              <UBadge v-if="ad.default === 'default'" label="default" color="primary" variant="subtle" size="sm" data-account-is-default />
              <span class="text-xs text-muted">the tool's own login on this server, where it keeps it</span>
              <div class="flex-1" />
              <UButton v-if="ad.default !== 'default'" label="Set as default" size="xs" color="neutral" variant="outline" :disabled="busy || !owner" data-account-set-default @click="run(() => store.setDefault(ad.adapter, 'default'))" />
            </div>
            <CapacityPanel v-if="readerAgent(ad)" compact :query="{ agent: readerAgent(ad), account: 'default' }" />
          </li>
          <li v-for="a in accountsOf(ad.adapter)" :key="a.id" class="flex flex-col gap-1.5 p-3" :class="a.retired ? 'opacity-70' : ''" :data-account-row="a.id" :data-account-label="a.label">
            <div class="flex flex-wrap items-center gap-2">
              <template v-if="renaming?.id === a.id">
                <UInput v-model="renaming.label" size="sm" maxlength="60" class="min-w-40 flex-1" aria-label="The account's new label" data-account-rename-input @keydown.enter.prevent="saveRename" />
                <UButton label="Save" size="xs" :disabled="busy || !renaming.label.trim()" data-account-rename-save @click="saveRename" />
                <UButton label="Cancel" size="xs" color="neutral" variant="ghost" @click="renaming = null" />
              </template>
              <template v-else>
                <span class="text-sm font-medium" data-account-name>{{ a.label }}</span>
                <UBadge v-if="ad.default === a.id" label="default" color="primary" variant="subtle" size="sm" data-account-is-default />
                <UBadge v-if="a.retired" label="retired" color="neutral" variant="outline" size="sm" data-account-retired />
                <code class="min-w-0 flex-1 break-all text-xs text-muted" data-account-folder>{{ a.folder }}</code>
              </template>
            </div>
            <div v-if="renaming?.id !== a.id" class="flex flex-wrap items-center gap-1.5">
              <template v-if="removing === a.id">
                <span class="text-xs">Remove {{ a.label }}? Its id is never given again; the folder is left as it is.</span>
                <UButton label="Remove" size="xs" color="error" variant="soft" :disabled="busy" data-account-remove-confirm @click="run(() => store.remove(a.id), `${a.label} removed`).then(() => (removing = ''))" />
                <UButton label="Keep it" size="xs" color="neutral" variant="ghost" @click="removing = ''" />
              </template>
              <template v-else>
                <UButton v-if="!a.retired && ad.default !== a.id" label="Set as default" size="xs" color="neutral" variant="outline" :disabled="busy || !owner" data-account-set-default @click="run(() => store.setDefault(ad.adapter, a.id))" />
                <UButton v-if="!a.retired" label="Rename" size="xs" color="neutral" variant="ghost" :disabled="busy || !owner" data-account-rename @click="renaming = { id: a.id, label: a.label }" />
                <UButton v-if="!a.retired" label="Retire" size="xs" color="neutral" variant="ghost" :disabled="busy || !owner" title="Kept for the work bound to it; out of every choice and default" data-account-retire @click="run(() => store.retire(a.id), `${a.label} retired: kept for the work bound to it, out of every choice`)" />
                <UButton label="Remove" size="xs" color="neutral" variant="ghost" :disabled="busy || !owner" data-account-remove @click="removing = a.id" />
              </template>
            </div>
            <CapacityPanel v-if="!a.retired && readerAgent(ad, a)" compact :query="{ agent: readerAgent(ad, a), account: a.id }" />
          </li>
        </ul>
      </section>

      <form v-if="doc && doc.adapters.length && owner" class="flex flex-col gap-3 rounded-md bg-elevated/50 p-3" data-account-add @submit.prevent="addAccount">
        <p class="text-sm font-medium">Add an account</p>
        <div class="grid gap-3 sm:grid-cols-[minmax(0,10rem)_minmax(0,1fr)]">
          <UFormField label="Agent tool" name="adapter">
            <USelect v-model="add.adapter" :items="adapterItems" class="w-full" data-account-add-adapter />
          </UFormField>
          <UFormField label="Label" name="label" hint="such as Work">
            <UInput v-model="add.label" maxlength="60" class="w-full" data-account-add-label />
          </UFormField>
        </div>
        <UFormField label="Folder" name="folder" description="The folder that login lives in (the tool's configuration folder). Fixed once added: for another folder, add another account.">
          <DirInput v-model="add.folder" placeholder="/home/me/.claude-work" picker-title="The folder the login lives in" data-account-add-folder />
        </UFormField>
        <div class="flex justify-end">
          <UButton type="submit" label="Add account" icon="i-lucide-plus" :loading="busy" :disabled="busy || !add.adapter || !add.label.trim() || !add.folder.trim()" data-account-add-submit />
        </div>
      </form>

      <p v-if="others.length" class="text-xs text-muted" data-accounts-others>{{ others.join(', ') }}: {{ others.length === 1 ? 'its' : 'their' }} own login on this server only. Conductor cannot move {{ others.length === 1 ? 'its' : 'their' }} login folder, and knows no way to read {{ others.length === 1 ? 'its' : 'their' }} usage.</p>
    </div>
  </UCard>
</template>
