<script setup lang="ts">
import type { HostsReply } from '~/utils/protocol'
import {
  hostAvailabilityWords,
  cardSessions,
  coalesce,
  holderNote,
  holderWords,
  hostsReader,
  moreWords,
  platformWords,
  remoteRunView,
  remoteSection,
  remoteWords,
  runRowView,
  serverRunLine,
  sortHosts,
} from '~/utils/hosts'
import { clock } from '~/utils/syncStatus'
import { pageState } from '~/utils/pageState'
import { sessionStatus } from '~/utils/status'

// Hosts (docs/plans/hosts.md, PR-3 to PR-5): every machine this server knows, what runs and waits on each, and who holds this data
// directory's managed runs, from GET /api/hosts. Read on open, again (at most once a second) on the session, removal and run events
// the layout already streams, and every 30 s for the times it says; one read at a time, a reply older than one shown never drawn.
// Its only actions are links: nothing here starts, moves, continues or retries work.

useHead({ title: 'Hosts' })

const { request } = useApi()
const admin = useWorkbenchToken()
const attention = useAttention()
const now = useNow()

const reply = ref<HostsReply | null>(null)
const error = ref('')
const loading = ref(false)
const shownState = computed(() => pageState({ read: !!reply.value, error: error.value, count: reply.value ? 1 : 0 }))

const reader = hostsReader<HostsReply>({
  read: (signal) => {
    loading.value = true
    return request<HostsReply>('/api/hosts', { signal }).finally(() => (loading.value = false))
  },
  apply: (r) => {
    reply.value = r
    error.value = ''
  },
  fail: (e) => (error.value = e.message || 'The server did not answer.'),
})
const soon = coalesce(() => reader.refresh(), 1000)

// The events the layout streams: a session's change or removal bumps its list, a run's event its runs.
const sessionsVersion = useState<number>('attentionVersion', () => 0)
const runsVersion = useState<number>('attentionRunsVersion', () => 0)
watch([sessionsVersion, runsVersion], () => soon.ask())
watch(
  () => admin.token.value,
  () => {
    reply.value = null
    error.value = ''
    reader.bump()
  },
)
let tick: ReturnType<typeof setInterval> | undefined
onMounted(() => {
  reader.refresh()
  tick = setInterval(() => reader.refresh(), 30_000)
})
onBeforeUnmount(() => {
  soon.cancel()
  if (tick) clearInterval(tick)
  reader.end()
})

/** The sessions the page holds (the layout's live list): a card names its sessions and draws each one's state. */
const sessionsById = computed(() => new Map(attention.sessions.value.map((s) => [s.id, s])))
function listed(ids: readonly string[] | undefined) {
  return cardSessions(ids, (id) => sessionsById.value.get(id)?.name).map((c) => {
    const s = sessionsById.value.get(c.id)
    return s ? { ...c, status: sessionStatus(s, now.value) } : c
  })
}

const hosts = computed(() => sortHosts(reply.value?.hosts ?? []))
const runs = computed(() => (reply.value ? reply.value.runs.map((r) => runRowView(r, reply.value!.ledger)) : []))
const remote = computed(() => remoteSection(reply.value))
const started = computed(() => clock(reply.value?.started))
const ledgerNote = computed(() => (reply.value ? holderNote(reply.value.ledger.holder, reply.value.ledger.note) : ''))
</script>

<template>
  <UDashboardPanel id="hosts">
    <template #header>
      <UDashboardNavbar :toggle="false" title="Hosts" data-hosts-header>
        <template #right>
          <PageAbout :shown="shownState === 'content'">
            Every machine this server knows: itself, the hosts that reach it and the switchyard it publishes to, with what runs and waits on
            each, and who holds this data directory's managed runs. Runs stay with the server that holds them; nothing on this page moves or
            starts work.
          </PageAbout>
          <IconButton icon="i-lucide-refresh-cw" color="neutral" variant="outline" :loading="loading" label="Read again" data-hosts-refresh @click="reader.refresh()" />
        </template>
      </UDashboardNavbar>
    </template>
    <template #body>
      <PageEmpty v-if="shownState !== 'content'" page="hosts" :state="shownState === 'empty' ? 'reading' : shownState" thing="machines" :error="error" @again="reader.refresh()" />
      <div v-else-if="reply" class="mx-auto flex w-full max-w-5xl flex-col gap-8" data-hosts :data-hosts-at="reply.at">
        <UAlert v-if="error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" data-hosts-error />

        <section aria-labelledby="hosts-this" data-hosts-section="this">
          <h2 id="hosts-this" class="mb-2 text-[11px] font-semibold uppercase tracking-wider text-muted">This server</h2>
          <HostCard
            kind="server"
            :host-key="reply.this.key"
            :name="reply.this.name"
            :platform="reply.this.platform"
            :version="reply.this.version"
            availability="here"
            :availability-words="hostAvailabilityWords('here')"
            :sessions="reply.this.sessions"
            :decisions="reply.this.decisions"
            :run-line="serverRunLine(reply.ledger.holder, reply.runs.length)"
          >
            <div v-if="reply.this.hosted" class="mt-3 border-t border-default pt-3" data-host-hosted>
              <p class="text-sm font-medium text-highlighted">Hosted from this machine</p>
              <HostCard
                bare
                kind="host"
                :host-key="`${reply.this.key}-hosted`"
                :name="reply.this.name"
                availability="here"
                :availability-words="hostAvailabilityWords('here')"
                :sessions="reply.this.hosted.sessions"
                :session-list="listed(reply.this.hosted.sessionIds)"
                :more-sessions="reply.this.hosted.moreSessionIds"
                :where="reply.this.hosted.where"
              />
            </div>
          </HostCard>
        </section>

        <section aria-labelledby="hosts-hosts" data-hosts-section="hosts">
          <h2 id="hosts-hosts" class="text-[11px] font-semibold uppercase tracking-wider text-muted">Hosts</h2>
          <p class="mb-2 mt-1 text-sm text-muted" data-hosts-started>
            Machines running conductor host against this server. One whose sessions all left stays listed for 24 hours, since this server
            started at {{ started }}.
          </p>
          <div v-if="hosts.length" class="grid gap-3 lg:grid-cols-2">
            <HostCard
              v-for="h in hosts"
              :key="h.key"
              kind="host"
              :host-key="h.key"
              :name="h.name"
              :names="h.names"
              :platform="h.platform"
              :version="h.version"
              :user="h.user"
              :availability="h.availability"
              :availability-words="hostAvailabilityWords(h.availability, h.since)"
              :sessions="h.sessions"
              :session-list="listed(h.sessionIds)"
              :more-sessions="h.moreSessionIds"
              :where="h.where"
              run-line="Holds no runs"
            />
          </div>
          <p v-else class="rounded-md px-3 py-2 text-sm text-muted ring ring-default" data-hosts-none>No host reaches this server.</p>
          <p v-if="reply.more.hosts" class="mt-2 text-xs text-muted" data-hosts-more>{{ moreWords(reply.more.hosts, reply.more.hosts === 1 ? 'host' : 'hosts') }}</p>
        </section>

        <section v-if="remote.kind !== 'none'" aria-labelledby="hosts-other" data-hosts-section="other">
          <h2 id="hosts-other" class="mb-2 text-[11px] font-semibold uppercase tracking-wider text-muted">Other machines</h2>
          <p v-if="remote.kind === 'line'" class="rounded-md px-3 py-2 text-sm text-muted ring ring-default" data-hosts-other-line>{{ remote.words }}</p>
          <template v-else>
            <div v-if="remote.machines.length" class="grid gap-3 lg:grid-cols-2">
              <HostCard
                v-for="m in remote.machines"
                :key="m.device"
                kind="remote"
                read-only
                :host-key="m.device"
                :name="m.name"
                :platform="m.platform"
                :version="m.version"
                availability="reported"
                :availability-words="remoteWords(m.lastReport)"
                :sessions="m.summary?.sessions"
                :decisions="m.decisions"
              >
                <div v-if="m.summary?.hosts.length" class="mt-3" data-host-summary>
                  <p class="text-xs text-muted">Its hosts, as {{ m.name }} last saw them</p>
                  <ul class="mt-1 flex flex-col gap-1">
                    <li v-for="(sh, i) in m.summary.hosts" :key="`${sh.name}-${i}`" class="flex min-w-0 flex-wrap items-center gap-x-2 text-sm" data-host-summary-host>
                      <span class="min-w-0 truncate text-highlighted">{{ sh.name }}</span>
                      <span class="text-xs text-muted">{{ [platformWords(sh.platform), hostAvailabilityWords(sh.availability, sh.since)].filter(Boolean).join(' · ') }}</span>
                    </li>
                  </ul>
                  <p v-if="m.summary.moreHosts" class="mt-1 text-xs text-muted">{{ moreWords(m.summary.moreHosts, m.summary.moreHosts === 1 ? 'host' : 'hosts') }}</p>
                </div>
                <ul v-if="m.runs.length" class="mt-3 flex flex-col gap-2" data-host-remote-runs>
                  <HostRunRow v-for="r in m.runs" :key="r.id" :row="remoteRunView(r)" remote />
                </ul>
              </HostCard>
            </div>
            <p v-else class="rounded-md px-3 py-2 text-sm text-muted ring ring-default" data-hosts-other-none>The sync service names no other machine of this workspace.</p>
            <p v-if="remote.more" class="mt-2 text-xs text-muted" data-hosts-other-more>{{ moreWords(remote.more, remote.more === 1 ? 'machine' : 'machines') }}</p>
            <p v-if="remote.moreRuns" class="mt-1 text-xs text-muted" data-hosts-other-more-runs>{{ moreWords(remote.moreRuns, remote.moreRuns === 1 ? 'run of other machines' : 'runs of other machines') }}</p>
          </template>
        </section>

        <section v-if="reply.switchyard" aria-labelledby="hosts-switchyard" data-hosts-section="switchyard">
          <h2 id="hosts-switchyard" class="mb-2 text-[11px] font-semibold uppercase tracking-wider text-muted">The switchyard</h2>
          <UCard :ui="{ body: 'p-4 sm:p-4' }" data-hosts-switchyard>
            <p class="break-all text-sm text-highlighted" data-hosts-switchyard-url>{{ reply.switchyard.url }}</p>
            <p class="mt-1 text-sm text-muted" data-hosts-switchyard-counts>
              {{ reply.switchyard.publishing === 1 ? '1 session published' : `${reply.switchyard.publishing} sessions published` }} ·
              {{ reply.switchyard.connected }} connected
            </p>
          </UCard>
        </section>

        <section aria-labelledby="hosts-runs" data-hosts-section="runs">
          <h2 id="hosts-runs" class="text-[11px] font-semibold uppercase tracking-wider text-muted">Runs and who holds them</h2>
          <p class="mb-2 mt-1 text-sm" data-hosts-holder :data-holder="reply.ledger.holder">
            <span class="text-highlighted" data-hosts-holder-words>{{ holderWords(reply.ledger.holder) }}</span>
            <span v-if="ledgerNote" class="text-muted" data-hosts-holder-note>: {{ ledgerNote }}</span>
          </p>
          <ul v-if="runs.length" class="flex flex-col gap-2" data-hosts-runs>
            <HostRunRow v-for="r in runs" :key="r.id" :row="r" />
          </ul>
          <p v-else class="rounded-md px-3 py-2 text-sm text-muted ring ring-default" data-hosts-no-runs>No managed run is open on this data directory.</p>
          <p v-if="reply.more.runs" class="mt-2 text-xs text-muted" data-hosts-more-runs>{{ moreWords(reply.more.runs, reply.more.runs === 1 ? 'run' : 'runs') }}</p>
        </section>

        <p class="border-t border-default pt-4 text-sm text-muted" data-hosts-footer>Runs stay with the server that holds them; nothing on this page moves or starts work.</p>
      </div>
    </template>
  </UDashboardPanel>
</template>
