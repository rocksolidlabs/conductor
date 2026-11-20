<script setup lang="ts">
import { readRefusal, readSignIn, timeLeft, withinWait } from '~/utils/shell'

/**
 * The provider's sign-in (docs/plans/phone-shell.md, OSS-1b): "Sign in to <name>", the code in large type, Open the sign-in page
 * (the provider's page, opened outside the workbench), the time left and Cancel; an answer of words shows them with Try again. Open
 * a server is a link under it in every state.
 */
const shell = useShell()
const p = shell.provider!

const started = ref<{ code: string; page: string; expiresAt: string } | null>(null)
const words = ref('')
const busy = ref(false)
const now = ref(Date.now())
let tick: ReturnType<typeof setInterval> | undefined
onMounted(() => (tick = setInterval(() => (now.value = Date.now()), 15_000)))
onBeforeUnmount(() => clearInterval(tick))
const left = computed(() => (started.value ? timeLeft(started.value.expiresAt, now.value) : ''))

async function begin() {
  if (busy.value) return
  busy.value = true
  words.value = ''
  try {
    const r = readSignIn(await withinWait(p.signIn(), `${shell.name} did not answer: no sign-in began.`))
    if ('code' in r) {
      started.value = r
      now.value = Date.now()
    } else {
      started.value = null
      words.value = r.words
    }
  } catch (e) {
    started.value = null
    words.value = readRefusal(e, 'No sign-in can begin now.').words
  } finally {
    busy.value = false
  }
}

async function cancel() {
  started.value = null
  await withinWait(p.cancelSignIn(), '').catch(() => {})
}

function openPage() {
  if (started.value) window.open(started.value.page, '_blank', 'noopener')
}
</script>

<template>
  <ShellHeader :title="`Sign in to ${shell.name}`" :line="shell.state.value?.words || shell.stateWords.value || undefined" />
  <section class="flex min-w-0 flex-col gap-3" data-shell-signin>
    <template v-if="started">
      <p class="text-sm text-muted">On the sign-in page, enter this code:</p>
      <p class="break-all text-center text-4xl font-semibold tracking-widest text-highlighted" aria-label="The sign-in code" data-shell-code>{{ started.code }}</p>
      <p class="text-center text-sm" :class="left ? 'text-muted' : 'text-warning'" role="status" data-shell-code-left>{{ left || 'The code has expired.' }}</p>
      <UButton :ui="{ label: 'min-w-0 whitespace-normal break-words text-start' }" v-if="left" label="Open the sign-in page" icon="i-lucide-external-link" color="primary" size="lg" class="max-w-full min-h-11 justify-center" data-shell-open-page @click="openPage" />
      <UButton :ui="{ label: 'min-w-0 whitespace-normal break-words text-start' }" v-else label="Try again" icon="i-lucide-refresh-cw" color="primary" size="lg" class="max-w-full min-h-11 justify-center" data-shell-signin-again @click="begin" />
      <UButton :ui="{ label: 'min-w-0 whitespace-normal break-words text-start' }" label="Cancel" color="neutral" variant="outline" size="lg" class="max-w-full min-h-11 justify-center" data-shell-signin-cancel @click="cancel" />
    </template>
    <template v-else>
      <p v-if="words" class="break-words text-sm text-warning" role="status" data-shell-signin-words>{{ words }}</p>
      <UButton :ui="{ label: 'min-w-0 whitespace-normal break-words text-start' }" :label="words ? 'Try again' : 'Sign in'" icon="i-lucide-log-in" color="primary" size="lg" class="max-w-full min-h-11 justify-center" :loading="busy" data-shell-signin-begin @click="begin" />
    </template>
    <ULink to="/shell/server" class="inline-flex min-h-11 items-center gap-2 self-start text-sm text-primary" data-shell-to-server>
      <UIcon name="i-lucide-server" class="size-4" /> Open a server
    </ULink>
  </section>
</template>
