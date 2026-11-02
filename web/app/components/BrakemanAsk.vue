<script setup lang="ts">
import type { BrakemanRequest } from '~/utils/brakeman'
import { KINDS, KIND_SUBJECTS, askProblem, kindWords, parseRef } from '~/utils/brakeman'

/**
 * Asking Brakeman (docs/plans/brakeman.md, PR-6): a kind, the subjects it takes, picked from the runs, their tasks and the sessions
 * the page has live, or a ticket by its key; a note; and for a briefing, the time it starts at (empty: since the last brief, else
 * the last day). The checks are the server's (askProblem); a refusal says the server's words. Asked, the request shows on the page.
 */
const props = defineProps<{ kind?: string; subject?: string; disabled?: boolean }>()
const emit = defineEmits<{ asked: [request: BrakemanRequest] }>()

const bk = useBrakeman()
const live = useAttention()
const identity = useIdentity()

const kind = ref(props.kind && KIND_SUBJECTS[props.kind] ? props.kind : 'briefing')
const subjects = ref<string[]>(props.subject && parseRef(props.subject) ? [props.subject] : [])
const ticketKey = ref('')
const note = ref('')
const since = ref('')
const sending = ref(false)
const error = ref('')

const takes = computed(() => KIND_SUBJECTS[kind.value] ?? { kinds: [], min: 0, max: 0, note: false })
const kindItems = KINDS.map((k) => ({ label: k.label, value: k.value }))

/** What may be named: the runs and their tasks, and the sessions, by what the kind takes. */
const subjectItems = computed(() => {
  const takesKind = (k: string) => !takes.value.kinds.length || takes.value.kinds.includes(k)
  const out: Array<{ label: string; value: string }> = []
  for (const r of live.runs.value) {
    if (takesKind('run')) out.push({ label: `Run ${r.label || r.name} (${r.id})`, value: `run:${r.id}` })
    if (takesKind('task')) for (const t of r.tasks ?? []) out.push({ label: `Task ${t.id} of ${r.label || r.name}`, value: `task:${r.id}/${t.id}` })
  }
  if (takesKind('session')) for (const s of live.sessions.value) out.push({ label: `Session ${s.name}`, value: `session:${s.id}` })
  for (const s of subjects.value) if (!out.some((o) => o.value === s)) out.push({ label: s, value: s })
  return out
})
const takesTicket = computed(() => !takes.value.kinds.length ? takes.value.max > 0 : takes.value.kinds.includes('ticket'))

watch(kind, () => {
  // A kind that takes other subjects keeps none of the last one's that it does not take.
  subjects.value = subjects.value.filter((s) => {
    const r = parseRef(s)
    return r && (!takes.value.kinds.length || takes.value.kinds.includes(r.kind))
  }).slice(0, takes.value.max)
  if (kind.value !== 'briefing') since.value = ''
})

function addTicket() {
  const key = ticketKey.value.trim().toUpperCase()
  if (!key) return
  const ref = `ticket:${key}`
  if (!parseRef(ref)) {
    error.value = `${key} is no ticket key (a project's key, a dash and its number: CND-4).`
    return
  }
  error.value = ''
  if (!subjects.value.includes(ref)) subjects.value = [...subjects.value, ref]
  ticketKey.value = ''
}

/** The display name as a request keeps it: one line of at most 60 bytes (brakeman.MaxBy), cut where a character ends. */
function byName(name: string): string {
  let out = ''
  for (const ch of name.replace(/[\n\t]/g, ' ')) {
    if (new TextEncoder().encode(out + ch).length > 60) break
    out += ch
  }
  return out.trim()
}

/** The since field's local time as the server takes it: RFC 3339, in UTC. */
const sinceISO = computed(() => {
  if (!since.value) return ''
  const t = new Date(since.value)
  return Number.isNaN(t.getTime()) ? '' : t.toISOString()
})
const problem = computed(() => askProblem(kind.value, subjects.value, note.value, sinceISO.value))

async function send() {
  if (problem.value || sending.value) return
  sending.value = true
  error.value = ''
  try {
    const r = await bk.ask({
      kind: kind.value,
      subjects: subjects.value.length ? subjects.value : undefined,
      note: note.value.trim() ? note.value : undefined,
      since: sinceISO.value || undefined,
      by: byName(identity.name.value) || undefined,
    })
    note.value = ''
    subjects.value = []
    since.value = ''
    emit('asked', r)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    sending.value = false
  }
}
</script>

<template>
  <form class="flex flex-col gap-3" data-brakeman-ask @submit.prevent="send">
    <div class="grid gap-3 sm:grid-cols-2">
      <UFormField label="Ask">
        <USelect v-model="kind" :items="kindItems" class="w-full" data-ask-kind />
      </UFormField>
      <UFormField v-if="kind === 'briefing'" label="Since" description="Empty: since the last brief, else the last day.">
        <UInput v-model="since" type="datetime-local" class="w-full" data-ask-since />
      </UFormField>
    </div>
    <UFormField v-if="takes.max > 0" :label="takes.max === 1 ? 'About' : 'Subjects'" :description="takes.min ? undefined : 'Optional.'">
      <div class="flex flex-col gap-2">
        <USelectMenu v-model="subjects" :items="subjectItems" value-key="value" multiple :placeholder="`Pick ${takes.max === 1 ? 'one' : 'what it is about'}`" class="w-full" data-ask-subjects />
        <div v-if="takesTicket" class="flex gap-2">
          <UInput v-model="ticketKey" placeholder="A ticket's key, such as CND-4" class="flex-1" data-ask-ticket @keydown.enter.prevent="addTicket" />
          <UButton label="Add" color="neutral" variant="outline" @click="addTicket" />
        </div>
      </div>
    </UFormField>
    <UFormField :label="takes.note ? 'Question' : 'Note'" :description="takes.note ? undefined : 'Optional: anything Brakeman should know.'">
      <UTextarea v-model="note" :rows="3" autoresize class="w-full" data-ask-note />
    </UFormField>
    <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" data-ask-error />
    <div class="flex items-center gap-3">
      <UButton type="submit" :label="kindWords(kind)" icon="i-lucide-send" :loading="sending" :disabled="disabled || !!problem" data-ask-send />
      <span v-if="problem && (subjects.length || note)" class="text-xs text-muted">{{ problem }}</span>
    </div>
  </form>
</template>
