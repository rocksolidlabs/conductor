<script setup lang="ts">
import type { DispatchTicket, RelatedTicket, TicketRelation, TicketSummary } from '~/composables/useDispatch'
import { RELATION_CHOICES, projectPath, relationExcluded, relationGroups, statusWord, ticketPath, type RelationChoice, type RelationRow } from '~/utils/dispatch'

/**
 * A ticket among its project's (docs/plans/dispatch.md, PR-10; "Hierarchy, relations and references", "Deleting a ticket"): its
 * children, the first 50 by number with their count and the rest in the list; what it blocks, what blocks it ("depends on" is a
 * blocks read from the other end) and what it relates to, each with its other end's key, title and status. A reference to a deleted
 * ticket is kept, inactive, never detached, and said so: its key opens the deleted ticket's page, where Undelete is. Blocked is said
 * from the live blockers neither done nor cancelled. A relation is added with a ticket picked by key or title (TicketPicker) and
 * removed from either end, each one operation; a blocks that would close a cycle is refused in the store's words.
 */
const props = defineProps<{
  ticket: DispatchTicket
  project: string
  locked: boolean
  add: (choice: RelationChoice, other: TicketSummary) => Promise<boolean>
  remove: (relation: TicketRelation) => Promise<boolean>
}>()

const groups = computed(() => relationGroups(props.ticket.id, props.ticket.relations ?? []))
const children = computed(() => props.ticket.children?.tickets ?? [])
const childrenTotal = computed(() => props.ticket.children?.total ?? 0)
const choice = ref<RelationChoice>('blocks')
/** The tickets not to offer: this one, and those it holds the relation chosen with already; one of another kind may still be made. */
const exclude = computed(() => relationExcluded(props.ticket.id, props.ticket.relations ?? [], choice.value))
const adding = ref(false)
async function picked(t: TicketSummary) {
  adding.value = true
  try {
    await props.add(choice.value, t)
  } finally {
    adding.value = false
  }
}

const sections = computed(() => [
  { name: 'blocks', title: 'Blocks', rows: groups.value.blocks },
  { name: 'blocked-by', title: 'Blocked by', rows: groups.value.blockedBy },
  { name: 'relates', title: 'Related', rows: groups.value.relates },
])

function otherWords(o: RelatedTicket): string {
  return o.deletedAt ? 'deleted' : statusWord(o.status).label
}
function rowClass(r: RelationRow): string {
  return r.active ? '' : 'opacity-60'
}
</script>

<template>
  <section class="flex min-w-0 flex-col gap-3" data-ticket-relations>
    <div class="flex flex-wrap items-center gap-2">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">Children and relations</h3>
      <UBadge v-if="groups.blocking.length && !ticket.deletedAt" color="warning" variant="subtle" size="sm" icon="i-lucide-octagon-pause" data-ticket-blocked>
        Blocked by {{ groups.blocking.length === 1 ? groups.blocking[0]!.other.key : `${groups.blocking.length} open tickets` }}
      </UBadge>
    </div>

    <!-- Its children: the first 50 by number, the rest in the project's list filtered by parent. -->
    <div class="flex flex-col gap-1.5" data-ticket-children :data-count="childrenTotal">
      <span class="text-xs font-medium text-muted">Children<template v-if="childrenTotal"> <span class="font-mono text-dimmed" data-children-count>({{ childrenTotal }})</span></template></span>
      <span v-if="!children.length" class="text-sm text-dimmed">None</span>
      <ul v-else class="flex flex-col">
        <li v-for="c in children" :key="c.id" class="flex min-w-0 items-center gap-2 py-0.5 text-sm" :data-child="c.key">
          <UIcon :name="statusWord(c.status).icon" class="size-3.5 flex-none text-muted" />
          <NuxtLink :to="ticketPath(c.key)" class="flex min-w-0 items-baseline gap-2 hover:underline">
            <span class="flex-none font-mono text-xs text-muted">{{ c.key }}</span>
            <span class="truncate">{{ c.title }}</span>
          </NuxtLink>
        </li>
      </ul>
      <NuxtLink v-if="childrenTotal > children.length" :to="{ path: projectPath(project), query: { parent: ticket.key } }" class="text-xs text-muted hover:underline" data-children-all>All {{ childrenTotal }} children in the list</NuxtLink>
    </div>

    <div v-for="s in sections" :key="s.name" class="flex flex-col gap-1.5" :data-relations="s.name" :data-count="s.rows.length">
      <span class="text-xs font-medium text-muted">{{ s.title }}</span>
      <span v-if="!s.rows.length" class="text-sm text-dimmed">None</span>
      <ul v-else class="flex flex-col">
        <li v-for="r in s.rows" :key="r.relation.id" class="flex min-w-0 items-center gap-2 py-0.5 text-sm" :class="rowClass(r)" :data-relation="r.other.key" :data-relation-active="r.active ? 'yes' : 'no'">
          <UIcon :name="r.active ? statusWord(r.other.status).icon : 'i-lucide-trash-2'" class="size-3.5 flex-none text-muted" />
          <NuxtLink :to="ticketPath(r.other.key)" class="flex min-w-0 items-baseline gap-2 hover:underline">
            <span class="flex-none font-mono text-xs text-muted">{{ r.other.key }}</span>
            <span class="truncate" :class="r.active ? '' : 'line-through'">{{ r.other.title }}</span>
          </NuxtLink>
          <span class="flex-none text-xs text-dimmed" data-relation-state>{{ otherWords(r.other) }}</span>
          <UButton v-if="!locked && r.active" icon="i-lucide-x" size="xs" color="neutral" variant="ghost" class="ml-auto" :aria-label="`Remove the relation with ${r.other.key}`" data-relation-remove @click="remove(r.relation)" />
        </li>
      </ul>
    </div>
    <p v-if="ticket.relationsTotal > (ticket.relations?.length ?? 0)" class="text-xs text-dimmed">The first {{ ticket.relations.length }} of {{ ticket.relationsTotal }} relations are shown.</p>

    <div v-if="!locked" class="flex flex-col gap-2 sm:flex-row sm:items-center" data-relation-add>
      <USelect v-model="choice" :items="RELATION_CHOICES.map((c) => ({ label: c.label, value: c.value, icon: c.icon }))" size="sm" class="sm:w-40" aria-label="The relation" data-relation-kind />
      <TicketPicker :project="project" :exclude="exclude" :disabled="adding" placeholder="A ticket of the project…" class="min-w-0 flex-1" data-relation-to @pick="picked" />
    </div>
  </section>
</template>
