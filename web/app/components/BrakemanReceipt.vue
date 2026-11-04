<script setup lang="ts">
import type { TicketComment } from '~/composables/useDispatch'
import { parseRef } from '~/utils/brakeman'
import { commentAuthor } from '~/utils/dispatch'

/**
 * A record a brief cites that no page shows by itself, read from the API with the token as it is opened (docs/plans/brakeman.md,
 * PR-6; `receiptPath` names the route): a check receipt's log, with its SHA-256 beside the one kept when the brief was; a command
 * receipt; a Dispatch receipt of the admin's; a comment, wherever it falls among its ticket's. A link would carry no token, so these
 * are fetched, never followed.
 */
const props = defineProps<{ refName: string; path: string; label: string; keptSha256?: string }>()

const { request, requestText } = useApi()
const open = ref(false)
const loading = ref(false)
const error = ref('')
const text = ref('')
const sha256 = ref('')
const comment = ref<TicketComment | null>(null)
const kind = computed(() => parseRef(props.refName)?.kind ?? '')

async function read() {
  open.value = true
  loading.value = true
  error.value = ''
  text.value = ''
  sha256.value = ''
  comment.value = null
  try {
    if (kind.value === 'check') {
      const r = await requestText(props.path)
      text.value = r.text
      sha256.value = r.headers.get('X-Conductor-Log-SHA256') ?? ''
    } else if (kind.value === 'comment') {
      const r = await request<{ comments?: TicketComment[] }>(props.path)
      comment.value = r.comments?.[0] ?? null
      if (!comment.value) error.value = 'The comment is not there any more.'
    } else {
      text.value = JSON.stringify(await request<unknown>(props.path), null, 2)
    }
  } catch (e) {
    const err = e as { status?: number; code?: string; message?: string }
    error.value =
      err.status === 410 || err.code === 'log_dropped'
        ? "The log was not kept: the run's logs were at their bound. Its SHA-256 stays on the receipt."
        : err.status === 404
          ? 'The server holds no such record now.'
          : err.message || 'The record could not be read.'
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <UButton icon="i-lucide-receipt-text" size="xs" color="neutral" variant="link" class="p-0" :aria-label="`Read ${label}`" :title="`Read ${label}`" data-ref-receipt @click.prevent="read" />
  <UModal v-model:open="open" :title="label" :description="refName" :ui="{ content: 'sm:max-w-3xl' }">
    <template #body>
      <div class="flex flex-col gap-2" data-ref-receipt-modal>
        <p v-if="loading" class="text-sm text-muted">Reading…</p>
        <UAlert v-else-if="error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" data-ref-receipt-error />
        <template v-else-if="comment">
          <p class="text-xs text-muted">#{{ comment.n }} · {{ commentAuthor(comment) }}<template v-if="comment.deletedAt"> · deleted</template></p>
          <MarkdownPreview :text="comment.body ?? ''" dir="" :relative-links="false" class="text-sm" />
        </template>
        <pre v-else class="max-h-[60vh] overflow-auto whitespace-pre-wrap break-words rounded bg-elevated/40 p-2 font-mono text-xs" data-ref-receipt-text>{{ text }}</pre>
        <p v-if="sha256" class="break-all font-mono text-[11px] text-muted">
          SHA-256 {{ sha256 }}<template v-if="keptSha256"> · {{ keptSha256 === sha256 ? 'the one kept with the brief' : `the brief kept ${keptSha256}` }}</template>
        </p>
      </div>
    </template>
  </UModal>
</template>
