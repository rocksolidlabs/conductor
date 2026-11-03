<script setup lang="ts">
import type { BrakemanRequest, BriefItem, RefResolution } from '~/utils/brakeman'
import { refRoute } from '~/utils/brakeman'
import { blocksWords, madeWords } from '~/utils/brakemanCards'
import { ApiError } from '~/composables/useApi'

/**
 * A draft ticket (docs/plans/brakeman.md, PR-7): the draft as a form the person edits, and Create, which makes it a ticket through
 * the make route (PR-4: Dispatch's own operation under the admin, with the person's name, the derived op id kept before it runs).
 * Once made, the ticket it became, linked; its blocks of the brief's other drafts become relations as both ends are made, in either
 * order. A make asked again answers what it made: nothing is made twice.
 */
const props = defineProps<{ request: BrakemanRequest; item: Extract<BriefItem, { kind: 'draft_ticket/1' }>; index: number; items: readonly BriefItem[]; refs: ReadonlyMap<string, RefResolution> }>()

const bk = useBrakeman()
const identity = useIdentity()

/** The request as the page has it now: a make's reply and the events bring its `made` and `edges`. */
const req = computed(() => bk.requests.value.find((r) => r.id === props.request.id) ?? props.request)
const made = computed(() => req.value.made?.find((m) => m.item === props.index))
const done = computed(() => made.value?.state === 'made' || made.value?.state === 'deleted')

const project = ref(props.item.project)
const title = ref(props.item.title)
const description = ref(props.item.description ?? '')
const acceptance = ref(props.item.acceptance ?? '')
const priority = ref(props.item.priority ?? 0)
const priorities = [0, 1, 2, 3, 4].map((p) => ({ label: p === 0 ? 'none' : `P${p}`, value: p }))

const sending = ref(false)
const error = ref('')

/** A draft's title by its index in the brief, as the blocks name it. */
const draftTitle = (i: number) => {
  const it = props.items[i]
  return it?.kind === 'draft_ticket/1' ? `"${it.title}"` : `item ${i + 1}`
}
const blocks = computed(() => blocksWords(props.index, props.item.blocks, req.value.edges, draftTitle))

async function create() {
  if (sending.value || done.value) return
  sending.value = true
  error.value = ''
  try {
    const edit = { project: project.value.trim(), title: title.value, description: description.value, acceptance: acceptance.value, priority: priority.value, blocks: props.item.blocks, from: props.item.from }
    await bk.make(props.request.id, props.index, { by: identity.name.value.trim() || undefined, edit })
  } catch (e) {
    // Made already (another tab, a reply lost twice): the request read again shows what it became.
    if (e instanceof ApiError && e.code === 'already_made') void bk.readRequest(props.request.id)
    error.value = (e as Error).message
  } finally {
    sending.value = false
  }
}
</script>

<template>
  <div class="flex flex-col gap-2 rounded-md border border-default p-3" data-brief-draft data-draft-ticket :data-draft-item="index" :data-made-state="made?.state ?? ''">
    <div class="flex flex-wrap items-center gap-2 text-sm">
      <UIcon name="i-lucide-ticket-plus" class="size-4 text-muted" />
      <span class="font-medium">A draft ticket</span>
      <span v-if="item.from?.length" class="flex flex-wrap items-center gap-1 text-xs text-muted">from <BrakemanRefChip v-for="r in item.from" :key="r" :ref-name="r" :resolution="refs.get(r)" /></span>
    </div>
    <template v-if="done">
      <p class="text-sm" data-made-words>
        {{ madeWords(made) }}
        <NuxtLink v-if="made?.key" :to="refRoute(`ticket:${made.key}`)!" class="font-mono underline" data-made-link>{{ made.key }}</NuxtLink>
      </p>
      <p v-if="blocks" class="text-xs text-muted" data-draft-blocks>{{ blocks }}</p>
    </template>
    <template v-else>
      <div class="grid gap-2 sm:grid-cols-[8rem_1fr]">
        <UFormField label="Project"><UInput v-model="project" class="w-full font-mono" data-draft-project /></UFormField>
        <UFormField label="Title"><UInput v-model="title" class="w-full" maxlength="200" data-draft-title /></UFormField>
      </div>
      <UFormField label="Description"><UTextarea v-model="description" :rows="3" autoresize class="w-full" data-draft-description /></UFormField>
      <UFormField label="Acceptance"><UTextarea v-model="acceptance" :rows="2" autoresize class="w-full" data-draft-acceptance /></UFormField>
      <UFormField label="Priority"><USelect v-model="priority" :items="priorities" class="w-32" data-draft-priority /></UFormField>
      <p v-if="blocks" class="text-xs text-muted" data-draft-blocks>{{ blocks }}</p>
      <p v-if="made" class="text-xs text-muted" data-made-words>{{ madeWords(made) }}</p>
      <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" data-draft-error />
      <div class="flex items-center gap-2">
        <UButton label="Create the ticket" icon="i-lucide-plus" size="sm" :loading="sending" :disabled="!title.trim() || !project.trim() || made?.state === 'pending'" data-draft-create @click="create" />
        <span class="text-xs text-muted">Made as you edited it, under your name; Brakeman makes nothing itself.</span>
      </div>
    </template>
  </div>
</template>
