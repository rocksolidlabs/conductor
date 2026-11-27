<script setup lang="ts">
import type { CommitChangeRow } from '~/utils/commits'
import type { ChatThread } from '~/composables/useChat'
import type { SessionInfo, ShareLink } from '~/composables/useSessions'
import type { ActivityEntry, ChatMessage, FileRequester, Role, ViewerInfo } from '~/utils/protocol'
import type { FileTarget } from '~/components/FileBrowser.vue'
import type { ChangeRow } from '~/utils/changes'
import { avatarTone } from '~/utils/avatar'
import { initials, relativeTime } from '~/utils/sessions'
import { COLOR_TEXT, entryIcon, fileOpWords, fileToolWords, linkableUrl } from '~/utils/events'
import { inspectorItems, type FilesView, type InspectorView } from '~/utils/inspector'
import { changesTitle } from '~/utils/changes'
import { commitsHead } from '~/utils/commits'
import { opIcon, touchedWords } from '~/utils/touched'

/**
 * The session page's inspector (design 4h, #301): one rail on its outer
 * edge with People (who is here, and the people's chat when the owner takes
 * chat), Explorer, Changes, Commits and Activity, the panel beside it showing the view picked, or
 * collapsed to the rail alone, whose pills still count. Each view opens
 * with a two-line title. Touched is an Activity filter (All · Files ·
 * Tools). The git status and log are polled here (useRepoStatus), so the
 * Changes and Commits pills are right whatever the panel shows.
 */
const props = defineProps<{
  session: SessionInfo
  role: Role
  viewers: ViewerInfo[]
  activity: ActivityEntry[]
  links: ShareLink[]
  request: FileRequester
  rawUrl?: (path: string) => string | null
  /** The session's chat (design 2a); the item shows once the owner said it takes chat. */
  chat?: ChatThread
  chatUnread?: number
  chatOffline?: boolean
  ended?: boolean
}>()
const emit = defineEmits<{ newLink: []; revoke: [link: ShareLink]; chatSend: [text: string, to: string]; chatSendToAgent: [ref: string]; chatRetry: [nonce: string]; chatAnswer: [m: ChatMessage, index: number]; openFile: [target: FileTarget]; openDiff: [change: ChangeRow, against: { top: string; branch?: string; base?: string; baseId?: string }]; openCommitDiff: [change: CommitChangeRow, commit: { sha: string; short: string; parent: string }] }>()

const view = defineModel<InspectorView>('view', { default: 'people' })
const open = defineModel<boolean>('open', { default: true })
const target = defineModel<FileTarget | null>('target', { default: null })
const url = defineModel<string | null>('url', { default: null })

// The Files pane's view follows the rail's; People, Activity and Chat leave it where it was.
const section = computed<FilesView>({
  get: () => (view.value === 'changes' || view.value === 'commits' ? view.value : 'explorer'),
  set: (v) => (view.value = v),
})
const shown = computed<InspectorView | null>(() => (open.value ? view.value : null))
const repo = useRepoStatus(
  props.request,
  () => props.session.cwd,
  () => undefined,
  () => shown.value,
)
const touched = useTouched(
  props.request,
  () => props.session.cwd,
  () => props.activity,
)

// Activity's filter (4h): All, Files (the touched rows, the whole session), Tools (tool uses and refusals).
const filter = ref<'all' | 'files' | 'tools'>('all')
watch(
  () => shown.value === 'activity' && filter.value === 'files',
  (files) => {
    if (files) void touched.load()
  },
)
// The Explorer's dots: an index the first read could not get (the terminal still connecting) is asked for again as the Explorer opens.
watch(
  () => shown.value === 'explorer',
  (explorer) => {
    if (explorer && touched.index.value === null) void touched.load()
  },
)
// What the page's command palette lists under `:` (#302): the status's changes and the commits the inspector polls.
defineExpose({ changes: repo.changes, commits: repo.commits, status: repo.status })

const now = ref(Date.now())
let tick: number | undefined
onMounted(() => (tick = window.setInterval(() => (now.value = Date.now()), 1000)))
onBeforeUnmount(() => window.clearInterval(tick))

function typing(v: ViewerInfo) {
  return !!v.lastInputAt && now.value - Date.parse(v.lastInputAt) < 4000
}

function who(v: ViewerInfo) {
  const parts: string[] = []
  if (v.link) parts.push(`via “${v.link}”`)
  else parts.push(v.role === 'control' ? 'Owner' : 'Viewer')
  parts.push(typing(v) ? 'typing' : relativeTime(v.since, now.value))
  return parts.join(' · ')
}

const liveLinks = computed(() => props.links.filter((l) => !l.revoked))
/** With the people's chat, who is here and the links are folded under People's head until it is pressed. */
const roster = ref(false)
const here = computed(() => props.viewers.length || props.session.viewers)
const peopleSub = computed(() => `${here.value} here now · ${liveLinks.value.length} share ${liveLinks.value.length === 1 ? 'link' : 'links'}`)

function expiry(l: ShareLink) {
  if (!l.expiresAt) return 'never expires'
  const ms = Date.parse(l.expiresAt) - now.value
  if (ms <= 0) return 'expired'
  const min = Math.floor(ms / 60000)
  if (min < 60) return `expires in ${min}m`
  const h = Math.floor(min / 60)
  if (h < 24) return `expires in ${h}h ${min % 60}m`
  return `expires in ${Math.floor(h / 24)}d`
}

// Newest first. Text only: nothing an agent sends is rendered as HTML, and a
// URL is a link only when linkableUrl allows it.
const activityRows = computed(() =>
  [...props.activity]
    .reverse()
    .filter((e) => filter.value !== 'tools' || e.type === 'tool_use' || e.type === 'tool_denied')
    .map((e) => {
      const icon = entryIcon(e)
      return { e, text: describe(e), icon: icon.icon, color: COLOR_TEXT[icon.color], link: linkableUrl(e.url) }
    }),
)
const toolCount = computed(() => props.activity.filter((e) => e.type === 'tool_use' || e.type === 'tool_denied').length)
const activitySub = computed(() =>
  filter.value === 'files' ? `newest first · ${touched.rows.value.length} since the session started` : filter.value === 'tools' ? `newest first · ${toolCount.value} tool uses` : `newest first · ${props.activity.length} entries`,
)
function describe(e: ActivityEntry) {
  const by = e.byName ? `${e.byName} ` : ''
  const msg = e.message ? `: ${e.message}` : ''
  switch (e.type) {
    case 'input':
      // A line Conductor typed (a crew's prompt, a broadcast) is sent; a reply to a question answered.
      return e.op === 'typed' ? `${by}sent${msg}` : `${by}answered${msg}`
    case 'join':
      return `${by}joined`
    case 'leave':
      return `${by}left`
    case 'attention':
      return e.message || 'attention changed'
    case 'link':
      return e.message || 'link changed'
    case 'status':
      return e.message || 'status changed'
    case 'progress':
      return e.message || 'progress'
    case 'artifact':
      return e.message || (e.url ? '' : 'artifact')
    case 'handoff':
      return `handed off${e.to ? ` to ${e.to}` : ''}${msg}`
    case 'tool_use':
      return `used ${e.tool || 'a tool'}${msg}`
    case 'tool_denied':
      return `${e.tool || 'a tool'} denied${msg}`
    case 'error':
      return `${e.tool ? `${e.tool}: ` : ''}${e.message || 'error'}`
    case 'file':
      return `${fileOpWords(e.op)} ${e.path ?? ''}${e.tool ? ` · ${fileToolWords(e.tool)}` : ''}`
  }
  return e.message || e.type
}

const items = computed(() =>
  inspectorItems({
    viewers: here.value,
    changes: repo.changes.value.length,
    commits: repo.commits.value.length,
    unread: props.chatUnread ?? 0,
    chat: !!props.chat?.capable,
    subs: {
      people: peopleSub.value,
      changes: repo.notRepo.value ? 'not a repository' : changesTitle(repo.changes.value.length),
      commits: repo.logNotRepo.value ? 'not a repository' : commitsHead(repo.commits.value.length, repo.log.value),
      activity: activitySub.value,
    },
  }),
)
</script>

<template>
  <!-- overflow-x-clip (#325): nothing of the rail or the panel widens the page, whatever a badge or a long name does. -->
  <aside class="flex h-full min-h-0 flex-none overflow-x-clip border-l border-default bg-default" data-inspector :data-inspector-open="open ? 'true' : 'false'">
    <div v-if="open" class="flex h-full min-h-0 w-[332px] flex-col" data-inspector-panel>
      <!-- People (docs/plans/chat-or-terminal.md, design 11): the people's chat and who is here, one view. With the chat, who is here and
           the share links fold under the head, which opens them; without it they are the view. -->
      <template v-if="view === 'people' || view === 'chat'">
        <component
          :is="chat?.capable ? 'button' : 'div'"
          :type="chat?.capable ? 'button' : undefined"
          class="flex w-full items-center gap-2 border-b border-default px-3 py-2 text-left text-xs"
          :class="chat?.capable && 'hover:bg-elevated/60'"
          :aria-expanded="chat?.capable ? roster : undefined"
          data-inspector-head="people"
          @click="chat?.capable && (roster = !roster)"
        >
          <UIcon name="i-lucide-users" class="size-3.5 flex-none text-muted" />
          <div class="flex min-w-0 flex-1 flex-col">
            <span class="text-[13px] font-semibold text-highlighted">People</span>
            <span class="truncate text-[11px] text-muted tabular-nums" data-inspector-sub>{{ peopleSub }}</span>
          </div>
          <UIcon v-if="chat?.capable" :name="roster ? 'i-lucide-chevron-up' : 'i-lucide-chevron-down'" class="size-3.5 flex-none text-muted" aria-hidden="true" />
        </component>
        <div v-if="!chat?.capable || roster" class="min-h-0 overflow-y-auto p-4 flex flex-col gap-6" :class="chat?.capable ? 'max-h-[45%] flex-none border-b border-default' : 'flex-1'" data-people-roster>
          <section class="flex flex-col gap-2.5">
            <h3 class="text-[11px] font-semibold uppercase tracking-wider text-muted">Here now · {{ here }}</h3>
            <p v-if="!viewers.length" class="text-xs text-muted">Names arrive once everyone reconnects.</p>
            <div v-for="v in viewers" :key="v.id" class="flex items-center gap-2.5">
              <span class="grid size-7 place-items-center rounded-full text-[11px] font-semibold flex-none" :class="[avatarTone(v.name), typing(v) && 'ring-2 ring-warning ring-offset-2 ring-offset-default']">{{ initials(v.name) }}</span>
              <div class="min-w-0 flex-1 flex flex-col">
                <span class="truncate text-sm font-medium">{{ v.name }}</span>
                <span class="truncate text-xs text-muted">{{ who(v) }}</span>
              </div>
              <UBadge :label="v.role === 'control' ? 'Control' : 'View'" :color="v.role === 'control' ? 'primary' : 'neutral'" :variant="v.role === 'control' ? 'subtle' : 'outline'" size="sm" />
            </div>
          </section>

          <section class="flex flex-col gap-2.5">
            <h3 class="flex items-center text-[11px] font-semibold uppercase tracking-wider text-muted">
              Share links
              <UButton label="New link" icon="i-lucide-plus" size="xs" variant="link" color="secondary" class="ml-auto normal-case tracking-normal" @click="emit('newLink')" />
            </h3>
            <!-- What a link is, said where the list is empty (#365). -->
            <p v-if="!liveLinks.length" class="text-xs text-muted" data-share-links-empty>No share links yet. A link lets someone watch this session from anywhere, or take control of it, until it expires or you revoke it.</p>
            <div v-for="l in liveLinks" :key="l.id" class="flex flex-col gap-1 rounded-md border border-default px-3 py-2.5">
              <div class="flex items-center gap-2">
                <span class="text-sm font-medium truncate">{{ l.label || 'unlabelled' }}</span>
                <UBadge :label="l.role === 'control' ? 'Control' : 'View'" :color="l.role === 'control' ? 'primary' : 'neutral'" :variant="l.role === 'control' ? 'subtle' : 'outline'" size="sm" />
                <UButton label="Revoke" size="xs" variant="link" color="secondary" class="ml-auto" @click="emit('revoke', l)" />
              </div>
              <span class="text-[11px] text-muted tabular-nums">{{ expiry(l) }}<template v-if="l.active !== undefined"> · {{ l.active }} using</template></span>
            </div>
          </section>
        </div>
        <ChatPanel v-if="chat?.capable" class="min-h-0 flex-1" bare :thread="chat" :role="role" :ended="ended" :offline="chatOffline" :viewers="viewers" @send="(text, to) => emit('chatSend', text, to)" @send-to-agent="emit('chatSendToAgent', $event)" @retry="emit('chatRetry', $event)" @answer="(m, i) => emit('chatAnswer', m, i)" />
      </template>

      <div v-else-if="view === 'explorer' || view === 'changes' || view === 'commits'" class="flex-1 min-h-0 flex flex-col">
        <FileBrowser v-model:section="section" v-model:target="target" v-model:url="url" :request="request" :cwd="session.cwd" :raw-url="rawUrl" :activity="activity" :repo="repo" :touched="touched.rows.value" external class="flex-1 min-h-0" @open="emit('openFile', $event)" @open-diff="(c, a) => emit('openDiff', c, a)" @open-commit-diff="(c, k) => emit('openCommitDiff', c, k)" />
      </div>

      <template v-else>
        <div class="flex items-center gap-2 border-b border-default px-3 py-2 text-xs" data-inspector-head="activity">
          <UIcon name="i-lucide-activity" class="size-3.5 flex-none text-muted" />
          <div class="flex min-w-0 flex-1 flex-col">
            <span class="text-[13px] font-semibold text-highlighted">Activity</span>
            <span class="truncate text-[11px] text-muted tabular-nums" data-inspector-sub :data-files-touched-head="filter === 'files' ? '' : undefined">{{ activitySub }}</span>
          </div>
        </div>
        <!-- Touched, folded in as a filter (4h): Files lists the whole session's touched files, Tools the tool uses and refusals. -->
        <div class="flex items-center gap-1.5 border-b border-default px-3 py-2" data-activity-filters>
          <UButton label="All" size="xs" :variant="filter === 'all' ? 'solid' : 'outline'" :color="filter === 'all' ? 'neutral' : 'neutral'" data-activity-filter="all" @click="filter = 'all'" />
          <UButton size="xs" :variant="filter === 'files' ? 'solid' : 'outline'" color="neutral" data-activity-filter="files" @click="filter = 'files'">
            Files<span v-if="touched.rows.value.length" class="ml-1 rounded-full bg-elevated px-1.5 text-[10px] font-semibold leading-4 text-highlighted" data-files-touched-count>{{ touched.rows.value.length }}</span>
          </UButton>
          <UButton label="Tools" size="xs" :variant="filter === 'tools' ? 'solid' : 'outline'" color="neutral" data-activity-filter="tools" @click="filter = 'tools'" />
        </div>
        <div v-if="filter === 'files'" class="flex-1 min-h-0 overflow-y-auto">
          <p v-if="!touched.rows.value.length" class="p-6 text-sm text-muted">Nothing yet. The files the agent reads, edits, writes and deletes list here as its hooks report them.</p>
          <ul v-else class="py-1" data-files-touched>
            <li v-for="r in touched.rows.value" :key="r.abs">
              <button type="button" class="flex w-full items-center gap-2 px-3 py-1 text-left text-sm hover:bg-elevated" :data-touched="r.abs" :data-touched-op="r.op" @click="emit('openFile', { path: r.abs })">
                <UIcon :name="opIcon(r.op)" class="size-3.5 flex-none text-muted" />
                <span class="min-w-0 flex-1 truncate font-mono"><span class="text-muted">{{ r.dir }}</span>{{ r.name }}</span>
                <span class="flex-none text-[11px] text-muted">{{ touchedWords(r) }}</span>
              </button>
            </li>
          </ul>
          <p class="border-t border-default px-3 py-1.5 text-[11px] text-muted" data-files-touched-foot>The same events are in Activity and in the feed in History, quiet there by default.</p>
        </div>
        <div v-else class="flex-1 min-h-0 overflow-y-auto p-4">
          <p v-if="!activityRows.length" class="text-xs text-muted">Nothing yet.</p>
          <ol v-else class="flex flex-col gap-2 text-sm">
            <li v-for="(r, i) in activityRows" :key="i" class="flex gap-2.5" :data-activity="r.e.type">
              <span class="font-mono text-[11px] text-muted pt-0.5 flex-none">{{ new Date(r.e.at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) }}</span>
              <UIcon :name="r.icon" class="mt-0.5 size-4 flex-none" :class="r.color" aria-hidden="true" />
              <span class="min-w-0 break-words">
                {{ r.text }}
                <template v-if="r.e.url">
                  <a v-if="r.link" :href="r.link" target="_blank" rel="noopener noreferrer" class="break-all text-primary underline underline-offset-2">{{ r.e.url }}</a>
                  <span v-else class="break-all font-mono text-xs text-muted">{{ r.e.url }}</span>
                </template>
              </span>
            </li>
          </ol>
        </div>
      </template>
    </div>
    <InspectorRail v-model:view="view" v-model:open="open" :items="items" />
  </aside>
</template>
