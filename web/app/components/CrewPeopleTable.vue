<script setup lang="ts">
import type { Responsibility } from '~/composables/useSessions'
import { PEOPLE_LIMITS, RESPONSIBILITIES, RESPONSIBILITY_WORDS, draftPerson, peopleProblems, type DraftMember, type DraftPerson, type DraftTask } from '~/utils/crews'

/**
 * The people of a crew (docs/plans/decisions.md, HM1), one row each: the name (one Conductor knows, from Dispatch's directory and the saved
 * crews, or typed), the responsibilities they hold in this crew (each held by one person at most: one held by another row is offered
 * disabled, "held by ana"), a note, and remove. A person is never started; they own decisions and outcomes. The server's rules are shown
 * under the row they name (peopleProblems); the server still decides.
 */
const people = defineModel<DraftPerson[]>({ default: () => [] })
const props = defineProps<{ members: readonly DraftMember[]; tasks?: readonly DraftTask[] }>()

// The names Conductor knows, read as the table opens and again whenever a name picker opens.
const known = usePeople()
onMounted(() => void known.load())

const problems = computed(() => peopleProblems({ people: people.value, members: props.members, tasks: props.tasks }))
/** A row's first problem, `Person <n>: ` taken off; '' for none. */
function problem(i: number): string {
  const what = `Person ${i + 1}: `
  return problems.value.find((p) => p.startsWith(what))?.slice(what.length) ?? ''
}
const tableProblem = computed(() => problems.value.find((p) => p.startsWith('At most ')) ?? '')

/** A row's person in labels: their name, or their place while they have none. */
function who(p: DraftPerson, i: number): string {
  return p.name || `person ${i + 1}`
}

/** The four responsibilities for a row: one another row holds is offered disabled, with its holder. */
function responsibilityItems(p: DraftPerson) {
  return RESPONSIBILITIES.map((r) => {
    const holder = people.value.find((o) => o.key !== p.key && o.responsibilities?.includes(r))
    return { label: holder ? `${RESPONSIBILITY_WORDS[r]} · held by ${holder.name || 'another person'}` : RESPONSIBILITY_WORDS[r], value: r, disabled: !!holder }
  })
}

function update(key: number, patch: Partial<DraftPerson>) {
  people.value = people.value.map((p) => (p.key === key ? { ...p, ...patch } : p))
}

function setResponsibilities(key: number, rs: Responsibility[]) {
  // In their order, whatever order they were picked in.
  update(key, { responsibilities: RESPONSIBILITIES.filter((r) => rs.includes(r)) })
}

function add() {
  people.value = [...people.value, draftPerson({ name: '' })]
}

function remove(key: number) {
  people.value = people.value.filter((p) => p.key !== key)
}
</script>

<template>
  <div class="flex flex-col gap-2" data-crew-people>
    <div class="@container overflow-hidden rounded-lg ring ring-default">
      <div class="hidden grid-cols-[13rem_18rem_minmax(8rem,1fr)_1.5rem] gap-x-2.5 border-b border-default bg-muted px-4 py-2.5 text-[11px] font-semibold uppercase tracking-wider text-muted @min-[48rem]:grid" aria-hidden="true">
        <span>Name</span>
        <span>Responsibilities</span>
        <span>Note</span>
        <span />
      </div>

      <div v-for="(p, i) in people" :key="p.key" class="flex flex-col gap-2 border-b border-default px-4 py-2.5 last:border-b-0" data-crew-person :data-person-name="p.name">
        <div class="grid grid-cols-[minmax(0,1fr)_1.5rem] items-start gap-x-2.5 gap-y-2 @min-[48rem]:grid-cols-[13rem_18rem_minmax(8rem,1fr)_1.5rem]">
          <PersonPicker
            :model-value="p.name"
            :names="known.names.value"
            create="Name them {name}"
            placeholder="A name"
            :aria-label="`Name of ${who(p, i)}`"
            class="col-start-1 row-start-1 @min-[48rem]:col-start-auto @min-[48rem]:row-start-auto"
            data-person-name-picker
            @update:model-value="update(p.key, { name: $event })"
            @open="known.load()"
          />
          <USelect
            :model-value="p.responsibilities ?? []"
            :items="responsibilityItems(p)"
            multiple
            placeholder="no responsibility"
            :aria-label="`Responsibilities of ${who(p, i)}`"
            class="col-start-1 row-start-2 w-full @min-[48rem]:col-start-auto @min-[48rem]:row-start-auto"
            data-person-responsibilities
            @update:model-value="setResponsibilities(p.key, $event as Responsibility[])"
          />
          <UInput
            :model-value="p.note ?? ''"
            placeholder="note (optional)"
            :maxlength="PEOPLE_LIMITS.note"
            :aria-label="`Note on ${who(p, i)}`"
            class="col-start-1 row-start-3 w-full @min-[48rem]:col-start-auto @min-[48rem]:row-start-auto"
            data-person-note
            @update:model-value="update(p.key, { note: String($event) || undefined })"
          />
          <div class="col-start-2 row-start-1 flex h-8 items-center justify-end @min-[48rem]:col-start-auto @min-[48rem]:row-start-auto">
            <UButton icon="i-lucide-x" color="neutral" variant="ghost" size="xs" :aria-label="`Remove ${who(p, i)}`" data-person-remove @click="remove(p.key)" />
          </div>
        </div>
        <span v-if="problem(i)" class="text-xs leading-snug text-error" data-person-error>{{ problem(i) }}</span>
      </div>

      <p v-if="!people.length" class="px-4 py-3 text-sm text-muted">No people yet: add the ones who own the crew's decisions and outcomes.</p>
    </div>
    <span v-if="tableProblem" class="text-xs text-error" data-people-error>{{ tableProblem }}</span>
    <div class="flex flex-wrap items-center gap-x-4 gap-y-1">
      <UButton label="Add person" icon="i-lucide-plus" color="secondary" variant="link" size="sm" class="px-0" :disabled="people.length >= PEOPLE_LIMITS.people" data-add-person @click="add" />
      <span class="text-xs text-muted" data-people-never-started>A person is never started: no session, worktree or agent. They own decisions and outcomes.</span>
    </div>
  </div>
</template>
