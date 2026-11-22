<script setup lang="ts">
import type { HostsDecisions, HostsSessions } from '~/utils/protocol'
import { hostDecisionWords, moreWords, platformWords, sessionWords, waitingLink, hostWaitingWords, whereWords, type CardSession } from '~/utils/hosts'
import { statusOfKey, type StatusView } from '~/utils/status'

/**
 * A machine on the Hosts page (docs/plans/hosts.md, PR-3): its name, its platform in words, its version and user, its availability as
 * a neutral badge (Here, Connected, Away since 14:02, Last seen 13:40), its sessions and what
 * waits there (a link to Needs you, PR-4), each session a link to its page, where its sessions run, and its run line. Availability is
 * a fact this server sees, never a session's state: the badge is neutral, its icon telling the four apart, and no status colour is
 * drawn for it. The junction mark is never drawn here. Its only actions are links.
 */
export type CardAvailability = 'here' | 'connected' | 'away' | 'seen'

const props = withDefaults(
  defineProps<{
    kind: 'server' | 'host'
    hostKey: string
    name: string
    /** How many names its sessions report, when more than one. */
    names?: number
    platform?: string
    version?: string
    user?: string
    availability: CardAvailability
    availabilityWords: string
    sessions?: HostsSessions
    /** The machine's open decisions (this server's, PR-4). */
    decisions?: HostsDecisions | null
    sessionList?: Array<CardSession & { status?: StatusView }>
    moreSessions?: number
    where?: string[]
    runLine?: string
    /** Drawn inside another card (the sessions hosted from this server's machine): no card of its own, no name line. */
    bare?: boolean
  }>(),
  { names: 0, platform: '', version: '', user: '', sessions: undefined, decisions: undefined, sessionList: () => [], moreSessions: 0, where: () => [], runLine: '', bare: false },
)

const ICON: Record<CardAvailability, string> = {
  here: 'i-lucide-house',
  connected: 'i-lucide-plug',
  away: 'i-lucide-unplug',
  seen: 'i-lucide-history',
}
const UCard = resolveComponent('UCard')
const machineIcon = computed(() => (props.kind === 'host' ? 'i-lucide-laptop' : 'i-lucide-server'))
const meta = computed(() => [platformWords(props.platform) || 'Platform not reported', props.version, props.user && `user ${props.user}`].filter(Boolean).join(' · '))
const needs = statusOfKey('needs')
const waiting = computed(() => hostWaitingWords(props.sessions?.waiting ?? 0))
const waitingTo = computed(() => waitingLink(props.sessions?.waiting, props.sessions?.waitingRef))
const decided = computed(() => hostDecisionWords(props.decisions?.open))
const decidedTo = computed(() => waitingLink(props.decisions?.open, props.decisions?.waitingRef))
</script>

<template>
  <component :is="bare ? 'div' : UCard" :data-host-card="hostKey" :data-host-kind="kind" :data-host-availability="availability" :ui="bare ? undefined : { body: 'p-4 sm:p-4' }">
    <div class="flex min-w-0 items-start gap-3">
      <UIcon v-if="!bare" :name="machineIcon" class="mt-0.5 size-5 flex-none text-muted" />
      <div class="min-w-0 flex-1">
        <template v-if="!bare">
          <div class="flex min-w-0 items-center gap-2">
            <h3 class="min-w-0 truncate font-medium text-highlighted" :title="name" data-host-name>{{ name }}</h3>
            <UBadge color="neutral" variant="subtle" :icon="ICON[availability]" :label="availabilityWords" class="ms-auto flex-none" data-host-availability-words />
          </div>
          <p class="mt-0.5 text-sm text-muted" data-host-meta>{{ meta }}</p>
        </template>
        <p v-if="names > 1" class="text-xs text-muted" data-host-names>Reported as {{ names }} names</p>
        <div v-if="sessions || decided" class="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm" :class="bare ? 'mt-1' : 'mt-2'" data-host-counts>
          <span v-if="sessions" data-host-sessions :data-count="sessions.running">{{ sessionWords(sessions) }}</span>
          <template v-if="waiting">
            <ULink v-if="waitingTo" :to="waitingTo" class="inline-flex items-center gap-1.5 hover:underline pointer-coarse:py-2.5" data-host-waiting :data-count="sessions?.waiting">
              <StatusMark :status="needs" />
              <span>{{ waiting }}</span>
            </ULink>
            <span v-else class="inline-flex items-center gap-1.5" data-host-waiting :data-count="sessions?.waiting">
              <StatusMark :status="needs" />
              <span>{{ waiting }}</span>
            </span>
          </template>
          <template v-if="decided">
            <ULink v-if="decidedTo" :to="decidedTo" class="inline-flex items-center gap-1.5 hover:underline pointer-coarse:py-2.5" data-host-decisions :data-count="decisions?.open">
              <StatusMark :status="needs" />
              <span>{{ decided }}</span>
            </ULink>
            <span v-else class="inline-flex items-center gap-1.5" data-host-decisions :data-count="decisions?.open">
              <StatusMark :status="needs" />
              <span>{{ decided }}</span>
            </span>
          </template>
        </div>
        <p v-if="where.length" class="mt-1 text-xs text-muted" data-host-where>{{ whereWords(where) }}</p>
        <ul v-if="sessionList.length" class="mt-2 flex flex-col gap-0.5" data-host-session-list>
          <li v-for="s in sessionList" :key="s.id" class="min-w-0">
            <ULink :to="`/sessions/${encodeURIComponent(s.id)}`" class="flex min-w-0 items-center gap-2 rounded-md px-1.5 py-1 text-sm hover:bg-elevated pointer-coarse:min-h-11" :data-host-session="s.id">
              <StatusMark v-if="s.status" :status="s.status" />
              <span v-if="s.known" class="min-w-0 truncate" :title="s.name">{{ s.name }}</span>
              <span v-else class="min-w-0 truncate font-mono text-xs" data-mono="id" :title="s.id">{{ s.id }}</span>
            </ULink>
          </li>
        </ul>
        <p v-if="moreSessions" class="mt-1 text-xs text-muted" data-host-more-sessions>{{ moreWords(moreSessions, moreSessions === 1 ? 'session' : 'sessions') }}</p>
        <slot />
        <p v-if="runLine" class="mt-2 text-sm text-muted" data-host-runs>{{ runLine }}</p>
      </div>
    </div>
  </component>
</template>
