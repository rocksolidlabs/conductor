<script setup lang="ts">
import type { BrakemanRequest, BriefItem, RefResolution } from '~/utils/brakeman'
import { displayBy } from '~/utils/brakeman'
import { makeAgain, madeWords } from '~/utils/brakemanCards'
import { ApiError } from '~/composables/useApi'

/**
 * A draft crew (docs/plans/brakeman.md, PR-7): the crew as a form the person edits (its name and goal, and its body as the JSON
 * POST /api/crews takes), the gates Brakeman notes before its tasks, and Save, which makes it a saved crew through the make route
 * (PR-4: the crew store's CreateFrom, never launched). Saved, it opens in the crew editor, where the person launches it or not. A
 * save that did not finish (the backend failed after its claim) is finished from here, asked again as it was sent: the form shows
 * that content and takes no edit, since the route finishes the make only on its own id and content.
 */
const props = defineProps<{ request: BrakemanRequest; item: Extract<BriefItem, { kind: 'draft_crew/1' }>; index: number; refs: ReadonlyMap<string, RefResolution> }>()

const bk = useBrakeman()
const identity = useIdentity()
const router = useRouter()

const req = computed(() => bk.requests.value.find((r) => r.id === props.request.id) ?? props.request)
const made = computed(() => req.value.made?.find((m) => m.item === props.index))
const done = computed(() => made.value?.state === 'made' || made.value?.state === 'deleted')

const name = ref(typeof props.item.crew?.name === 'string' ? props.item.crew.name : '')
const goal = ref(typeof props.item.crew?.goal === 'string' ? (props.item.crew.goal as string) : '')
/** The rest of the body, as JSON the person may change. */
const body = ref(JSON.stringify(Object.fromEntries(Object.entries(props.item.crew ?? {}).filter(([k]) => k !== 'name' && k !== 'goal')), null, 2))
const parsed = computed<{ ok: true; value: Record<string, unknown> } | { ok: false; why: string }>(() => {
  try {
    const v = JSON.parse(body.value || '{}')
    return v && typeof v === 'object' && !Array.isArray(v) ? { ok: true, value: v } : { ok: false, why: 'The body is a JSON object.' }
  } catch (e) {
    return { ok: false, why: `The body is no JSON: ${(e as Error).message}` }
  }
})
const summary = computed(() => {
  if (!parsed.value.ok) return ''
  const v = parsed.value.value as { members?: unknown[]; tasks?: unknown[] }
  const m = Array.isArray(v.members) ? v.members.length : 0
  const t = Array.isArray(v.tasks) ? v.tasks.length : 0
  return `${m} ${m === 1 ? 'member' : 'members'}${t ? `, ${t} ${t === 1 ? 'task' : 'tasks'}` : ''}`
})

/** The save this browser asked that did not finish, and what the card does about it. */
const kept = computed(() => bk.keptMake(props.request.id, props.index))
/** A new make on its way: kept before it is sent (useBrakeman's make), it is this card's to answer, not one to finish yet. */
const creating = ref(false)
const again = computed(() => makeAgain(made.value, creating.value ? undefined : kept.value))
watch(
  again,
  (a) => {
    if (a.kind === 'drop') bk.forgetMake(props.request.id, props.index)
  },
  { immediate: true },
)
/** To be finished, the form shows the content it was asked with. */
watch(
  () => (again.value.kind === 'finish' ? kept.value : undefined),
  (k) => {
    const c = (k?.body.edit as { crew?: Record<string, unknown> } | undefined)?.crew
    if (!c) return
    name.value = typeof c.name === 'string' ? c.name : ''
    goal.value = typeof c.goal === 'string' ? c.goal : ''
    body.value = JSON.stringify(Object.fromEntries(Object.entries(c).filter(([k]) => k !== 'name' && k !== 'goal')), null, 2)
  },
  { immediate: true },
)
const finishing = computed(() => again.value.kind === 'finish')

const sending = ref(false)
const error = ref('')

async function save() {
  if (sending.value || done.value || (!finishing.value && !parsed.value.ok)) return
  sending.value = true
  error.value = ''
  try {
    let out
    if (finishing.value) out = await bk.finishMake(props.request.id, props.index)
    else {
      if (!parsed.value.ok) return
      const crew = { ...parsed.value.value, name: name.value.trim(), ...(goal.value.trim() ? { goal: goal.value.trim() } : {}) }
      creating.value = true
      out = await bk.make(props.request.id, props.index, { by: displayBy(identity.name.value) || undefined, edit: { crew, gates: props.item.gates, from: props.item.from } })
    }
    // Saved, never launched: the crew editor shows it, where the person decides what next.
    if (out.crew?.id) await router.push(`/crews/${encodeURIComponent(out.crew.id)}`)
  } catch (e) {
    if (e instanceof ApiError && e.code === 'already_made') void bk.readRequest(props.request.id)
    error.value = (e as Error).message
  } finally {
    sending.value = false
    creating.value = false
  }
}
</script>

<template>
  <div class="flex flex-col gap-2 rounded-md border border-default p-3" data-brief-draft data-draft-crew :data-draft-item="index" :data-made-state="made?.state ?? ''">
    <div class="flex flex-wrap items-center gap-2 text-sm">
      <UIcon name="i-lucide-users" class="size-4 text-muted" />
      <span class="font-medium">A draft crew</span>
      <span v-if="summary" class="text-xs text-muted">{{ summary }}</span>
      <span v-if="item.from?.length" class="flex flex-wrap items-center gap-1 text-xs text-muted">from <BrakemanRefChip v-for="r in item.from" :key="r" :ref-name="r" :resolution="refs.get(r)" /></span>
    </div>
    <ul v-if="item.gates?.length" class="flex flex-col gap-1 text-xs" data-draft-gates>
      <li v-for="g in item.gates" :key="g.task"><span class="font-mono">{{ g.task }}</span>: you decide first: {{ g.decision }}</li>
    </ul>
    <template v-if="done">
      <p class="text-sm" data-made-words>
        {{ madeWords(made) }}
        <NuxtLink v-if="made?.crew" :to="`/crews/${encodeURIComponent(made.crew)}`" class="underline" data-made-link>Open it in the crew editor</NuxtLink>
      </p>
    </template>
    <template v-else>
      <div class="grid gap-2 sm:grid-cols-2">
        <UFormField label="Name"><UInput v-model="name" class="w-full" :disabled="finishing" data-draft-crew-name /></UFormField>
        <UFormField label="Goal"><UInput v-model="goal" class="w-full" :disabled="finishing" data-draft-crew-goal /></UFormField>
      </div>
      <UFormField label="Members, tasks and the rest" description="The body POST /api/crews takes, as JSON."><UTextarea v-model="body" :rows="6" autoresize class="w-full font-mono text-xs" :disabled="finishing" data-draft-crew-body /></UFormField>
      <p v-if="!parsed.ok" class="text-xs text-error">{{ parsed.why }}</p>
      <p v-if="again.words" class="text-xs text-muted" data-made-words>{{ again.words }}</p>
      <p v-else-if="made" class="text-xs text-muted" data-made-words>{{ madeWords(made) }}</p>
      <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" data-draft-error />
      <div class="flex items-center gap-2">
        <UButton v-if="finishing" label="Finish saving it" icon="i-lucide-rotate-cw" size="sm" :loading="sending" data-draft-finish @click="save" />
        <UButton v-else label="Save the crew" icon="i-lucide-save" size="sm" :loading="sending" :disabled="!name.trim() || !parsed.ok || made?.state === 'pending'" data-draft-create @click="save" />
        <span class="text-xs text-muted">Saved, not launched: it opens in the crew editor.</span>
      </div>
    </template>
  </div>
</template>
