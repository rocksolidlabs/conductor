<script setup lang="ts">
import type { AgentInfo } from '~/composables/useSessions'
import { QUICK_START_URL } from '~/utils/about'
import { lookAgain, onWindowsOnly, pageRequests, programsToLookFor, settleChecks, welcomeOffer } from '~/utils/agents'
import { EXAMPLES_FAILED, examplesToast, welcomeTips, welcomeWords } from '~/utils/welcome'

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
/**
 * Reads the agents. From the start (`fresh`: the first read, Read again) the card shows the looking state meanwhile; a read for Look
 * again keeps the card as it is until the answer.
 */
async function readAgents(fresh = true) {
  const gen = requests.next()
  gaveUp.value = new Set()
  held.value = undefined
  if (fresh) catalog.value = null
  catalogError.value = ''
  looked.value = false
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
    if (gen.aborted) return
    catalog.value = null
    catalogError.value = (e as Error).message
  }
}
onMounted(() => readAgents())
onBeforeUnmount(() => requests.end())
const offer = computed(() => (catalog.value ? welcomeOffer(catalog.value, { gaveUp: gaveUp.value, keep: held.value }) : null))
watch(offer, (o) => o?.state === 'found' && (held.value = o.main.id))
/** With no agent found: the one the server saw on Windows alone, from WSL, which the card names. */
const windowsOnly = computed(() => (offer.value?.state === 'none' && catalog.value ? onWindowsOnly(catalog.value) : undefined))

// Look again: the machine asked now about each program the server cannot launch, then the agents read again. None found: a line says
// so, and the focus stays on the button.
const start = useTemplateRef<HTMLElement>('start')
const looking = ref(false)
const looked = ref(false)
async function lookAgainNow() {
  if (looking.value || !catalog.value) return
  looking.value = true
  try {
    await lookAgain(programsToLookFor(catalog.value), (p) => api.checkCommand(p))
    if (requests.left()) return
    await readAgents(false)
  } finally {
    looking.value = false
  }
  if (offer.value?.state !== 'none') return
  looked.value = true
  await nextTick()
  start.value?.querySelector<HTMLElement>('[data-welcome-again]')?.focus()
}

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

      <div ref="start" class="flex flex-col gap-3" data-welcome-start>
        <!-- No answer yet: a button the main one's size where it will be, and the Or line's room under it, so nothing below moves. -->
        <template v-if="!catalogError && (!offer || offer.state === 'looking')">
          <UButton :label="welcomeWords.looking" loading block size="lg" color="neutral" variant="subtle" :ui="{ label: 'min-w-0 text-clip whitespace-normal text-center' }" class="pointer-coarse:min-h-11" data-welcome-looking />
          <div class="min-h-7 pointer-coarse:min-h-11" aria-hidden="true" />
        </template>
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
          <!-- No agent but the shell: said in plain text (a standing fact, never amber), with the two ways on. -->
          <p class="text-sm text-default" data-welcome-none :data-welcome-windows="windowsOnly ? '' : undefined">{{ windowsOnly ? welcomeWords.onWindows(windowsOnly) : welcomeWords.none }}</p>
          <div class="flex flex-wrap gap-2">
            <TipButton :tip="welcomeTips.agents" label="Choose an agent to install" icon="i-lucide-bot" to="/agents" class="pointer-coarse:min-h-11" data-welcome-agents />
            <UButton label="Look again" icon="i-lucide-refresh-cw" color="neutral" variant="outline" :loading="looking" class="pointer-coarse:min-h-11" data-welcome-again @click="lookAgainNow" />
          </div>
          <p v-if="looked" role="status" class="text-sm text-muted" data-welcome-looked>{{ welcomeWords.looked }}</p>
        </template>
        <!-- The agents not read: the server's words, the Launch dialog (which reads them itself) and a read again. -->
        <template v-else>
          <p class="flex items-start gap-2 text-sm text-warning" data-welcome-failed>
            <UIcon name="i-lucide-triangle-alert" class="mt-0.5 size-4 flex-none" />
            <span class="min-w-0">{{ welcomeWords.failed(catalogError) }}</span>
          </p>
          <div class="flex flex-wrap gap-2">
            <TipButton :tip="welcomeTips.launch" label="Launch agent" icon="i-lucide-play" class="pointer-coarse:min-h-11" data-welcome-launch @click="launch.show()" />
            <UButton label="Read again" icon="i-lucide-refresh-cw" color="neutral" variant="outline" class="pointer-coarse:min-h-11" data-welcome-read-again @click="readAgents()" />
          </div>
        </template>
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
