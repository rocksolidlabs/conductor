<script setup lang="ts">
import type { TransportKind } from '~/utils/protocol'
import type { TransportState } from '~/utils/transport/types'
import { PasteTransport } from '~/utils/transport/paste'
import { isPasteBlob } from '~/utils/paste'

/**
 * Join by paste: no server between you and the session. This page gathers
 * your side of a WebRTC connection into an invite, which you send the person
 * sharing through any messenger; their Conductor answers with a blob of its
 * own, which you paste back here; the terminal then runs between the two
 * machines alone. Works for most home-to-home pairs; a carrier NAT or an
 * office network on either side needs a switchyard instead.
 */
definePageMeta({ layout: 'bare' })
useHead({ title: 'Join by paste' })

const api = useSessions()
const identity = useIdentity()
const copy = useCopy()

const nameDraft = ref('')
const stun = ref<string[]>([])
const offer = ref('')
const making = ref(false)
const answer = ref('')
const error = ref('')
const joined = ref(false)
const transport = ref<{ kind: TransportKind; state: TransportState; rtt: number | null }>({ kind: 'webrtc', state: 'idle', rtt: null })
let pending: PasteTransport | null = null

onMounted(async () => {
  nameDraft.value = identity.name.value
  try {
    stun.value = (await api.ice()).stun
  } catch {
    stun.value = ['stun:stun.l.google.com:19302']
  }
})

/** Step 1: the invite, gathered from this browser's candidates. */
async function makeInvite() {
  making.value = true
  error.value = ''
  try {
    identity.set(nameDraft.value)
    const t = new PasteTransport(stun.value, { name: identity.name.value })
    pending = t
    offer.value = await t.offer()
    await copy(offer.value, 'Invite copied', 'Send it to the person sharing; paste their answer below.')
  } catch (e) {
    error.value = (e as Error).message
    pending = null
  } finally {
    making.value = false
  }
}

/** Step 2: their answer, pasted back; the terminal connects. */
async function connect() {
  const t = pending
  if (!t) return
  error.value = ''
  try {
    await t.accept(answer.value)
    joined.value = true
  } catch (e) {
    error.value = (e as Error).message
  }
}

/** The terminal takes the transport the invite was made with, once; a reconnect needs a new invite. */
function createTransport() {
  const t = pending!
  pending = null
  return t
}

function onTransport(s: { kind: TransportKind; state: TransportState; rtt: number | null }) {
  transport.value = s
}

const answerLooksRight = computed(() => isPasteBlob(answer.value))
</script>

<template>
  <div class="flex h-dvh flex-col bg-default text-default" data-paste-page>
    <template v-if="!joined">
      <div class="flex flex-1 items-center justify-center p-4">
        <UCard class="w-full max-w-xl" :ui="{ body: 'flex flex-col gap-5' }">
          <div class="flex items-center gap-3">
            <img src="/brand/conductor-mark.svg" alt="" class="size-8 dark:hidden" />
            <img src="/brand/conductor-mark-reversed.svg" alt="" class="size-8 hidden dark:block" />
            <div>
              <h1 class="text-lg font-semibold tracking-tight">Join by paste</h1>
              <p class="text-sm text-muted">No server between you and the session: two blobs through any messenger, then the terminal runs machine to machine.</p>
            </div>
          </div>
          <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" data-paste-error />

          <section class="flex flex-col gap-2" data-paste-step="1">
            <h2 class="text-sm font-semibold">1. Your invite</h2>
            <UFormField label="Your name" description="What the others see you as.">
              <UInput v-model="nameDraft" class="w-full" maxlength="40" />
            </UFormField>
            <UButton v-if="!offer" label="Make my invite" icon="i-lucide-sparkles" :loading="making" :disabled="!nameDraft.trim()" class="self-end" data-paste-make @click="makeInvite" />
            <template v-else>
              <UTextarea :model-value="offer" :rows="4" readonly class="w-full font-mono text-[11px]" data-paste-offer />
              <div class="flex items-center gap-2">
                <UButton label="Copy invite" icon="i-lucide-clipboard" size="sm" color="neutral" variant="outline" @click="copy(offer, 'Invite copied', 'Send it to the person sharing.')" />
                <span class="text-xs text-muted">Send this to the person sharing. Their Conductor answers with a blob; paste it below.</span>
              </div>
            </template>
          </section>

          <section class="flex flex-col gap-2" :class="!offer && 'opacity-50'" data-paste-step="2">
            <h2 class="text-sm font-semibold">2. Their answer</h2>
            <UTextarea v-model="answer" :rows="4" :disabled="!offer" placeholder="cpi1.…" class="w-full font-mono text-[11px]" data-paste-answer />
            <UButton label="Connect" icon="i-lucide-plug" :color="offer ? 'primary' : 'neutral'" :variant="offer ? 'solid' : 'outline'" :disabled="!offer || !answerLooksRight" class="self-start" data-paste-connect @click="connect" />
          </section>
        </UCard>
      </div>
    </template>

    <template v-else>
      <header class="flex items-center gap-3 border-b border-default px-4 py-2">
        <img src="/brand/conductor-mark.svg" alt="" class="size-6 dark:hidden" />
        <img src="/brand/conductor-mark-reversed.svg" alt="" class="size-6 hidden dark:block" />
        <span class="font-semibold">Conductor</span>
        <USeparator orientation="vertical" class="h-5" />
        <span class="truncate">pasted session</span>
        <TransportBadge :kind="transport.kind" :state="transport.state" :rtt="transport.rtt" />
        <div class="flex-1" />
        <span class="text-xs text-muted hidden md:inline">you are <b class="text-default">{{ identity.name.value }}</b></span>
        <FullscreenButton size="sm" />
      </header>
      <main class="flex-1 min-h-0 p-2 sm:p-3">
        <TerminalView :create-transport="createTransport" auto-connect class="h-full" @transport="onTransport" />
      </main>
    </template>
  </div>
</template>
