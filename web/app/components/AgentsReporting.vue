<script setup lang="ts">
import type { Integration } from '~/composables/useSessions'

/**
 * The Agents page's section "Reporting to Conductor": every hook adapter as a row with Install on this machine (IntegrationsTable,
 * each row `#reporting-<id>`, which a card that is not set up links to), and the Conductor skill. `integrations` is null until read.
 */
const props = defineProps<{
  integrations: Integration[] | null
  /** The host name the integrations' answer gives; '' when the server cannot tell. */
  host: string
  /** Why the integrations could not be read; '' when they were. */
  error: string
}>()
const emit = defineEmits<{ changed: [] }>()

const { httpBase } = useApiBase()
const list = computed(() => props.integrations ?? [])

/** Whether the server knows its user's home directory: it lists where each adapter it can install goes, and none without one. */
const homeKnown = computed(() => list.value.some((i) => i.where))

/** The machine the server runs on, where Install on this machine writes: the name the server gives itself, else the host this browser reaches it by. */
const where = computed(() => {
  if (props.host) return props.host
  try {
    return new URL(httpBase.value || location.origin).hostname || 'this server'
  } catch {
    return 'this server'
  }
})

// The skill has no install of its own: a launch puts it in place, and so do the hooks of the agents that read skills.
const skillReaders = computed(() => list.value.filter((i) => i.installsSkill))
/** Their names as prose: "Claude Code, Codex, pi and Goose". */
const skillReaderNames = computed(() => {
  const names = skillReaders.value.map((i) => i.name)
  return names.length > 1 ? `${names.slice(0, -1).join(', ')} and ${names.at(-1)}` : (names[0] ?? 'the agents that read skills')
})
// The notify commands as the skill writes them: the binary the session names in CONDUCTOR_BIN, else the conductor on PATH.
const skillCommands = [
  '"${CONDUCTOR_BIN:-conductor}" notify --event progress --message "4/7 handlers"',
  '"${CONDUCTOR_BIN:-conductor}" notify --event artifact --url https://github.com/acme/api/pull/212',
  '"${CONDUCTOR_BIN:-conductor}" notify --event handoff --to tests --message "/v1/users done"',
  'conductor skill',
]
</script>

<template>
  <section id="reporting" class="flex scroll-mt-4 flex-col gap-4" aria-labelledby="reporting-title" data-agents-reporting>
    <div>
      <h2 id="reporting-title" class="text-[11px] font-semibold uppercase tracking-wider text-muted">Reporting to Conductor</h2>
      <p class="mt-1 max-w-4xl text-sm text-muted">
        Every session gets <code>CONDUCTOR_NOTIFY_URL</code> and <code>CONDUCTOR_NOTIFY_TOKEN</code>. An agent with hooks reports through them; one without falls back to
        the terminal bell and screen patterns, which only know "needs input".
      </p>
    </div>
    <UAlert v-if="error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="`Which agents report could not be read: ${error}`" data-reporting-error />
    <IntegrationsTable v-if="integrations" :integrations="integrations" :host="where" :home-known="homeKnown" @changed="emit('changed')" />
    <p v-else-if="!error" class="text-sm text-muted" data-reporting-reading>Reading which agents report…</p>

    <section class="flex flex-col gap-3 rounded-lg p-4 ring ring-default" data-skill-card>
      <div class="flex items-center gap-2.5">
        <UIcon name="i-lucide-book-open-text" class="size-5 flex-none text-primary" />
        <span class="font-semibold text-highlighted">Conductor skill</span>
        <span class="ml-auto text-xs text-muted">any SKILL.md reader</span>
      </div>
      <p class="text-sm">
        Teaches agents to report progress, artifacts, blockers and handoffs to named crew members, not just "needs input". Every agent that reads skills gets it at its
        first launch ({{ skillReaderNames }}); <b class="text-default">Install on this machine</b> puts it there with the hooks.
      </p>
      <CodeBlock :commands="skillCommands" />
      <div v-if="skillReaders.length" class="flex flex-wrap gap-1.5">
        <UBadge v-for="i in skillReaders" :key="i.id" :label="`${i.name}: ${i.installed ? 'installed' : 'not installed'}`" color="neutral" :variant="i.installed ? 'subtle' : 'outline'" size="sm" />
      </div>
    </section>
  </section>
</template>
