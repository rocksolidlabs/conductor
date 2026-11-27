<script setup lang="ts">
import type { AgentInfo } from '~/composables/useSessions'
import { QUICK_START_URL } from '~/utils/about'
import { pageRequests, settleChecks, welcomeOffer } from '~/utils/agents'
import { EXAMPLES_FAILED, examplesToast, welcomeTips } from '~/utils/welcome'

// The first-run welcome (#359; the UX review's P17; UX3): one sentence, one button that launches the first agent this machine has, the
// other agents on one line, and the other ways to start folded. It shows where the server has no session at all: the home, and the
// phone's Sessions page.
withDefaults(defineProps<{ /** Drawn inside a list (the phone's Sessions page): no room of its own around the card. */ inList?: boolean }>(), { inList: false })

const api = useSessions()
const toast = useToast()
const router = useRouter()
const launch = useLaunchModal()

// The agents: the server's quick answer (#704). Each agent it is still checking is read once: an answer fills it in, and one whose
// program is still looked up after that counts as not found for now (gaveUp), so the card never waits on it.
const catalog = ref<AgentInfo[] | null>(null)
const catalogError = ref('')
const gaveUp = ref<ReadonlySet<string>>(new Set())
/** The agent the main button names once it is drawn: it stays until the card reads again, unless an answer says it is gone. */
const held = ref<string>()
const requests = pageRequests()
async function readAgents() {
  const gen = requests.next()
  gaveUp.value = new Set()
  held.value = undefined
  try {
    const r = await requests.live(api.catalogNow())
    if (gen.aborted) return
    catalog.value = r.agents
    void settleChecks(
      r.agents.filter((a) => a.checking || a.recheck).map((a) => a.id),
      api.catalogAgent,
      (a) => catalog.value && (catalog.value = catalog.value.map((x) => (x.id === a.id ? a : x))),
      { signal: gen, gaveUp: (id) => (gaveUp.value = new Set([...gaveUp.value, id])), reads: 1 },
    )
  } catch (e) {
    if (!gen.aborted) catalogError.value = (e as Error).message
  }
}
onMounted(readAgents)
onBeforeUnmount(() => requests.end())
const offer = computed(() => (catalog.value ? welcomeOffer(catalog.value, { gaveUp: gaveUp.value, keep: held.value }) : null))
watch(offer, (o) => o?.state === 'found' && (held.value = o.main.id))

/** The other ways to start, folded at every visit. */
const unfolded = ref(false)
const foldId = useId()

// The example crews: loaded once, then the Crews page shows them.
const loadingExamples = ref(false)
async function loadExamples() {
  loadingExamples.value = true
  try {
    const r = await api.loadExampleCrews()
    toast.add({ ...examplesToast(r.added.length), icon: 'i-lucide-package-open' })
    await router.push('/crews')
  } catch (e) {
    toast.add({ title: EXAMPLES_FAILED, description: (e as Error).message, color: 'error' })
  } finally {
    loadingExamples.value = false
  }
}
</script>

<template>
  <div class="flex flex-1 flex-col items-center" :class="inList ? 'py-2' : 'px-4 pb-8 pt-[max(2rem,10vh)] sm:px-8'" data-welcome>
    <div class="flex w-full max-w-[30rem] flex-col gap-5 rounded-lg border border-default bg-elevated/50 p-4 sm:p-6" data-welcome-card>
      <div class="flex items-start gap-3">
        <img src="/brand/conductor-mark.svg" alt="" class="size-8 flex-none dark:hidden" />
        <img src="/brand/conductor-mark-reversed.svg" alt="" class="hidden size-8 flex-none dark:block" />
        <div class="min-w-0">
          <h2 class="text-base font-semibold text-highlighted" data-welcome-title>Welcome to Conductor</h2>
          <p class="text-balance text-sm text-muted" data-welcome-sentence>Run your coding agents here, and answer them from anywhere.</p>
        </div>
      </div>

      <div class="flex flex-col gap-3" data-welcome-start>
        <p v-if="!catalogError && (!offer || offer.state === 'looking')" class="flex items-center gap-2 text-sm text-muted" data-welcome-looking>
          <UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" />Looking for agents on this machine…
        </p>
        <template v-else-if="offer?.state === 'found'">
          <TipButton
            :tip="welcomeTips.agent(offer.main.name, offer.main.version)"
            :label="`Launch ${offer.main.name}`"
            icon="i-lucide-play"
            block
            size="lg"
            :ui="{ label: 'min-w-0 text-clip whitespace-normal text-center' }"
            class="pointer-coarse:min-h-11"
            :data-welcome-agent="offer.main.id"
            data-welcome-primary
            @click="launch.show({ agentId: offer.main.id })"
          />
          <p v-if="offer.named.length" class="flex flex-wrap items-center gap-x-1.5 text-sm text-muted" data-welcome-or>
            <span>Or:</span>
            <template v-for="(a, i) in offer.named" :key="a.id">
              <span v-if="i" aria-hidden="true">·</span>
              <TipButton
                :tip="welcomeTips.agent(a.name, a.version)"
                :label="a.name"
                color="neutral"
                variant="link"
                :ui="{ label: 'text-clip whitespace-normal text-start' }"
                class="min-h-6 px-0 py-1 underline underline-offset-2 pointer-coarse:min-h-11 pointer-coarse:min-w-11 pointer-coarse:justify-center"
                :data-welcome-agent="a.id"
                @click="launch.show({ agentId: a.id })"
              />
            </template>
            <template v-if="offer.rest">
              <span aria-hidden="true">·</span>
              <TipButton
                :tip="welcomeTips.more"
                :label="`${offer.rest} more`"
                :aria-label="`${offer.rest} more agents`"
                color="neutral"
                variant="link"
                class="min-h-6 px-0 py-1 underline underline-offset-2 pointer-coarse:min-h-11 pointer-coarse:min-w-11 pointer-coarse:justify-center"
                data-welcome-more
                @click="launch.show()"
              />
            </template>
          </p>
        </template>
        <template v-else-if="offer">
          <p class="text-sm text-muted" data-welcome-none>No agent is installed on this machine yet.</p>
          <TipButton :tip="welcomeTips.agents" label="Agents" icon="i-lucide-bot" color="neutral" variant="outline" to="/agents" class="self-start pointer-coarse:min-h-11" data-welcome-agents />
        </template>
        <!-- The catalog not read: the dialog reads it again and says why. -->
        <TipButton v-else :tip="welcomeTips.launch" label="Launch agent" icon="i-lucide-play" block class="pointer-coarse:min-h-11" data-welcome-launch @click="launch.show()" />
      </div>

      <div class="flex flex-col border-t border-default pt-2">
        <UButton
          label="Other ways to start"
          color="neutral"
          variant="ghost"
          block
          trailing-icon="i-lucide-chevron-down"
          class="-mx-2.5 w-[calc(100%+1.25rem)] justify-between text-muted pointer-coarse:min-h-11"
          :ui="{ trailingIcon: ['transition-transform', unfolded ? 'rotate-180' : ''].join(' ') }"
          :aria-expanded="unfolded ? 'true' : 'false'"
          :aria-controls="foldId"
          data-welcome-expander
          @click="unfolded = !unfolded"
        />
        <div v-if="unfolded" :id="foldId" class="flex flex-col gap-5 pt-3" data-welcome-fold>
          <section class="flex flex-col gap-2" data-welcome-way="host">
            <h3 class="text-sm font-semibold text-highlighted">An agent on another machine</h3>
            <p class="text-sm text-muted">Run <code class="font-mono text-highlighted" data-mono="command">conductor host</code> on that machine to see its terminal here.</p>
            <TipButton :tip="welcomeTips.host" label="How to bring one in" icon="i-lucide-book-open" trailing-icon="i-lucide-external-link" color="neutral" variant="outline" :to="QUICK_START_URL" target="_blank" class="self-start pointer-coarse:min-h-11" data-welcome-host />
          </section>
          <section class="flex flex-col gap-2" data-welcome-way="crew">
            <h3 class="text-sm font-semibold text-highlighted">Try a crew</h3>
            <TipButton :tip="welcomeTips.examples" label="Load the example crews" icon="i-lucide-package-open" color="neutral" variant="outline" :loading="loadingExamples" class="self-start pointer-coarse:min-h-11" data-welcome-examples @click="loadExamples" />
          </section>
        </div>
      </div>
    </div>
  </div>
</template>
