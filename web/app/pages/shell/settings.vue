<script setup lang="ts">
import { clockTime } from '~/utils/brakemanRole'
import { endingWords, notificationWords, readRefusal, readWords } from '~/utils/shell'

/**
 * The shell's Settings (docs/plans/phone-shell.md, OSS-1b): who is signed in, notifications with their state and the last one
 * received, Turn on notifications, Send a test notification with its words, Sign out (the provider makes it take effect on the phone
 * at once) and, while its end is not confirmed, the words that say so; Open a server in every state.
 */
definePageMeta({ layout: 'shell' })
useHead({ title: 'Settings' })
const shell = useShell()
const p = shell.provider
const s = shell.state

const notifyWords = ref('')
const testWords = ref('')
const outWords = ref('')
const busy = ref<'' | 'notify' | 'test' | 'out'>('')

onMounted(() => {
  if (!p) void navigateTo('/shell', { replace: true })
})

async function act(which: 'notify' | 'test' | 'out', f: () => Promise<void>) {
  if (busy.value) return
  busy.value = which
  try {
    await f()
  } finally {
    busy.value = ''
  }
}

const turnOn = () =>
  act('notify', async () => {
    try {
      notifyWords.value = (await p!.enableNotifications()) === 'on' ? 'Notifications are on.' : 'Notifications are turned off in the phone’s settings: turn them on there.'
    } catch (e) {
      notifyWords.value = readRefusal(e, 'Notifications could not be turned on.').words
    }
    await shell.refresh()
  })

const sendTest = () =>
  act('test', async () => {
    try {
      testWords.value = readWords(await p!.test(), 'A test notification was sent.')
    } catch (e) {
      testWords.value = readRefusal(e, 'The test notification was not sent.').words
    }
  })

const signOut = () =>
  act('out', async () => {
    try {
      await p!.signOut()
      outWords.value = ''
    } catch (e) {
      outWords.value = readRefusal(e, 'Signing out did not finish.').words
    }
    await shell.refresh()
  })
</script>

<template>
  <ShellHeader title="Settings" :line="shell.name" back />
  <p v-if="s?.words || shell.stateWords.value" class="break-words text-sm text-warning" role="status" data-shell-state-words>{{ s?.words || shell.stateWords.value }}</p>

  <dl class="flex min-w-0 flex-col gap-3" data-shell-settings>
    <div class="flex min-w-0 flex-col gap-1">
      <dt class="text-sm text-muted">Signed in</dt>
      <dd class="break-words" data-shell-who>{{ s?.signedIn ? s.who || 'Yes' : 'Not signed in' }}</dd>
      <dd v-if="s?.ending" class="break-words text-sm text-warning" data-shell-ending>{{ endingWords(shell.name) }}</dd>
      <dd v-if="s?.signedIn && !s.ending">
        <UButton :ui="{ label: 'min-w-0 whitespace-normal break-words text-start' }" label="Sign out" icon="i-lucide-log-out" color="neutral" variant="outline" class="max-w-full min-h-11" :loading="busy === 'out'" data-shell-sign-out @click="signOut" />
      </dd>
      <dd v-if="outWords" class="break-words text-sm text-error" role="status" data-shell-sign-out-words>{{ outWords }}</dd>
    </div>

    <div class="flex min-w-0 flex-col gap-1">
      <dt class="text-sm text-muted">Notifications</dt>
      <dd class="break-words" data-shell-notifications>{{ s ? notificationWords(s, (t) => clockTime(t)) : 'Not known' }}</dd>
      <dd v-if="s?.signedIn" class="flex flex-wrap gap-2">
        <UButton :ui="{ label: 'min-w-0 whitespace-normal break-words text-start' }" v-if="s.notifications !== 'on'" label="Turn on notifications" icon="i-lucide-bell" color="neutral" variant="outline" class="max-w-full min-h-11" :loading="busy === 'notify'" data-shell-notify-on @click="turnOn" />
        <UButton :ui="{ label: 'min-w-0 whitespace-normal break-words text-start' }" label="Send a test notification" icon="i-lucide-bell-ring" color="neutral" variant="outline" class="max-w-full min-h-11" :loading="busy === 'test'" data-shell-test @click="sendTest" />
      </dd>
      <dd v-if="notifyWords" class="break-words text-sm" role="status" data-shell-notify-words>{{ notifyWords }}</dd>
      <dd v-if="testWords" class="break-words text-sm" role="status" data-shell-test-words>{{ testWords }}</dd>
    </div>

    <div class="flex min-w-0 flex-col gap-1">
      <dt class="text-sm text-muted">Another server</dt>
      <dd>
        <ULink to="/shell/server" class="inline-flex min-h-11 items-center gap-2 text-primary" data-shell-to-server>
          <UIcon name="i-lucide-server" class="size-4" /> Open a server
        </ULink>
      </dd>
    </div>
  </dl>
</template>
