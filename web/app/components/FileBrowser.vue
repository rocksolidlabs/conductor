<script setup lang="ts">
import type { ActivityEntry, FileEntry, FileHeader, FileRequester } from '~/utils/protocol'
import type { RepoStatus } from '~/composables/useRepoStatus'
import { agoWords, changesTitle, statusLetter, statusTone, type ChangeRow } from '~/utils/changes'
import { commitAgo, commitChangeRows, commitsHead, type CommitChangeRow, type CommitRow } from '~/utils/commits'
import type { FilesView } from '~/utils/inspector'
import { planRows, type PlanRow } from '~/utils/plans'
import { touchedPaths, type TouchedRow } from '~/utils/touched'
import { crumbsOf, findNode, foldCrumbs, foundRows, looksLikePath, pathQuery, resolveTyped, rootNode, setChildren, visibleRows, type TreeNode } from '~/utils/fileTree'
import { parseLocation } from '~/utils/links'

export interface FileTarget {
  path: string
  line?: number
}

/**
 * The Files pane (design 4a): the working directory as a tree as soon as
 * the pane shows, folders expanding in place with a listing fetched on the
 * first expansion, the breadcrumb from the top, a box that filters the tree
 * or opens a typed `path:line`, and the hint that paths in the terminal are
 * clickable. A file opens in the pane (until the editor area of design 4b
 * takes it): highlighted, at its line, with its own crumbs, size, copy and
 * raw buttons. A URL the agent printed previews in a sandboxed frame.
 */
const props = defineProps<{
  request: FileRequester
  /** The session's working directory: the tree's root and the breadcrumb's. */
  cwd?: string
  /** Builds a raw download URL for server sessions; null when unavailable. */
  rawUrl?: (path: string) => string | null
  /** Files open elsewhere (the editor area, design 4b): a file chosen is emitted as `open`, and the pane stays the tree. */
  external?: boolean
  /** What the Changes section counts against (design 4d): HEAD when empty; a crew member's run base. */
  base?: string
  /** The session's activity: its newest `file` entry refreshes the Plans list. */
  activity?: ActivityEntry[]
  /** The working directory's git status and log, polled by whoever owns the pane (useRepoStatus, 4h): the Changes and Commits views read it. */
  repo: RepoStatus
  /** The files the agent touched (useTouched, 4h): their dots beside the Explorer's files. */
  touched?: TouchedRow[]
}>()
const emit = defineEmits<{ open: [target: FileTarget]; openDiff: [change: ChangeRow, against: { top: string; branch?: string; base?: string; baseId?: string }]; openCommitDiff: [change: CommitChangeRow, commit: { sha: string; short: string; parent: string }] }>()

// The view (design 4h, #301): chosen on the rail outside the pane (the inspector's, or a Files aside's), never here.
const section = defineModel<FilesView>('section', { default: 'explorer' })
const dots = computed(() => touchedPaths(props.touched ?? []))
// Plans (#289): the agent's plans outside the working directory, as the session lists them (the files its agent reported in its plans
// directory), at the top of the Explorer; asked for when the pane shows, when the working directory changes and whenever the activity
// gains a file entry (the agent writing or deleting a plan is one): the newest entry, since the page keeps a bounded list whose length
// stops changing once it is full. An older session, or an agent without a plans directory: none.
const plans = ref<PlanRow[]>([])
async function loadPlans() {
  if (!props.cwd) return
  try {
    const res = await props.request(props.cwd, false, { op: 'plans' })
    if (res.header.kind === 'plans') plans.value = planRows(res.header)
  } catch {
    // Nothing listed.
  }
}
watch(
  () => props.cwd,
  () => void loadPlans(),
)
watch(
  () => props.activity?.at(-1),
  (e) => {
    if (e?.type === 'file') void loadPlans()
  },
)
// The Changes view (design 4d) and the Commits view (4e) read the status and the log the owner of the pane polls (props.repo), so
// the rail's pills count while another view shows; a commit opens to its files, a file to its diff against the commit's parent.
const { status, statusError, statusLoading, statusAt, log, logError, logLoading, notRepo, logNotRepo, changes, commits, marks, loadStatus, loadLog } = props.repo
const now = ref(Date.now())
let clock: number | undefined
const opened = reactive(new Map<string, { header: FileHeader | null; error: string }>())
async function toggleCommit(c: CommitRow) {
  if (opened.has(c.sha)) {
    opened.delete(c.sha)
    return
  }
  opened.set(c.sha, { header: null, error: '' })
  try {
    const res = await props.request(props.cwd || '.', false, { op: 'commit', rev: c.sha })
    opened.set(c.sha, { header: res.header, error: res.header.kind === 'error' ? res.header.error?.message || 'cannot read the commit' : '' })
  } catch (e) {
    opened.set(c.sha, { header: null, error: (e as Error).message })
  }
}
function openCommitChange(c: CommitRow, ch: CommitChangeRow) {
  emit('openCommitDiff', ch, { sha: c.sha, short: c.short, parent: c.parent })
}
onMounted(() => {
  clock = window.setInterval(() => (now.value = Date.now()), 1000)
  void loadPlans()
})
onBeforeUnmount(() => {
  window.clearInterval(clock)
})
function openChange(c: ChangeRow) {
  const h = status.value
  emit('openDiff', c, { top: h?.path || props.cwd || '/', branch: h?.branch, base: props.base, baseId: h?.base })
}

const target = defineModel<FileTarget | null>('target', { default: null })

/** A file to show: in the pane, or wherever the page puts it. */
function openFile(t: FileTarget) {
  if (props.external) emit('open', t)
  else target.value = t
}
const url = defineModel<string | null>('url', { default: null })

const copy = useCopy()
const loading = ref(false)
const header = ref<FileHeader | null>(null)
const text = ref('')
const imageSrc = ref('')
const html = ref('')
const errorText = ref('')
const targetLine = ref<number | undefined>()
const content = ref<HTMLElement>()

// The tree.
const root = ref<TreeNode | null>(null)
const query = ref('')
const treeError = ref('')
const treeLoading = ref(false)
let retries = 0

const HIGHLIGHT_LIMIT = 200 * 1024

const langByExt: Record<string, string> = {
  go: 'go', ts: 'typescript', tsx: 'tsx', js: 'javascript', jsx: 'jsx', mjs: 'javascript', cjs: 'javascript', vue: 'vue', py: 'python',
  rs: 'rust', java: 'java', kt: 'kotlin', rb: 'ruby', php: 'php', c: 'c', h: 'c', cc: 'cpp', cpp: 'cpp', hpp: 'cpp', cs: 'csharp',
  swift: 'swift', json: 'json', yaml: 'yaml', yml: 'yaml', toml: 'toml', md: 'markdown', sh: 'bash', bash: 'bash', zsh: 'bash',
  sql: 'sql', css: 'css', scss: 'scss', html: 'html', xml: 'xml', dockerfile: 'dockerfile', makefile: 'makefile', proto: 'proto',
  graphql: 'graphql', tf: 'hcl', svelte: 'svelte', lock: 'yaml', mod: 'go-mod', sum: 'text', ini: 'ini', cfg: 'ini', conf: 'ini', env: 'dotenv',
}

const mode = computed<'url' | 'file' | 'error' | 'tree'>(() => {
  if (url.value) return 'url'
  if (errorText.value) return 'error'
  if (header.value?.kind === 'file') return 'file'
  return 'tree'
})

const title = computed(() => {
  if (url.value) return url.value
  return header.value?.path || target.value?.path || root.value?.name || 'Files'
})

const rows = computed(() => (root.value ? visibleRows(root.value, query.value) : []))
// The filter reaches folders not opened yet: a find on the session's machine, a moment after typing stops, lists the files below
// whose name holds the words and the tree does not draw.
const found = ref<FileHeader | null>(null)
let findTimer: number | undefined
let findSeq = 0
watch(query, (q) => {
  window.clearTimeout(findTimer)
  const words = q.trim()
  // A `:line` at the end is a place to open, not a filter.
  if (!words || /:\d+(?::\d+)?$/.test(words) || !props.cwd) {
    found.value = null
    return
  }
  const seq = ++findSeq
  findTimer = window.setTimeout(async () => {
    // A path (a slash in it, #138) narrows the tree to that folder and below: the folders on the way are fetched and opened, so
    // that the folder shows with what it holds, and the find matches the path from the working directory rather than a name.
    const path = pathQuery(words)
    if (path !== null && root.value) void reveal(resolveTyped(root.value.path, path))
    try {
      const res = await props.request(words, false, { op: 'find' }) // the words ride as the path; the session's working directory is the root
      if (seq === findSeq) found.value = res.header.kind === 'find' ? res.header : null
    } catch {
      if (seq === findSeq) found.value = null
    }
  }, 300)
})
const foundBelow = computed(() => (found.value && query.value.trim() ? foundRows(found.value.path, found.value.matches ?? [], new Set(rows.value.map((n) => n.path))) : []))

/** The breadcrumb's row: scrolled to its end when the path changes, so the last crumb is in view. */
const crumbsNav = useTemplateRef<HTMLElement>('crumbsNav')
/**
 * The breadcrumb folded (#169): the working directory's own folder and, in file mode, the folders down to the file keep their
 * full names; the folders above fold into one `…` crumb that lists them, top to bottom. The full path is the row's tooltip.
 */
const folded = computed(() => {
  const all = crumbs.value
  const inside = all.filter((c) => c.inside).length
  // The working directory's own folder, plus the folders inside it in file mode; one folder above it when the path is short.
  return foldCrumbs(all, 1 + inside)
})
const fullPath = computed(() => crumbs.value.map((c) => c.label).join('/'))
const hiddenItems = computed(() =>
  folded.value.hidden.map((c) => ({ label: c.label, icon: 'i-lucide-folder', onSelect: () => copy(c.path, 'Path copied') })),
)
/** The breadcrumb: the working directory from the top; in file mode, on down to the file. */
const crumbs = computed(() => {
  const base = crumbsOf(props.cwd || '/')
  if (mode.value !== 'file' || !header.value?.path) return base.map((c) => ({ ...c, inside: false }))
  const rootPath = root.value?.path ?? (props.cwd || '').replace(/\/+$/, '')
  const p = header.value.path
  const rel = rootPath && p.startsWith(rootPath + '/') ? p.slice(rootPath.length + 1) : null
  if (rel === null) {
    const out: Array<{ label: string; path: string; inside: boolean }> = []
    let acc = ''
    for (const part of p.split('/').filter(Boolean)) {
      acc = `${acc}/${part}`
      out.push({ label: part, path: acc, inside: false })
    }
    return out
  }
  const out = base.map((c) => ({ ...c, inside: false }))
  let acc = rootPath
  for (const part of rel.split('/')) {
    acc = `${acc}/${part}`
    out.push({ label: part, path: acc, inside: true })
  }
  return out
})
// The strip shows its end: scrolled there when the path changes, and as the nav itself mounts (`@vue:mounted` on it): the header
// holding it is conditional, so a view switch (Changes, then Explorer again) makes a new nav for the same path, and a pane
// opened on Changes has none at mount (Codex's round on #315). Below the computed it reads: a watch's source runs as the watch
// is made, and read above its declaration the computed threw (#315), so the strip never scrolled to its end.
function scrollCrumbsToEnd() {
  void nextTick(() => crumbsNav.value?.scrollTo({ left: crumbsNav.value.scrollWidth }))
}
watch(() => crumbs.value.map((c) => c.path).join('\n'), scrollCrumbsToEnd)

const lines = computed(() => (html.value ? [] : text.value.split('\n')))

function fmtSize(n?: number) {
  if (n === undefined) return ''
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`
  return `${(n / 1024 / 1024).toFixed(1)} MiB`
}

function escapeHtml(s: string) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

async function highlight(source: string, path: string, line?: number) {
  html.value = ''
  const ext = (path.split('.').pop() || '').toLowerCase()
  const base = (path.split('/').pop() || '').toLowerCase()
  const lang = langByExt[base] || langByExt[ext]
  if (!lang || source.length > HIGHLIGHT_LIMIT) return
  try {
    const shiki = await import('shiki')
    const dark = document.documentElement.classList.contains('dark')
    html.value = await shiki.codeToHtml(source, {
      lang,
      theme: dark ? 'github-dark' : 'github-light',
      transformers: [
        {
          pre(node) {
            node.properties.style = ''
            node.properties.class = 'shiki'
          },
          line(node, n) {
            node.properties['data-line'] = n
            node.children.unshift({ type: 'element', tagName: 'span', properties: { class: 'line-no' }, children: [{ type: 'text', value: String(n) }] })
            if (line === n) node.properties.class = `${node.properties.class ?? ''} target`.trim()
            const cls = String(node.properties.class ?? '')
            node.properties.class = cls.includes('line') ? cls : `line ${cls}`.trim()
          },
        },
      ],
    })
  } catch {
    html.value = ''
  }
}

/** Lists a folder of the tree (the root, or one expanded), keeping what was known. */
async function listFolder(node: TreeNode): Promise<boolean> {
  node.loading = true
  node.error = undefined
  try {
    const res = await props.request(node.path, false)
    if (res.header.kind === 'dir') {
      setChildren(node, res.header.entries ?? [])
      return true
    }
    node.loading = false
    node.error = res.header.kind === 'error' ? res.header.error?.message || 'cannot read' : 'not a folder'
    return false
  } catch (e) {
    node.loading = false
    node.error = (e as Error).message
    return false
  }
}

/** The working directory's listing, the tree's first rows; a connection not open yet is tried again, once a second, ten times. */
async function loadRoot() {
  if (!props.cwd) return
  // Reactive from the start: the listing mutates the root, and the retry below compares it with the ref's.
  const r = reactive(rootNode(props.cwd)) as TreeNode
  root.value = r
  treeError.value = ''
  treeLoading.value = true
  const ok = await listFolder(r)
  treeLoading.value = false
  if (!ok) {
    treeError.value = r.error || 'cannot read'
    // The connection is not open yet (the page's terminal mounts and connects after the pane), or the owner's in-flight cap on file
    // requests was full for a moment (the pane's own reads of the status, the log, the touched index and the plans, on a slow
    // relay: CI's run of the switchyard spec): try again in a moment.
    if (/not connected|not ready|in-flight|too many/i.test(treeError.value) && retries < 10) {
      retries += 1
      setTimeout(() => {
        if (root.value === r && !r.loaded) loadRoot()
      }, 1000)
    }
  } else retries = 0
}

async function toggle(node: TreeNode) {
  if (!node.dir) {
    openFile({ path: node.path })
    return
  }
  // A folder clicked while the box holds a filter: the filter goes and the whole tree shows, this folder open or closed as
  // clicked (#138); with the filter kept, the folder would read as holding only the matches.
  query.value = ''
  if (node.expanded) {
    node.expanded = false
    return
  }
  node.expanded = true
  if (!node.loaded) await listFolder(node)
}

/** Opens the tree down to `path` (a folder), fetching what is not loaded, and expands it. */
async function reveal(path: string) {
  const r = root.value
  if (!r) return
  const want = path.replace(/\/+$/, '')
  if (want === r.path) return
  if (!want.startsWith(r.path + '/')) return
  let node: TreeNode = r
  const parts = want.slice(r.path.length + 1).split('/')
  for (const part of parts) {
    if (!node.loaded && !(await listFolder(node))) return
    const next = node.children.find((c) => c.name === part && c.dir)
    if (!next) return
    next.expanded = true
    node = next
  }
  if (!node.loaded) await listFolder(node)
}

async function load(path: string, line?: number) {
  loading.value = true
  errorText.value = ''
  imageSrc.value = ''
  html.value = ''
  text.value = ''
  targetLine.value = line
  try {
    const res = await props.request(path, false)
    if (res.header.kind === 'dir') {
      // A folder: shown in the tree, opened down to it.
      header.value = null
      loading.value = false
      if (root.value && findNode(root.value, path)) {
        const node = findNode(root.value, path)!
        setChildren(node, res.header.entries ?? [])
        node.expanded = true
        await reveal(path)
      } else await reveal(path)
      target.value = null
      return
    }
    header.value = res.header
    if (res.header.kind === 'error') {
      errorText.value = res.header.error?.message || 'cannot read'
    } else if (res.header.kind === 'file') {
      if (res.header.mime?.startsWith('image/') && res.body.length) {
        let bin = ''
        for (const b of res.body) bin += String.fromCharCode(b)
        imageSrc.value = `data:${res.header.mime};base64,${btoa(bin)}`
      } else if (!res.header.binary) {
        text.value = new TextDecoder().decode(res.body)
        await highlight(text.value, res.header.path, line)
      }
    }
  } catch (e) {
    errorText.value = (e as Error).message
  } finally {
    loading.value = false
    await nextTick()
    scrollToTarget()
  }
}

function scrollToTarget() {
  const el = content.value?.querySelector('.line.target') as HTMLElement | null
  el?.scrollIntoView({ block: 'center' })
}

/** A crumb: the tree opened down to that folder, the file left. */
function openCrumb(path: string) {
  header.value = null
  errorText.value = ''
  target.value = null
  url.value = null
  reveal(path)
}

/** Back from a file or a preview to the tree. */
function backToTree() {
  header.value = null
  errorText.value = ''
  target.value = null
  url.value = null
}

/** The box's Enter: a typed path opens; a name opens its first match. */
function go() {
  const q = query.value.trim()
  if (!q) return
  if (looksLikePath(q)) {
    const loc = parseLocation(q)
    openFile({ path: resolveTyped(props.cwd || '/', loc.path), line: loc.line })
    query.value = ''
    return
  }
  const first = rows.value.find((n) => n.name.toLowerCase().includes(q.toLowerCase()))
  if (!first) return
  if (first.dir) toggle(first)
  else {
    openFile({ path: first.path })
    query.value = ''
  }
}

function copyPath() {
  return copy(header.value?.path || target.value?.path || root.value?.path || '', 'Path copied')
}

function refresh() {
  if (url.value) {
    const u = url.value
    url.value = null
    nextTick(() => (url.value = u))
  } else if (target.value) load(target.value.path, target.value.line)
  else if (section.value === 'changes') loadStatus()
  else loadRoot()
}

watch(
  () => target.value,
  (t) => {
    if (t) {
      url.value = null
      load(t.path, t.line)
    }
  },
  { deep: true },
)

watch(
  () => url.value,
  (u) => {
    if (u) {
      header.value = null
      errorText.value = ''
    }
  },
)

watch(
  () => props.cwd,
  () => {
    retries = 0
    loadRoot()
  },
  { immediate: true },
)

defineExpose({ title, mode, reload: loadRoot })

const rawHref = computed(() => (header.value?.kind === 'file' && props.rawUrl ? props.rawUrl(header.value.path) : null))
</script>

<template>
  <!--
    The pane is a column: the header rows, the content, a foot. The content
    keeps a few rows' height (min-h-24) and the column scrolls as a whole
    when the window is shorter than that (#203): squashed to nothing, the
    tree's rows were laid out under the foot, which took their clicks. On a
    very short window the advisory foot of the Explorer gives way too.
  -->
  <div class="flex h-full min-h-0 flex-col overflow-y-auto" :data-files-mode="mode">
    <!-- The view's head (design 4h): a two-line title, the bold name then the mono sub-line the view computes, the refresh button kept. -->
    <div v-if="mode === 'tree' && section === 'commits'" class="flex items-center gap-2 border-b border-default px-3 py-2 text-xs" data-files-commits-head>
      <UIcon name="i-lucide-git-commit-horizontal" class="size-3.5 flex-none text-muted" />
      <div class="flex min-w-0 flex-1 flex-col">
        <span class="text-[13px] font-semibold text-highlighted">Commits</span>
        <span class="truncate text-[11px] text-muted tabular-nums">{{ logNotRepo ? 'not a repository' : commitsHead(commits.length, log, base) }}</span>
      </div>
      <IconButton icon="i-lucide-refresh-cw" size="xs" color="neutral" variant="ghost" label="Refresh" :loading="logLoading" @click="loadLog" />
    </div>
    <div v-if="mode === 'tree' && section === 'explorer'" class="flex items-center gap-2 border-b border-default px-3 py-2 text-xs" data-files-explorer-head>
      <UIcon name="i-lucide-folder" class="size-3.5 flex-none text-muted" />
      <div class="flex min-w-0 flex-1 flex-col">
        <span class="text-[13px] font-semibold text-highlighted">Explorer</span>
        <span class="truncate font-mono text-[11px] text-muted" data-mono="path">{{ cwd }}</span>
      </div>
    </div>
    <div v-if="mode === 'tree' && section === 'explorer'" class="border-b border-default px-2 py-1.5">
      <UInput v-model="query" placeholder="Filter, or go to path:line" size="xs" class="w-full font-mono" icon="i-lucide-search" data-files-box @keydown.enter.prevent="go" @keydown.escape="query = ''" />
    </div>
    <div v-if="mode === 'tree' && section === 'changes'" class="flex items-center gap-2 border-b border-default px-3 py-2 text-xs" data-files-changes-head>
      <UIcon name="i-lucide-git-branch" class="size-3.5 flex-none text-muted" />
      <div class="flex min-w-0 flex-1 flex-col">
        <span class="text-[13px] font-semibold text-highlighted">Changes</span>
        <span class="truncate text-[11px] text-muted tabular-nums"><template v-if="notRepo">not a repository</template><template v-else><span class="font-mono" data-mono="commit">{{ status?.branch || '…' }}</span> · {{ base && base !== 'HEAD' ? `vs ${base}` : 'working directory' }} · {{ changesTitle(changes.length) }}</template></span>
      </div>
      <IconButton icon="i-lucide-refresh-cw" size="xs" color="neutral" variant="ghost" label="Refresh" :loading="statusLoading" @click="loadStatus" />
    </div>
    <div v-if="mode !== 'tree' || section === 'explorer'" class="flex items-center gap-2 border-b border-default px-3 py-2 text-xs">
      <template v-if="mode === 'url'">
        <UIcon name="i-lucide-globe" class="size-4 text-muted" />
        <span class="truncate flex-1 font-mono">{{ url }}</span>
        <UButton label="New tab" icon="i-lucide-external-link" size="xs" color="neutral" variant="soft" :to="url!" target="_blank" rel="noopener noreferrer" />
      </template>
      <template v-else>
        <IconButton v-if="mode !== 'tree'" icon="i-lucide-arrow-left" size="xs" color="neutral" variant="ghost" label="Back to the files" data-files-back @click="backToTree" />
        <!-- The end of the path reads whole (#169): the folders above fold into "…", which lists them; nothing is cut to a letter. -->
        <nav ref="crumbsNav" class="flex items-center gap-1 flex-1 min-w-0 overflow-x-auto whitespace-nowrap [scrollbar-width:none]" aria-label="path" :title="fullPath" data-files-crumbs @vue:mounted="scrollCrumbsToEnd">
          <template v-if="folded.hidden.length">
            <UDropdownMenu :items="hiddenItems" :content="{ align: 'start' }">
              <button type="button" class="flex-none font-mono text-muted hover:underline" :aria-label="`${folded.hidden.length} folders above: ${folded.hidden.map((c) => c.label).join('/')}`" data-files-crumbs-more>…</button>
            </UDropdownMenu>
            <UIcon name="i-lucide-chevron-right" class="size-3 text-muted flex-none" />
          </template>
          <template v-for="(c, i) in folded.shown" :key="c.path">
            <UIcon v-if="i > 0" name="i-lucide-chevron-right" class="size-3 text-muted flex-none" />
            <button type="button" class="flex-none font-mono" :class="[i === folded.shown.length - 1 ? 'text-highlighted' : 'text-muted', c.inside || c.path === crumbsOf(cwd || '/').at(-1)?.path ? 'hover:underline' : 'cursor-default']" :data-files-crumb="c.path" @click="(c.inside || c.path === crumbsOf(cwd || '/').at(-1)?.path) && openCrumb(c.path)">
              {{ c.label }}
            </button>
          </template>
        </nav>
        <UBadge v-if="header?.kind === 'file'" :label="fmtSize(header.size)" color="neutral" variant="subtle" size="sm" />
        <UBadge v-if="header?.truncated" label="truncated" color="warning" variant="subtle" size="sm" />
        <UBadge v-if="header?.binary" label="binary" color="neutral" variant="subtle" size="sm" />
        <IconButton v-if="mode === 'file'" icon="i-lucide-copy" size="xs" color="neutral" variant="ghost" label="Copy path" @click="copyPath" />
        <IconButton v-if="rawHref" icon="i-lucide-file-output" size="xs" color="neutral" variant="ghost" label="Open raw" :to="rawHref" target="_blank" rel="noopener noreferrer" />
      </template>
      <IconButton icon="i-lucide-refresh-cw" size="xs" color="neutral" variant="ghost" label="Refresh" :loading="loading || treeLoading" @click="refresh" />
    </div>

    <div ref="content" class="file-view flex-1 min-h-24 overflow-auto">
      <div v-if="loading" class="p-6 text-sm text-muted flex items-center gap-2">
        <UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Loading…
      </div>

      <iframe
        v-else-if="mode === 'url'"
        :src="url!"
        class="w-full h-full min-h-[70vh] bg-white"
        sandbox="allow-scripts allow-same-origin allow-forms allow-popups"
        referrerpolicy="no-referrer"
        title="URL preview"
      />

      <UAlert v-else-if="mode === 'error'" color="error" variant="subtle" icon="i-lucide-triangle-alert" class="m-3" :title="errorText || header?.error?.message || 'Cannot read this path'" />

      <template v-else-if="mode === 'file'">
        <img v-if="imageSrc" :src="imageSrc" alt="" class="max-w-full p-3" />
        <p v-else-if="header?.binary" class="p-6 text-sm text-muted">Binary file; nothing to show.</p>
        <div v-else-if="html" class="p-3" v-html="html" />
        <pre v-else class="p-3 font-mono"><div v-for="(l, i) in lines" :key="i" class="line" :class="{ target: targetLine === i + 1 }"><span class="line-no">{{ i + 1 }}</span><span class="line-code" v-html="escapeHtml(l) || ' '" /></div></pre>
      </template>

      <template v-else-if="section === 'commits'">
        <div v-if="logNotRepo" class="flex flex-col items-center gap-2 p-6 text-center text-sm" data-files-not-repo>
          <UIcon name="i-lucide-git-branch" class="size-6 text-muted" />
          <span class="font-medium text-highlighted">Not a git repository</span>
          <span class="text-muted">{{ cwd }} has no .git. Touched still lists what the agent did.</span>
        </div>
        <div v-else-if="logError" class="flex items-center gap-2 p-4 text-sm text-muted">
          <UIcon name="i-lucide-triangle-alert" class="size-4 flex-none text-warning" /><span class="min-w-0 flex-1">{{ logError }}</span>
          <UButton label="Retry" size="xs" color="neutral" variant="soft" @click="loadLog" />
        </div>
        <div v-else-if="!log" class="flex items-center gap-2 p-6 text-sm text-muted"><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Reading the commits…</div>
        <p v-else-if="!commits.length" class="p-6 text-sm text-muted">{{ base && base !== 'HEAD' ? `No commits since ${base}.` : 'No commits since the session started.' }}</p>
        <ul v-else class="py-1" data-files-commits>
          <li v-for="c in commits" :key="c.sha">
            <button type="button" class="flex w-full items-start gap-2 px-3 py-1.5 text-left text-sm hover:bg-elevated" :data-commit="c.sha" :aria-expanded="opened.has(c.sha)" @click="toggleCommit(c)">
              <UIcon :name="opened.has(c.sha) ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'" class="mt-0.5 size-3.5 flex-none text-muted" />
              <span class="min-w-0 flex-1">
                <span class="block truncate text-highlighted" data-commit-subject>{{ c.subject }}</span>
                <span class="block truncate text-[11px] text-muted"><span class="font-mono">{{ c.short }}</span>{{ c.author ? ` · ${c.author}` : '' }} · {{ commitAgo(c.atMs, now) }}</span>
              </span>
            </button>
            <div v-if="opened.has(c.sha)" class="pb-1 pl-6" :data-commit-files="c.sha">
              <div v-if="!opened.get(c.sha)!.header && !opened.get(c.sha)!.error" class="flex items-center gap-2 px-3 py-1 text-xs text-muted"><UIcon name="i-lucide-loader-circle" class="size-3.5 animate-spin" /> Reading…</div>
              <p v-else-if="opened.get(c.sha)!.error" class="px-3 py-1 text-xs text-warning">{{ opened.get(c.sha)!.error }}</p>
              <template v-else>
                <p v-if="opened.get(c.sha)!.header?.commit?.body" class="whitespace-pre-wrap px-3 py-1 text-xs text-muted" data-commit-body>{{ opened.get(c.sha)!.header?.commit?.body }}</p>
                <button v-for="ch in commitChangeRows(opened.get(c.sha)!.header)" :key="ch.abs" type="button" class="flex w-full items-center gap-2 px-3 py-0.5 text-left text-sm hover:bg-elevated" :data-commit-change="ch.abs" :data-change-status="ch.status" @click="openCommitChange(c, ch)">
                  <UBadge :label="statusLetter(ch.status)" :color="statusTone(ch.status)" variant="subtle" size="xs" class="w-5 flex-none justify-center font-mono" />
                  <span class="min-w-0 flex-1 truncate font-mono"><span class="text-muted">{{ ch.dir }}</span>{{ ch.name }}</span>
                  <span v-if="ch.binary" class="flex-none text-[11px] text-muted">binary</span>
                  <template v-else>
                    <span class="flex-none font-mono text-[11px] text-success">+{{ ch.added }}</span>
                    <span class="flex-none font-mono text-[11px] text-error">−{{ ch.removed }}</span>
                  </template>
                </button>
                <p v-if="opened.get(c.sha)!.header?.truncated" class="px-3 py-0.5 text-xs text-muted">The list stops here.</p>
              </template>
            </div>
          </li>
          <li v-if="log?.truncated" class="px-3 py-1 text-xs text-muted">The list stops at {{ commits.length }} commits.</li>
        </ul>
      </template>
      <template v-else-if="section === 'changes'">
        <div v-if="notRepo" class="flex flex-col items-center gap-2 p-6 text-center text-sm" data-files-not-repo>
          <UIcon name="i-lucide-git-branch" class="size-6 text-muted" />
          <span class="font-medium text-highlighted">Not a git repository</span>
          <span class="text-muted">{{ cwd }} has no .git. Touched still lists what the agent did.</span>
        </div>
        <div v-else-if="statusError" class="flex items-center gap-2 p-4 text-sm text-muted" data-files-changes-error>
          <UIcon name="i-lucide-triangle-alert" class="size-4 flex-none text-warning" /><span class="min-w-0 flex-1">{{ statusError }}</span>
          <UButton label="Retry" size="xs" color="neutral" variant="soft" @click="loadStatus" />
        </div>
        <p v-else-if="status && !changes.length" class="p-6 text-sm text-muted">Nothing changed since the last commit.</p>
        <ul v-else class="py-1" data-files-changes>
          <li v-for="c in changes" :key="c.abs">
            <button type="button" class="flex w-full items-center gap-2 px-3 py-1 text-left text-sm hover:bg-elevated" :data-change="c.abs" :data-change-status="c.status" @click="openChange(c)">
              <UBadge :label="statusLetter(c.status)" :color="statusTone(c.status)" variant="subtle" size="xs" class="w-5 flex-none justify-center font-mono" />
              <span class="min-w-0 flex-1 truncate font-mono"><span class="text-muted">{{ c.dir }}</span>{{ c.name }}</span>
              <span v-if="c.binary" class="flex-none text-[11px] text-muted">binary</span>
              <template v-else>
                <span class="flex-none font-mono text-[11px] text-success">+{{ c.added }}</span>
                <span class="flex-none font-mono text-[11px] text-error">−{{ c.removed }}</span>
              </template>
            </button>
          </li>
          <li v-if="status?.truncated" class="px-3 py-1 text-xs text-muted">The list stops at {{ changes.length }} files.</li>
        </ul>
      </template>
      <template v-else>
        <p v-if="!cwd" class="p-6 text-sm text-muted">Click a file path or URL in the terminal to open it here.</p>
        <div v-else-if="treeLoading && !root?.loaded" class="p-6 text-sm text-muted flex items-center gap-2"><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Listing {{ root?.name }}…</div>
        <div v-else-if="treeError" class="flex items-center gap-2 p-4 text-sm text-muted" data-files-error>
          <UIcon name="i-lucide-triangle-alert" class="size-4 flex-none text-warning" />
          <span class="min-w-0 flex-1">{{ treeError }}</span>
          <UButton label="Retry" size="xs" color="neutral" variant="soft" @click="loadRoot" />
        </div>
        <ul v-else class="py-1" data-files-tree>
          <!-- The agent's plans outside the working directory (#289), found here without the terminal; each opens as a file. -->
          <template v-if="plans.length && !query">
            <li class="px-3 pb-0.5 pt-1 text-[11px] font-medium uppercase tracking-wide text-muted" data-files-plans-head>Plans</li>
            <li v-for="p in plans" :key="p.abs" data-files-plans>
              <button type="button" class="flex w-full items-center gap-1.5 px-3 py-1 text-left text-sm hover:bg-elevated" :data-plan="p.abs" :title="p.abs" @click="openFile({ path: p.abs })">
                <UIcon name="i-lucide-file-text" class="size-4 flex-none text-primary" />
                <span class="min-w-0 flex-1 truncate font-mono">{{ p.name }}</span>
                <span class="text-[11px] text-muted">{{ fmtSize(p.size) }}</span>
              </button>
            </li>
            <li class="mx-3 mb-1 mt-1 border-t border-default" aria-hidden="true" />
          </template>
          <li v-if="root && !rows.length && !foundBelow.length" class="px-4 py-3 text-sm text-muted">{{ query ? 'Nothing matches' : 'Empty directory' }}</li>
          <template v-for="n in rows" :key="n.path">
            <li>
              <button
                type="button"
                class="flex w-full items-center gap-1.5 py-1 pr-3 text-left text-sm hover:bg-elevated"
                :style="{ paddingLeft: `${12 + n.depth * 14}px` }"
                :data-file-node="n.path"
                :data-file-dir="n.dir ? '' : undefined"
                :data-file-expanded="n.dir && n.expanded ? '' : undefined"
                @click="toggle(n)"
              >
                <UIcon v-if="n.dir" :name="n.expanded ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'" class="size-3.5 flex-none text-muted" />
                <span v-else class="size-3.5 flex-none" />
                <UIcon :name="n.dir ? (n.expanded ? 'i-lucide-folder-open' : 'i-lucide-folder') : 'i-lucide-file'" class="size-4 flex-none" :class="n.dir ? 'text-primary' : 'text-muted'" />
                <span class="font-mono flex-1 truncate">{{ n.name }}<span v-if="!n.dir && dots.has(n.path)" class="ml-1.5 inline-block size-1.5 rounded-full bg-muted align-middle" :data-file-touched="n.path" /></span>
                <span v-if="!n.dir && marks.get(n.path)" class="flex-none font-mono text-[10px] font-semibold" :class="statusTone(marks.get(n.path)!) === 'success' ? 'text-success' : statusTone(marks.get(n.path)!) === 'error' ? 'text-error' : 'text-warning'" :data-file-mark="statusLetter(marks.get(n.path)!)">{{ statusLetter(marks.get(n.path)!) }}</span>
                <UIcon v-if="n.loading" name="i-lucide-loader-circle" class="size-3.5 animate-spin text-muted" />
                <span v-else-if="!n.dir" class="text-[11px] text-muted">{{ fmtSize(n.size) }}</span>
              </button>
            </li>
            <li v-if="n.dir && n.expanded && n.loaded && !n.children.length && !query" class="py-1 text-xs text-muted" :style="{ paddingLeft: `${12 + (n.depth + 1) * 14 + 20}px` }" :data-file-empty="n.path">Empty directory</li>
            <li v-if="n.dir && n.expanded && n.error" class="py-1 text-xs text-warning" :style="{ paddingLeft: `${12 + (n.depth + 1) * 14 + 20}px` }">{{ n.error }}</li>
          </template>
          <template v-if="foundBelow.length">
            <li class="mt-1 border-t border-default px-3 pb-0.5 pt-2 text-[11px] font-medium uppercase tracking-wide text-muted" data-files-found-head>In folders not opened yet</li>
            <li v-for="f in foundBelow" :key="f.abs">
              <button type="button" class="flex w-full items-center gap-1.5 px-3 py-1 text-left text-sm hover:bg-elevated" :data-file-found="f.abs" @click="openFile({ path: f.abs })">
                <UIcon name="i-lucide-file" class="size-4 flex-none text-muted" />
                <span class="min-w-0 flex-1 truncate font-mono"><span class="text-muted">{{ f.dir }}</span>{{ f.name }}</span>
              </button>
            </li>
            <li v-if="found?.truncated" class="px-3 py-1 text-[11px] text-muted">The search stopped early; type more to narrow it.</li>
            <li class="px-3 py-1 text-[11px] text-muted">.git and node_modules are not searched.</li>
          </template>
        </ul>
      </template>
    </div>
    <p v-if="mode === 'tree' && cwd && section === 'explorer'" class="flex items-center gap-1.5 border-t border-default px-3 py-1.5 text-[11px] text-muted [@media(max-height:360px)]:hidden" data-files-hint>
      <UIcon name="i-lucide-mouse-pointer-click" class="size-3.5 flex-none" /> Paths the agent prints in the terminal are clickable.
    </p>
    <p v-if="mode === 'tree' && cwd && section === 'commits' && log?.kind === 'log'" class="flex items-center gap-1.5 border-t border-default px-3 py-1.5 text-[11px] text-muted" data-files-commits-foot><UIcon name="i-lucide-info" class="size-3 flex-none" />Commits made here. Nothing is pushed from Conductor.</p>
    <p v-if="mode === 'tree' && cwd && section === 'changes' && status?.kind === 'status'" class="flex items-center gap-1.5 border-t border-default px-3 py-1.5 text-[11px] text-muted" data-files-changes-foot>
      <UIcon name="i-lucide-refresh-cw" class="size-3.5 flex-none" /> Refreshed as the agent works · {{ agoWords(now - statusAt) }}
      <span class="ml-auto font-mono"><span class="text-success">+{{ status.added ?? 0 }}</span> <span class="text-error">−{{ status.removed ?? 0 }}</span></span>
    </p>
  </div>
</template>

<style scoped>
.file-view :deep(.shiki) {
  background: transparent !important;
  padding: 0;
  font-size: 0.8125rem;
  line-height: 1.45;
}
.file-view :deep(.shiki code) {
  display: block;
}
.file-view :deep(.shiki .line) {
  display: flex;
  white-space: pre;
}
.file-view :deep(.shiki .line.target) {
  background: color-mix(in oklab, var(--ui-primary) 18%, transparent);
}
.file-view :deep(.shiki .line-no) {
  user-select: none;
  width: 3.5rem;
  flex: none;
  text-align: right;
  padding-right: 0.75rem;
  opacity: 0.45;
}
</style>

