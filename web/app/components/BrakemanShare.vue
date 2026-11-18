<script setup lang="ts">
import type { RunInfo } from '~/composables/useSessions'
import type { BrakemanDigest, BrakemanRequest, BriefItem } from '~/utils/brakeman'
import { SHARE_NONE, SHARE_WHAT, type ShareCrew, forLine, shareCrews, shareNoTicket, shareRuns, shareTickets } from '~/utils/brakemanShare'
import { responsibilityWords } from '~/utils/crews'
import { personPath } from '~/utils/people'

/**
 * Share a brief with a crew's people (issue #1012): the person's own sending. The dialog names the people of the crews whose runs the
 * brief is about, as those runs were launched (a choice of one crew when there are more), says in one fixed sentence that Conductor
 * sends nothing, and gives the brief as Markdown with their names on its first line: Copy for them, to the person's clipboard, and
 * Comment on the run's ticket, through today's comment dialog under the person's name. Conductor keeps no record of a share and makes
 * no link to a brief. Its only read is a run the page does not hold, once (GET /api/runs/{run}); a run that cannot be read names no one.
 */
const props = defineProps<{
  request: Pick<BrakemanRequest, 'id' | 'kind' | 'subjects'>
  digest?: Pick<BrakemanDigest, 'about'>
  items: readonly BriefItem[]
  /** The brief as Markdown, with the "For …" line first when one is given. */
  markdown: (addressed?: string) => string
  /** The tickets the brief names, which the comment offers after the run's own. */
  tickets: readonly string[]
  quotes?: boolean
}>()
const open = defineModel<boolean>('open', { default: false })

const attention = useAttention()
const sessions = useSessions()
const copy = useCopy()

const crews = ref<ShareCrew[]>([])
const reading = ref(false)
const crewId = ref('')
const crew = computed(() => crews.value.find((c) => c.crewId === crewId.value) ?? crews.value[0])

/** Reads the brief's runs: the page's own copy when it holds one, else once from the server; a run that cannot be read names no one. */
async function readCrews() {
  reading.value = true
  const ids = shareRuns(props.request, props.digest, props.items)
  const runs = await Promise.all(ids.map((id): Promise<RunInfo | null> => (attention.runOf(id) ? Promise.resolve(attention.runOf(id)!) : sessions.getRun(id).catch(() => null))))
  crews.value = shareCrews(runs)
  if (!crews.value.some((c) => c.crewId === crewId.value)) crewId.value = crews.value[0]?.crewId ?? ''
  reading.value = false
}
watch(open, (o) => o && void readCrews(), { immediate: true })

const addressed = computed(() => props.markdown(forLine(crew.value)))
const keys = computed(() => shareTickets(crew.value, props.tickets))
const commentLabel = computed(() => (crew.value?.ticket ? `Comment on ${crew.value.ticket}…` : 'Comment on a ticket…'))

function copyForThem() {
  const c = crew.value
  void copy(addressed.value, c ? `The brief is copied for ${c.name}'s people` : 'The brief is copied')
}
const commenting = ref(false)
function comment() {
  open.value = false
  commenting.value = true
}
</script>

<template>
  <UModal v-model:open="open" title="Share with a crew's people" description="Copy for them, or comment on a ticket, with their names on its first line.">
    <template #body>
      <div class="flex min-w-0 flex-col gap-3" data-share-dialog :data-share-reading="reading ? '' : undefined">
        <p v-if="reading" class="text-sm text-muted">Reading the brief's runs…</p>
        <template v-else-if="crew">
          <div v-if="crews.length > 1" class="flex flex-wrap gap-1.5" role="group" aria-label="The crew">
            <UButton
              v-for="c in crews"
              :key="c.crewId"
              :label="c.name"
              size="sm"
              color="neutral"
              :variant="crew.crewId === c.crewId ? 'subtle' : 'outline'"
              :aria-pressed="crew.crewId === c.crewId ? 'true' : 'false'"
              class="max-sm:min-h-11"
              :data-share-pick="c.crewId"
              @click="crewId = c.crewId"
            />
          </div>
          <div class="flex min-w-0 flex-col gap-1.5" :data-share-crew="crew.crewId">
            <span class="text-xs font-medium text-muted">{{ `${crew.name}'s people` }}</span>
            <div class="flex min-w-0 flex-wrap gap-1.5" data-share-people>
              <UButton
                v-for="p in crew.people"
                :key="p.name"
                :to="personPath(p.name)"
                :label="responsibilityWords(p.responsibilities) ? `${p.name} · ${responsibilityWords(p.responsibilities)}` : p.name"
                size="xs"
                color="neutral"
                variant="outline"
                class="min-w-0 max-w-full max-sm:min-h-11"
                :ui="{ label: 'truncate' }"
                :data-share-person="p.name"
              />
            </div>
            <p v-if="!crew.ticket" class="text-xs text-muted" data-share-no-ticket>{{ shareNoTicket(crew.name) }}</p>
          </div>
        </template>
        <p v-else class="text-sm text-muted" data-share-none>{{ SHARE_NONE }}</p>
        <p class="text-xs text-muted" data-share-what>{{ SHARE_WHAT }}</p>
      </div>
    </template>
    <template #footer>
      <div class="flex w-full flex-wrap justify-end gap-2">
        <UButton label="Copy for them" icon="i-lucide-clipboard-copy" color="neutral" variant="outline" class="max-sm:min-h-11" :disabled="reading" data-share-copy @click="copyForThem" />
        <UButton :label="commentLabel" icon="i-lucide-message-square-plus" class="max-sm:min-h-11" :disabled="reading" data-share-comment @click="comment" />
      </div>
    </template>
  </UModal>
  <BrakemanCommentTicket v-model:open="commenting" :text="addressed" :keys="keys" :pick="crew?.ticket" what="The brief" :quotes="quotes" />
</template>
