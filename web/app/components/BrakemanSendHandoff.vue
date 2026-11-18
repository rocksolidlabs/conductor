<script setup lang="ts">
import { ApiError } from '~/composables/useApi'
import { HAND_AS_REPORT, HAND_QUOTES, HAND_STARTS_NOTHING, type HandPreview, type HandWaiting, type SentHandoff, asWaiting, handBy, handRefusalWords, stateSentence, targetLine, underName, whoReads } from '~/utils/brakemanSend'

/**
 * Send a handoff package to a task's next attempt (issue #1012): the exact text the server will hand, read-only, every character of it
 * in a scrolling block with its size; where it goes and which attempt is handed it, by the task's state now; that sending starts
 * nothing and the agent reads it as a quoted report, not as instructions; in one fixed sentence who else can read what is sent (whoever
 * can watch the task's session or read its files, a link's holder too, and the agent may repeat it in its result); under whose name it
 * goes. Then Send or Cancel: nothing is edited and nothing is typed. A refusal is said in words; handoffs_full links each waiting
 * package's brief, where it is withdrawn.
 */
const props = defineProps<{ request: string; index: number; preview: HandPreview & { target: NonNullable<HandPreview['target']> }; quotes?: boolean }>()
const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ sent: [handoff: SentHandoff] }>()

const send = useBrakemanSend()
const identity = useIdentity()
const phone = useIsPhone()

const by = computed(() => handBy(identity.name.value))
const t = computed(() => props.preview.target)
const kib = (n: number) => `${(n / 1024).toFixed(1)} KiB`
const busy = ref(false)
const failure = ref('')
const waiting = ref<HandWaiting[]>([])
watch(open, (o) => {
  if (o) {
    failure.value = ''
    waiting.value = []
  }
})

async function confirm() {
  if (busy.value || by.value === null) return
  busy.value = true
  failure.value = ''
  waiting.value = []
  try {
    const h = await send.send(props.request, props.index, props.preview.sha256, by.value ?? '')
    emit('sent', h)
    open.value = false
  } catch (e) {
    if (e instanceof ApiError) {
      failure.value = handRefusalWords(e.status === 401 ? 'unauthorized' : e.code, { task: t.value.task, bytes: props.preview.bytes })
      if (e.code === 'handoffs_full') waiting.value = asWaiting(e.details.waiting)
    } else failure.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <UModal v-model:open="open" :title="`Send to ${t.task}'s next attempt`" :description="HAND_AS_REPORT" :fullscreen="phone">
    <template #body>
      <div class="flex min-w-0 flex-col gap-3" data-handoff-send-dialog>
        <p class="text-sm" data-handoff-send-target>{{ targetLine(t) }} {{ stateSentence(t) }}</p>
        <div class="flex min-w-0 flex-col gap-1">
          <pre class="max-h-80 overflow-auto whitespace-pre-wrap break-words rounded-md bg-elevated p-3 font-mono text-xs" tabindex="0" aria-label="The package's text, as the server will hand it" data-handoff-send-text>{{ preview.text }}</pre>
          <p class="text-xs text-muted" data-handoff-send-size>{{ kib(preview.bytes) }}, exactly as the server will hand it</p>
        </div>
        <p class="text-xs text-muted" data-handoff-send-words>{{ HAND_STARTS_NOTHING }} {{ HAND_AS_REPORT }}</p>
        <p class="text-xs text-muted" data-handoff-send-who>{{ whoReads(t.task) }}</p>
        <p class="text-xs" :class="by === null ? 'text-error' : 'text-muted'" data-handoff-send-name>{{ underName(by) }}<template v-if="quotes">{{ ` ${HAND_QUOTES}` }}</template></p>
        <div v-if="failure" class="flex flex-col gap-1" data-handoff-send-error>
          <p class="text-xs text-error">{{ failure }}</p>
          <ul v-if="waiting.length" class="flex flex-col gap-0.5 text-xs" data-handoff-send-waiting>
            <li v-for="w in waiting" :key="w.id">
              <NuxtLink :to="`/brakeman?request=${encodeURIComponent(w.request)}`" class="underline">{{ `${w.id}: ${w.task}, from ${w.request}` }}</NuxtLink>
            </li>
          </ul>
        </div>
      </div>
    </template>
    <template #footer>
      <div class="flex w-full justify-end gap-2">
        <UButton label="Cancel" color="neutral" variant="ghost" class="max-sm:min-h-11" data-handoff-send-cancel @click="open = false" />
        <UButton label="Send" icon="i-lucide-send" class="max-sm:min-h-11" :loading="busy" :disabled="by === null" data-handoff-send-confirm @click="confirm" />
      </div>
    </template>
  </UModal>
</template>
