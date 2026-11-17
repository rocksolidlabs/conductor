<script setup lang="ts">
import type { AgentInfo } from '~/composables/useSessions'
import { QUICK_START_URL } from '~/utils/about'
import { agentIcon } from '~/utils/agentIcons'
import { pageRequests, settleChecks, welcomeAgents } from '~/utils/agents'
import { EXAMPLES_LINE } from '~/utils/crews'
import { welcomeTips } from '~/utils/welcome'
import { navTiles } from '~/utils/pages'

// The first-run welcome (#359; the UX review's P17): what Conductor is in one sentence, then three steps, the first filled (the
// agents this machine has, each with its version), and "Where things are": each page of the nav with its sentence from the pages'
// table, the one the nav's tooltips say. It shows where the server has no session at all: the home, and the phone's Sessions page.
withDefaults(defineProps<{ /** Drawn inside a list (the phone's Sessions page): no room of its own around the card. */ inList?: boolean }>(), { inList: false })

const api = useSessions()
const toast = useToast()
const router = useRouter()
const launch = useLaunchModal()
const desktop = useDesktop()
const brakeman = useBrakeman()

const inbox = useInbox()
/**
 * The pages of the full sidebar's foot, as it draws them: its tiles in order (Brakeman's while the server has it, no Needs you on a
 * switchyard), then the pages under its More (Settings in the desktop app).
 */
const where = computed(() => {
  const t = navTiles({ brakeman: brakeman.available.value, desktop: desktop.isDesktop.value, switchyard: !!inbox.state.value.unavailable })
  return [...t.tiles.flatMap((c) => (c.kind === 'page' ? [c.page] : [])), ...t.more]
})

// The agents: the server's quick answer (#704), each agent still checked filled in as its own read answers, as the Agents page does.
const catalog = ref<AgentInfo[] | null>(null)
const catalogError = ref('')
const requests = pageRequests()
async function readAgents() {
  const gen = requests.next()
  try {
    const r = await requests.live(api.catalogNow())
    if (gen.aborted) return
    catalog.value = r.agents
    void settleChecks(
      r.agents.filter((a) => a.checking || a.recheck).map((a) => a.id),
      api.catalogAgent,
      (a) => catalog.value && (catalog.value = catalog.value.map((x) => (x.id === a.id ? a : x))),
      // A check that never answered leaves the agent as it was: the dialog says more of it.
      { signal: gen, gaveUp: () => undefined },
    )
  } catch (e) {
    if (!gen.aborted) catalogError.value = (e as Error).message
  }
}
onMounted(readAgents)
onBeforeUnmount(() => requests.end())
const offered = computed(() => (catalog.value ? welcomeAgents(catalog.value) : null))

// The example crews: loaded once, then the Crews page shows them.
const loadingExamples = ref(false)
async function loadExamples() {
  loadingExamples.value = true
  try {
    const r = await api.loadExampleCrews()
    toast.add({ title: `${r.added.length} added${r.skipped.length ? `, ${r.skipped.length} already there` : ''}`, icon: 'i-lucide-package-open', color: 'success' })
    await router.push('/crews')
  } catch (e) {
    toast.add({ title: 'Load the examples', description: (e as Error).message, color: 'error' })
  } finally {
    loadingExamples.value = false
  }
}
</script>

<template>
  <div class="flex flex-1 flex-col items-center" :class="inList ? 'py-2' : 'p-4 sm:p-8'" data-welcome>
    <div class="my-auto flex w-full max-w-xl flex-col gap-5 rounded-lg border border-default bg-elevated/50 p-4 sm:p-6" data-welcome-card>
      <div class="flex items-start gap-3">
        <img src="/brand/conductor-mark.svg" alt="" class="size-8 flex-none dark:hidden" />
        <img src="/brand/conductor-mark-reversed.svg" alt="" class="hidden size-8 flex-none dark:block" />
        <div class="min-w-0">
          <h2 class="text-base font-semibold text-highlighted">Welcome to Conductor</h2>
          <p class="text-sm text-muted" data-welcome-sentence>Conductor runs your coding agents in terminals on this machine, and lets you watch, answer and share them from any browser.</p>
        </div>
      </div>

      <ol class="flex flex-col gap-4">
        <li class="flex gap-3" data-welcome-step="launch">
          <span class="grid size-6 flex-none place-items-center rounded-full bg-primary text-xs font-semibold text-inverted" aria-hidden="true">1</span>
          <div class="flex min-w-0 flex-1 flex-col gap-2">
            <h3 class="text-sm font-semibold text-highlighted" data-welcome-step-label>Launch an agent</h3>
            <p v-if="!offered && !catalogError" class="flex items-center gap-2 text-sm text-muted" data-welcome-looking>
              <UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" />Looking for agents on this machine…
            </p>
            <template v-else-if="offered?.agents.length">
              <TipButton
                v-for="(a, i) in offered.agents"
                :key="a.id"
                :tip="welcomeTips.agent(a.name)"
                :icon="agentIcon(a.icon)"
                :color="i === 0 ? 'primary' : 'neutral'"
                :variant="i === 0 ? 'solid' : 'outline'"
                block
                class="justify-start pointer-coarse:min-h-11"
                :data-welcome-agent="a.id"
                @click="launch.show({ agentId: a.id })"
              >
                <span class="min-w-0 truncate" data-welcome-agent-name>{{ a.name }}</span>
                <span v-if="a.version" class="ml-auto flex-none font-mono text-xs opacity-80" data-mono="id" data-welcome-version>{{ a.version }}</span>
                <span v-else-if="a.checking" class="ml-auto flex-none text-xs opacity-80" data-welcome-checking>checking…</span>
              </TipButton>
              <TipButton v-if="offered.rest" :tip="welcomeTips.more" :label="`and ${offered.rest} more`" color="neutral" variant="link" class="self-start px-0 pointer-coarse:min-h-11" data-welcome-more @click="launch.show()" />
            </template>
            <template v-else-if="offered">
              <p class="text-sm text-muted" data-welcome-none>No agent is installed on this machine yet.</p>
              <TipButton :tip="welcomeTips.agents" label="Agents" icon="i-lucide-bot" color="neutral" variant="outline" to="/agents" class="self-start pointer-coarse:min-h-11" data-welcome-agents />
            </template>
            <!-- The catalog not read: the dialog reads it again and says why. -->
            <TipButton v-else :tip="welcomeTips.launch" label="Launch agent" icon="i-lucide-play" block class="pointer-coarse:min-h-11" data-welcome-launch @click="launch.show()" />
          </div>
        </li>

        <li class="flex gap-3" data-welcome-step="host">
          <span class="grid size-6 flex-none place-items-center rounded-full border border-default text-xs font-semibold text-muted" aria-hidden="true">2</span>
          <div class="flex min-w-0 flex-1 flex-col gap-2">
            <h3 class="text-sm font-semibold text-highlighted" data-welcome-step-label>Bring one in from another machine</h3>
            <p class="text-sm text-muted">Run <code class="font-mono text-highlighted">conductor host</code> there; its terminal shows here.</p>
            <TipButton :tip="welcomeTips.host" label="How to bring one in" icon="i-lucide-book-open" trailing-icon="i-lucide-external-link" color="neutral" variant="outline" :to="QUICK_START_URL" target="_blank" class="self-start pointer-coarse:min-h-11" data-welcome-host />
          </div>
        </li>

        <li class="flex gap-3" data-welcome-step="crew">
          <span class="grid size-6 flex-none place-items-center rounded-full border border-default text-xs font-semibold text-muted" aria-hidden="true">3</span>
          <div class="flex min-w-0 flex-1 flex-col gap-2">
            <h3 class="text-sm font-semibold text-highlighted" data-welcome-step-label>Try a crew</h3>
            <TipButton :tip="welcomeTips.examples" label="Load the example crews" icon="i-lucide-package-open" color="neutral" variant="outline" :loading="loadingExamples" class="self-start pointer-coarse:min-h-11" data-welcome-examples @click="loadExamples" />
            <p class="text-sm text-muted" data-examples-line>{{ EXAMPLES_LINE }}</p>
          </div>
        </li>
      </ol>

      <section class="flex flex-col gap-2 border-t border-default pt-4" data-welcome-where>
        <h3 class="text-xs font-semibold uppercase tracking-wider text-muted">Where things are</h3>
        <ul class="flex flex-col gap-1.5">
          <li v-for="p in where" :key="p.id" class="flex min-w-0 gap-2 text-sm" :data-where-page="p.id">
            <UIcon :name="p.icon" class="mt-0.5 size-4 flex-none text-muted" />
            <span class="min-w-0"><span class="font-medium text-highlighted" data-where-label>{{ p.label }}</span> <span class="text-muted" data-where-about>{{ p.about }}</span></span>
          </li>
        </ul>
      </section>
    </div>
  </div>
</template>
