<script setup lang="ts">
import type { FileTarget } from '~/components/FileBrowser.vue'
import type { ChangeRow } from '~/utils/changes'
import type { CommitChangeRow } from '~/utils/commits'
import type { ActivityEntry, FileRequester } from '~/utils/protocol'
import { filesItems, type FilesView, type InspectorView } from '~/utils/inspector'
import { changesTitle } from '~/utils/changes'
import { commitsHead } from '~/utils/commits'

/**
 * The Files aside of the Yard's focused tile and of the join page (design
 * 4g; 4h, #301): the three Files views on a rail of their own (Explorer,
 * Changes, Commits), the git status and log polled here so the rail's
 * pills are right, the touched files' dots in the Explorer. The rail cannot
 * collapse: the asides have their own buttons for that.
 */
const props = defineProps<{
  request: FileRequester
  cwd?: string
  rawUrl?: (path: string) => string | null
  base?: string
  /** The session's activity: its `file` entries mark the touched files in the Explorer. */
  activity?: ActivityEntry[]
}>()
const emit = defineEmits<{ open: [target: FileTarget]; openDiff: [change: ChangeRow, against: { top: string; branch?: string; base?: string; baseId?: string }]; openCommitDiff: [change: CommitChangeRow, commit: { sha: string; short: string; parent: string }] }>()

// The view shown: its own, or the one a page that opens the pane on Changes or Commits binds (the session's Files sheet below xl).
const section = defineModel<FilesView>('section', { default: 'explorer' })
const view = computed<InspectorView>({ get: () => section.value, set: (v) => (section.value = v === 'changes' || v === 'commits' ? v : 'explorer') })
const open = ref(true)
const repo = useRepoStatus(
  props.request,
  () => props.cwd,
  () => props.base,
  () => section.value,
)
const touched = useTouched(
  props.request,
  () => props.cwd,
  () => props.activity,
)
// The Explorer's dots: an index the first read could not get is asked for again as the Explorer shows.
watch(
  () => section.value === 'explorer',
  (explorer) => {
    if (explorer && touched.index.value === null) void touched.load()
  },
)
const items = computed(() =>
  filesItems({
    changes: repo.changes.value.length,
    commits: repo.commits.value.length,
    subs: {
      changes: repo.notRepo.value ? 'not a repository' : changesTitle(repo.changes.value.length),
      commits: repo.logNotRepo.value ? 'not a repository' : commitsHead(repo.commits.value.length, repo.log.value, props.base),
    },
  }),
)
</script>

<template>
  <div class="flex h-full min-h-0 w-full" data-files-pane>
    <FileBrowser v-model:section="section" :request="request" :cwd="cwd" :raw-url="rawUrl" :base="base" :repo="repo" :touched="touched.rows.value" :activity="activity" external class="flex-1 min-h-0 min-w-0" @open="emit('open', $event)" @open-diff="(c, a) => emit('openDiff', c, a)" @open-commit-diff="(c, k) => emit('openCommitDiff', c, k)" />
    <InspectorRail v-model:view="view" v-model:open="open" :items="items" :collapsible="false" />
  </div>
</template>
