<script setup lang="ts">
import { parseRef } from '~/utils/brakeman'
import { COMMENT_MAX, utf8Bytes } from '~/utils/brakemanCards'
import { mintCommandId, sendCommand } from '~/utils/tasks'
import { dispatchFailureWords } from '~/composables/useDispatchOps'

/**
 * Comment on a ticket with Brakeman's words: a dialog that posts `text` as a comment on the ticket the person picks (the tickets `keys`
 * names first, or any key typed), through Dispatch's comment operation as the admin, under the person's name, with a fresh opId. The
 * text is in a field the person may edit before sending; past a comment's bound (Dispatch's 16 KiB, in UTF-8 bytes) it is not sent
 * until the person shortens it there: nothing is ever cut silently. The confirm says it posts under the person's name, and, with
 * `quotes`, that the text (the brief, the handoff: `what`) may quote a terminal screen or a check log. Brakeman's own turns never comment: only a person, here.
 */
const props = defineProps<{ text: string; keys: readonly string[]; what: string; quotes?: boolean; pick?: string }>()
const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ commented: [key: string, n: number] }>()

const dispatch = useDispatchApi()
const identity = useIdentity()
const toast = useToast()

const key = ref('')
const typed = ref('')
const body = ref('')
// Each opening starts from the text as it is now and the ticket asked for (the first offered, else none).
watch(
  open,
  (o) => {
    if (!o) return
    body.value = props.text
    key.value = props.pick || props.keys[0] || ''
    typed.value = ''
  },
  { immediate: true },
)
/** The key the comment goes to: one typed in, or the one picked; "" while none is a ticket's key. */
const target = computed(() => {
  const k = typed.value.trim() || key.value
  return k && parseRef(`ticket:${k}`)?.kind === 'ticket' ? k : ''
})
const typedBad = computed(() => typed.value.trim() !== '' && !target.value)
const bytes = computed(() => utf8Bytes(body.value))
const over = computed(() => bytes.value > COMMENT_MAX)
const fits = computed(() => body.value.trim() !== '' && !over.value)
const kib = (n: number) => `${(n / 1024).toFixed(1)} KiB`
const name = computed(() => identity.name.value.trim())
const busy = ref(false)

async function send() {
  const k = target.value
  if (!k || !fits.value || busy.value) return
  busy.value = true
  try {
    const reply = await sendCommand((b) => dispatch.addComment(k, b), { opId: mintCommandId(), body: body.value, ...(name.value ? { byName: name.value } : {}) })
    toast.add({ title: `${props.what} is a comment on ${k}`, icon: 'i-lucide-message-square', color: 'success' })
    emit('commented', k, reply.comment?.n ?? 0)
    open.value = false
  } catch (e) {
    toast.add({ title: `${k}: not commented`, description: dispatchFailureWords(e), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <UModal v-model:open="open" title="Comment on a ticket" :description="`${what} goes to the ticket you pick, as a comment under your name.`">
    <template #body>
      <div class="flex flex-col gap-3" data-comment-ticket :data-comment-target="target">
        <div class="flex flex-col gap-1.5">
          <span class="text-xs font-medium text-muted">Ticket</span>
          <div v-if="keys.length" class="flex flex-wrap gap-1.5">
            <UButton
              v-for="k in keys"
              :key="k"
              :label="k"
              size="sm"
              color="neutral"
              :variant="!typed.trim() && key === k ? 'subtle' : 'outline'"
              :aria-pressed="!typed.trim() && key === k ? 'true' : 'false'"
              class="max-sm:min-h-11"
              :data-comment-key="k"
              @click="(key = k), (typed = '')"
            />
          </div>
          <UInput v-model="typed" :placeholder="keys.length ? 'Or another ticket, as KEY-12' : 'A ticket, as KEY-12'" aria-label="Another ticket's key" class="w-full max-w-60" data-comment-key-input />
          <p v-if="typedBad" class="text-xs text-error" data-comment-key-bad>That is not a ticket's key.</p>
        </div>
        <div class="flex flex-col gap-1.5">
          <span class="text-xs font-medium text-muted">The comment</span>
          <UTextarea v-model="body" :rows="8" autoresize :maxrows="16" aria-label="The comment" class="w-full text-xs" data-comment-body />
          <p class="text-xs" :class="over ? 'text-error' : 'text-muted'" data-comment-size>{{ kib(bytes) }} of {{ kib(COMMENT_MAX) }}</p>
          <p v-if="over" class="text-xs text-error" data-comment-bound>More than a ticket's comment holds: shorten it above to send it. Nothing is cut for you.</p>
        </div>
        <p class="text-xs text-muted" data-comment-confirm>
          {{ name ? `It posts under your name, ${name}.` : 'It posts as the admin, with no name: set your name in Settings to sign it.' }}
          <template v-if="quotes">{{ ` ${what} may quote a terminal screen or a check log.` }}</template>
        </p>
      </div>
    </template>
    <template #footer>
      <div class="flex w-full justify-end gap-2">
        <UButton label="Keep it" color="neutral" variant="ghost" @click="open = false" />
        <UButton :label="target ? `Comment on ${target}` : 'Comment'" icon="i-lucide-message-square-plus" :loading="busy" :disabled="!target || !fits" data-comment-send @click="send" />
      </div>
    </template>
  </UModal>
</template>
