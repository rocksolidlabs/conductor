<script setup lang="ts">
import type { LinkRequest, ReachInfo, ShareLink } from '~/composables/useSessions'
import { ApiError } from '~/composables/useApi'
import type { Role } from '~/utils/protocol'
import { qrSvg } from '~/utils/qr'
import { reuseHeld, shareReach, type ShareReach } from '~/utils/share'

/**
 * Share in one click: opening the dialog makes a link for two hours, with the
 * role last chosen here (view the first time; View / Control on the link
 * switches it),
 * copies it and says where it reaches (from anywhere, through the
 * switchyard, when the session is published there). Opening it again on the
 * same target shows the link already made rather than minting another; the
 * role, label and expiry are below, for another link.
 *
 * A session's links, or a crew run's: those open every member of the run,
 * one added later included. Exactly one of the two is given.
 *
 * Another link may answer decisions (docs/plans/decision-inbox.md, PR-7):
 * a control link with Answer decisions from this link on opens on the join
 * page's Decide tab, where its holder answers what the link covers. At a
 * switchyard that carries no decisions the server refuses it
 * (`switchyard_too_old`), and the switch then stays off with the words why.
 */
type Target = { sessionId: string; runId?: undefined } | { runId: string; sessionId?: undefined }
/** `role`: the role asked for by whoever opened the dialog (the palette's `share <name> control`), used for this opening's link. */
const props = defineProps<Target & { sessionName?: string; role?: Role }>()
const open = defineModel<boolean>('open', { default: false })

const api = useSessions()

/** The link routes of whichever the modal shares. */
function routes() {
  if (props.runId !== undefined) {
    const run = props.runId
    return {
      list: () => api.runLinkList(run),
      create: (body: LinkRequest) => api.createRunLink(run, body),
      revoke: (linkId: string) => api.revokeRunLink(run, linkId),
    }
  }
  const id = props.sessionId
  return {
    list: () => api.linkList(id),
    create: (body: LinkRequest) => api.createLink(id, body),
    revoke: (linkId: string) => api.revokeLink(id, linkId),
  }
}
const isRun = computed(() => props.runId !== undefined)
const targetKey = computed(() => (props.runId !== undefined ? `run:${props.runId}` : `session:${props.sessionId}`))
const title = computed(() => (props.sessionName ? `Share ${props.sessionName}` : props.runId ? 'Share this crew' : 'Share this session'))
const description = computed(() =>
  props.runId
    ? 'The link is copied: it opens every agent of the crew, one added later too. Revoking disconnects whoever used it.'
    : 'The link is copied. Revoking disconnects whoever used it.',
)
const toast = useToast()
const copyText = useCopy()
const links = ref<ShareLink[]>([])
const loading = ref(false)
const creating = ref(false)
const error = ref('')

type Created = { linkId: string; url: string; role: Role; label?: string; invite?: string; remote?: boolean; rendezvous?: { server: string; error: string }; decide?: boolean }
/** The link made for the current target, kept while the dialog is closed so the next open shows it again. */
const created = ref<Created | null>(null)
const createdFor = ref('')
/** The server's reach report, read when the modal opens; null when it could not be read. */
const reach = ref<ReachInfo | null>(null)
const createdReach = computed<ShareReach | null>(() => (created.value ? shareReach(created.value, reach.value, isRun.value) : null))
/** The link as a QR code for a phone in the room to scan (#144): the link alone is in it, and nothing else ever will be. */
const qr = computed(() => (created.value ? qrSvg(created.value.url) : null))
const reachColor: Record<string, 'success' | 'warning' | 'neutral' | 'info'> = { remote: 'success', public: 'success', pending: 'warning', local: 'neutral', unknown: 'info' }
const reachIcon = computed(() => {
  const r = createdReach.value
  if (!r) return ''
  if (r.kind === 'remote') return 'i-lucide-globe'
  if (r.kind === 'unpublished') return 'i-lucide-unplug'
  return r.level === 'public' ? 'i-lucide-globe' : r.level === 'pending' ? 'i-lucide-hourglass' : 'i-lucide-house'
})

const form = reactive<{ role: Role; label: string; ttl: string; decide: boolean }>({ role: 'view', label: '', ttl: '7200', decide: false })
/** The words of the server's refusal of a decision link here (`switchyard_too_old`): the switch stays off with them. */
const decideRefused = ref('')
watch(
  () => form.role,
  (r) => {
    if (r !== 'control') form.decide = false
  },
)

// The role of the one-click link: view until the person picks control on a
// link, then that, remembered in this browser.
const ROLE_KEY = 'conductor.share.role'
function readRole(): Role {
  try {
    return localStorage.getItem(ROLE_KEY) === 'control' ? 'control' : 'view'
  } catch {
    return 'view'
  }
}
const quickRole = ref<Role>(readRole())

/**
 * Switches the link just made to another role: a link's role is fixed, so a
 * new one is made and copied, and the one it replaces is revoked when nobody
 * joined through it yet.
 */
const switching = ref(false)
async function setRole(role: Role) {
  const old = created.value
  quickRole.value = role
  try {
    localStorage.setItem(ROLE_KEY, role)
  } catch {
    /* storage refused: the choice lasts the page */
  }
  if (!old || old.role === role) return
  switching.value = true
  try {
    await create({ role, ttlSeconds: 7200 })
    if (created.value && created.value.linkId !== old.linkId) {
      const used = links.value.find((l) => l.id === old.linkId)?.active ?? 0
      if (!used) {
        await routes()
          .revoke(old.linkId)
          .catch(() => {})
        await refresh()
      }
    }
  } finally {
    switching.value = false
  }
}

// A paste invite: the viewer's blob in, this session's answer out; no
// server between the two once connected. Sessions alone, not runs.
const paste = reactive<{ offer: string; role: Role; answer: string; busy: boolean; error: string }>({ offer: '', role: 'view', answer: '', busy: false, error: '' })
async function answerPaste() {
  if (!('sessionId' in props) || !props.sessionId) return
  paste.busy = true
  paste.error = ''
  paste.answer = ''
  try {
    const res = await api.paste(props.sessionId, { offer: paste.offer, role: paste.role })
    paste.answer = res.answer
    await copyText(res.answer, 'Answer copied', 'Send it back to the viewer; the terminal connects when they paste it.')
  } catch (e) {
    paste.error = (e as Error).message
  } finally {
    paste.busy = false
  }
}
const roles: Array<{ value: Role; label: string; description: string }> = [
  { value: 'view', label: 'View', description: "Watch output, open files. Can't type." },
  { value: 'control', label: 'Control', description: 'Types into the agent, answers prompts.' },
]
const ttlItems = [
  { label: 'Never', value: '0' },
  { label: 'In 1 hour', value: '3600' },
  { label: 'In 2 hours', value: '7200' },
  { label: 'In 8 hours', value: '28800' },
  { label: 'In 24 hours', value: '86400' },
  { label: 'In 7 days', value: '604800' },
]

/** What the links list says of the publication: published to the switchyard now; ended there, a link made now making it again. */
const publication = ref({ republish: false, published: false })

async function refresh() {
  loading.value = true
  error.value = ''
  try {
    const list = await routes().list()
    links.value = list.links
    publication.value = { republish: list.republish, published: list.published }
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

// Opening the dialog is the one click: the link already made for this
// target is shown and copied again; otherwise a view link for two hours is
// made, copied and shown. One mint per target, not per open: a switchyard
// allows a host five link requests a minute. When the switchyard ended the
// publication since, or the held link was made here while the session was
// not published and it is now, a link is made anew (reuseHeld).
watch(open, async (v) => {
  if (!v) return
  error.value = ''
  api
    .reach()
    .then((r) => (reach.value = r))
    .catch(() => (reach.value = null))
  await refresh()
  const held = reuseHeld(created.value, createdFor.value === targetKey.value && (!props.role || created.value?.role === props.role), links.value, publication.value)
  if (held) {
    await copy(created.value!.url)
    return
  }
  created.value = null
  await create({ role: props.role ?? quickRole.value, ttlSeconds: 7200 })
})

async function create(body: LinkRequest) {
  creating.value = true
  error.value = ''
  try {
    const res = await routes().create(body)
    created.value = {
      linkId: res.link.id,
      url: res.url,
      role: res.link.role,
      label: res.link.label,
      invite: res.invite,
      remote: 'remote' in res && res.remote === true,
      rendezvous: 'rendezvous' in res ? res.rendezvous : undefined,
      decide: res.link.decide === true,
    }
    createdFor.value = targetKey.value
    await refresh()
    await copy(res.url)
  } catch (e) {
    if (body.decide && e instanceof ApiError && e.code === 'switchyard_too_old') {
      decideRefused.value = e.message
      form.decide = false
    } else error.value = (e as Error).message
  } finally {
    creating.value = false
  }
}

async function createAnother() {
  const decide = form.role === 'control' && form.decide && !decideRefused.value
  await create({ role: form.role, label: form.label || undefined, ttlSeconds: Number(form.ttl) || undefined, ...(decide ? { decide: true } : {}) })
  form.label = ''
}

function copy(url: string) {
  return copyText(url, 'Link copied', 'Paste it to whoever should join.')
}

async function revoke(link: ShareLink) {
  try {
    await routes().revoke(link.id)
    toast.add({ title: 'Link revoked', description: 'Viewers using it were disconnected.', icon: 'i-lucide-ban', color: 'neutral' })
    if (created.value && created.value.linkId === link.id) created.value = null
    await refresh()
  } catch (e) {
    error.value = (e as Error).message
  }
}

function fmt(ts?: string) {
  return ts ? new Date(ts).toLocaleString() : ''
}

/** Shortens a long join URL for display: host/join/k7Qx…9fRm */
function shortUrl(url: string) {
  const m = /^(https?:\/\/)?([^/]+)\/join\/(.+)$/.exec(url)
  if (!m) return url
  const tok = m[3]!
  return `${m[2]}/join/${tok.length > 12 ? `${tok.slice(0, 4)}…${tok.slice(-4)}` : tok}`
}

const live = computed(() => links.value.filter((l) => !l.revoked))
</script>

<template>
  <UModal v-model:open="open" :title="title" :description="description" :ui="{ content: 'max-w-lg' }">
    <template #body>
      <div class="flex flex-col gap-4">
        <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />

        <div v-if="creating && !created" class="flex items-center gap-2 text-sm text-muted" data-share-making>
          <UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" />
          Making the link…
        </div>

        <div v-if="created" class="flex flex-col gap-2 rounded-md bg-elevated px-3.5 py-3" data-share-created>
          <div class="flex items-center gap-2.5">
            <span class="flex-1 truncate font-mono text-xs" :title="created.url" data-created-url>{{ shortUrl(created.url) }}</span>
            <UButton label="Copy link" size="sm" @click="copy(created!.url)" />
          </div>
          <div v-if="qr" class="flex items-center gap-3" data-share-qr-row>
            <svg :viewBox="qr.viewBox" class="size-28 flex-none rounded bg-white text-black" shape-rendering="crispEdges" role="img" :aria-label="created.url" data-share-qr>
              <path :d="qr.path" fill="currentColor" />
            </svg>
            <span class="text-xs text-muted">Scan it with a phone to open the link there. The code holds the link alone.</span>
          </div>
          <div class="flex flex-wrap items-center gap-2">
            <div class="flex gap-0.5 rounded-md bg-default p-0.5 ring ring-default" role="radiogroup" aria-label="What the link allows" data-share-role>
              <button
                v-for="r in roles"
                :key="r.value"
                type="button"
                role="radio"
                :aria-checked="created.role === r.value"
                :disabled="switching || creating"
                class="flex items-center gap-1.5 rounded px-2.5 py-1 text-xs font-medium transition-colors disabled:opacity-60"
                :class="created.role === r.value ? 'bg-inverted text-inverted' : 'text-muted hover:text-default'"
                :data-share-role-option="r.value"
                @click="setRole(r.value)"
              >
                <UIcon :name="r.value === 'control' ? 'i-lucide-keyboard' : 'i-lucide-eye'" class="size-3.5" />{{ r.label }}
              </button>
            </div>
            <span class="text-xs text-muted">{{ roles.find((r) => r.value === created!.role)?.description }}</span>
            <UIcon v-if="switching" name="i-lucide-loader-circle" class="size-3.5 animate-spin text-muted" />
          </div>
          <p v-if="created.decide" class="flex items-center gap-1.5 text-xs text-muted" data-created-decide>
            <UIcon name="i-lucide-list-checks" class="size-3.5" /> Answers decisions: the link opens on its Decide tab.
          </p>
          <div v-if="created.invite" class="flex items-center gap-2.5" data-created-invite>
            <span class="flex-1 truncate font-mono text-xs text-muted" :title="created.invite">{{ created.invite }}</span>
            <UButton label="Copy invite" size="sm" color="neutral" variant="outline" @click="copyText(created!.invite!, 'Invite copied', 'It opens in the Conductor app; the link opens in a browser.')" />
          </div>
          <UAlert
            v-if="createdReach"
            :color="reachColor[createdReach.kind === 'unpublished' ? 'pending' : createdReach.level]"
            variant="soft"
            :icon="reachIcon"
            :title="createdReach.title"
            :description="createdReach.text"
            :data-link-reach="createdReach.level"
            :data-share-reach="createdReach.kind"
            class="mt-1"
          />
        </div>

        <details class="rounded-md border border-default px-3.5 py-2" data-another-link>
          <summary class="cursor-pointer text-sm font-semibold">Another link</summary>
          <form class="mt-3 flex flex-col gap-4" @submit.prevent="createAnother">
            <div class="grid grid-cols-2 gap-2" role="radiogroup" aria-label="Role">
              <button
                v-for="r in roles"
                :key="r.value"
                type="button"
                role="radio"
                :aria-checked="form.role === r.value"
                class="flex flex-col gap-1 rounded-md border p-3 text-left transition-colors"
                :class="form.role === r.value ? 'border-primary bg-primary/5 ring-1 ring-primary' : 'border-default hover:border-accented'"
                @click="form.role = r.value"
              >
                <span class="text-sm font-semibold">{{ r.label }}</span>
                <span class="text-xs leading-snug text-muted">{{ r.description }}</span>
              </button>
            </div>
            <div v-if="form.role === 'control'" class="flex flex-col gap-1" data-share-decide>
              <USwitch v-model="form.decide" :disabled="!!decideRefused" label="Answer decisions from this link" description="It opens on a Decide tab: whoever holds it answers the questions and the tasks' choices it covers, each recorded as the link's." data-share-decide-switch />
              <p v-if="decideRefused" class="text-xs text-warning" data-share-decide-refused>{{ decideRefused }}</p>
            </div>
            <div class="grid grid-cols-2 gap-3">
              <UFormField label="Label" name="label">
                <UInput v-model="form.label" placeholder="what the link is for (optional)" class="w-full" />
              </UFormField>
              <UFormField label="Expires" name="ttl">
                <USelect v-model="form.ttl" :items="ttlItems" class="w-full" />
              </UFormField>
            </div>
            <UButton label="Create link" type="submit" icon="i-lucide-link" :loading="creating" class="self-end" />
          </form>
        </details>

        <details v-if="'sessionId' in props && props.sessionId" class="rounded-md border border-default px-3.5 py-2" data-paste-invite>
          <summary class="cursor-pointer text-sm font-semibold">Answer a paste invite</summary>
          <div class="mt-2 flex flex-col gap-2">
            <p class="text-xs text-muted">For a viewer no server can reach from here: they open <code>/paste</code> on any Conductor, send you their invite, you answer it here, and the terminal runs between the two machines alone.</p>
            <UTextarea v-model="paste.offer" :rows="3" placeholder="cpi1.…" class="w-full font-mono text-[11px]" data-paste-offer-in />
            <div class="flex items-center gap-2">
              <USelect v-model="paste.role" :items="roles.map((r) => ({ label: r.label, value: r.value }))" class="w-36" />
              <UButton label="Answer" icon="i-lucide-reply" size="sm" :loading="paste.busy" :disabled="!paste.offer.trim()" data-paste-answer-make @click="answerPaste" />
            </div>
            <UAlert v-if="paste.error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="paste.error" />
            <template v-if="paste.answer">
              <UTextarea :model-value="paste.answer" :rows="3" readonly class="w-full font-mono text-[11px]" data-paste-answer-out />
              <UButton label="Copy answer" icon="i-lucide-clipboard" size="sm" class="self-start" @click="copyText(paste.answer, 'Answer copied')" />
            </template>
          </div>
        </details>

        <div>
          <div class="flex items-center justify-between mb-2">
            <h3 class="text-[11px] font-semibold uppercase tracking-wider text-muted">Links</h3>
            <UButton icon="i-lucide-refresh-cw" size="xs" color="neutral" variant="ghost" :loading="loading" aria-label="Refresh" @click="refresh" />
          </div>
          <p v-if="!live.length && !loading" class="text-sm text-muted">No links yet.</p>
          <ul v-else class="flex flex-col gap-2">
            <li v-for="link in live" :key="link.id" class="flex flex-col gap-1 rounded-md border border-default px-3 py-2.5" data-share-link>
              <div class="flex items-center gap-2">
                <span class="text-sm font-medium truncate">{{ link.label || 'unlabelled' }}</span>
                <UBadge :label="link.role === 'control' ? 'Control' : 'View'" :color="link.role === 'control' ? 'primary' : 'neutral'" :variant="link.role === 'control' ? 'subtle' : 'outline'" size="sm" />
                <UBadge v-if="link.remote" label="switchyard" color="info" variant="subtle" size="sm" icon="i-lucide-globe" />
                <UBadge v-if="link.decide" label="answers decisions" color="primary" variant="outline" size="sm" icon="i-lucide-list-checks" data-share-link-decide />
                <UBadge v-if="link.revokePending" label="revoke pending" color="warning" variant="subtle" size="sm" />
                <UButton label="Revoke" size="xs" variant="link" color="secondary" class="ml-auto" @click="revoke(link)" />
              </div>
              <span class="font-mono text-[11px] text-muted">
                created {{ fmt(link.createdAt) }}<template v-if="link.expiresAt"> · expires {{ fmt(link.expiresAt) }}</template><template v-if="link.active !== undefined && !link.remote"> · {{ link.active }} using</template>
              </span>
            </li>
          </ul>
        </div>
      </div>
    </template>
  </UModal>
</template>
