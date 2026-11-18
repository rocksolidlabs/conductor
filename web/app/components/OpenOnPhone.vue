<script setup lang="ts">
import { ApiError } from '~/composables/useApi'
import { qrSvg } from '~/utils/qr'
import { shareRefusal } from '~/utils/share'
import { decisionLinkTargets, phoneOffer, PHONE_NOT_OFFERED } from '~/utils/inbox'

/**
 * Open on your phone: the decision page's address as a QR code, whatever page it is opened from (its origin and `/decisions` alone);
 * on a loopback origin, the words why a phone cannot reach it and a decision link in its place. Mounted once by the default layout;
 * the decision page's button and your menu open it (useOpenOnPhone).
 */
const { open } = useOpenOnPhone()
const live = useAttention()
const api = useSessions()

const offer = computed(() => (import.meta.client ? phoneOffer(location.origin) : null))
const qr = computed(() => (offer.value?.kind === 'qr' ? qrSvg(offer.value.url) : null))
/** The dialog's line: the code's, only where there is a code. */
const phoneWords = computed(() =>
  offer.value?.kind === 'qr'
    ? "Scan the code with a phone on the same network. It holds the page's address alone: the phone asks for the workbench token once, as any browser does."
    : "This page's address is this computer's own.",
)

/**
 * On a loopback origin, a decision link in place of the page's address (MB0's PR-7): for a run or a session the person picks, a
 * control link that answers decisions, made for two hours and shown as a QR code. Its address is the one the server gives its links
 * (the switchyard it publishes to, or its public address); one that is this computer's own too is said to be so.
 */
const targets = computed(() => decisionLinkTargets(live.sessions.value, live.runs.value))
const target = ref('')
watch(targets, (t) => {
  if (!t.some((x) => x.value === target.value)) target.value = t[0]?.value ?? ''
}, { immediate: true })
const making = ref(false)
const linkError = ref('')
const madeLink = ref<{ url: string; loopback: boolean } | null>(null)
const madeQr = computed(() => (madeLink.value ? qrSvg(madeLink.value.url) : null))
async function makeDecisionLink() {
  const [kind, id] = [target.value.slice(0, target.value.indexOf(':')), target.value.slice(target.value.indexOf(':') + 1)]
  if (!id) return
  making.value = true
  linkError.value = ''
  madeLink.value = null
  try {
    const body = { role: 'control' as const, decide: true, label: 'Decisions on a phone', ttlSeconds: 7200 }
    const res = kind === 'run' ? await api.createRunLink(id, body) : await api.createLink(id, body)
    madeLink.value = { url: res.url, loopback: phoneOffer(new URL(res.url).origin).kind === 'loopback' }
  } catch (e) {
    // A run let go as it was picked answers as Share does: why no link is made, in the same words.
    linkError.value = (e instanceof ApiError ? shareRefusal(e.code)?.text : undefined) ?? (e as Error).message
  } finally {
    making.value = false
  }
}
watch(open, (o) => {
  if (!o) {
    madeLink.value = null
    linkError.value = ''
  }
})
</script>

<template>
  <UModal v-model:open="open" title="Open on your phone" :description="phoneWords">
    <template #body>
      <div v-if="offer?.kind === 'qr' && qr" class="flex flex-col items-center gap-3" data-phone-offer="qr" :data-phone-url="offer.url">
        <svg :viewBox="qr.viewBox" class="size-48 rounded bg-white text-black" shape-rendering="crispEdges" role="img" :aria-label="offer.url" data-phone-qr>
          <path :d="qr.path" fill="currentColor" />
        </svg>
        <code class="break-all text-sm">{{ offer.url }}</code>
      </div>
      <div v-else-if="offer?.kind === 'loopback'" class="flex flex-col gap-3" data-phone-offer="loopback">
        <p class="text-sm text-pretty">{{ offer.words }}</p>
        <template v-if="targets.length">
          <p class="text-sm text-pretty text-muted">A decision link opens on a phone instead: what it covers is answered there, through the switchyard this server publishes to or its public address. Pick a run or a session.</p>
          <div class="flex flex-wrap items-center gap-2">
            <USelect v-model="target" :items="targets" class="min-w-0 flex-1" aria-label="What the decision link covers" data-phone-target />
            <UButton label="Make a decision link" icon="i-lucide-link" :loading="making" :disabled="!target" class="min-h-11" data-phone-make-link @click="makeDecisionLink" />
          </div>
        </template>
        <p class="text-xs text-pretty text-muted" data-phone-not-offered>{{ PHONE_NOT_OFFERED }}</p>
        <UAlert v-if="linkError" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="linkError" data-phone-link-error />
        <div v-if="madeLink && madeQr" class="flex flex-col items-center gap-3" data-phone-link :data-phone-url="madeLink.url">
          <svg :viewBox="madeQr.viewBox" class="size-48 rounded bg-white text-black" shape-rendering="crispEdges" role="img" :aria-label="madeLink.url" data-phone-link-qr>
            <path :d="madeQr.path" fill="currentColor" />
          </svg>
          <code class="break-all text-sm">{{ madeLink.url }}</code>
          <p v-if="madeLink.loopback" class="text-xs text-warning text-pretty">This link's address is this computer's own too: a phone reaches a decision link where this server publishes to a switchyard or has a public address.</p>
          <p v-else class="text-xs text-muted text-pretty">The code holds the link: anyone who scans it answers what it covers, for two hours, until you revoke it from Share.</p>
        </div>
      </div>
    </template>
  </UModal>
</template>
