<script setup lang="ts">
import type { RefResolution } from '~/utils/brakeman'
import { commentAuthor } from '~/utils/dispatch'

/**
 * A record a brief cites that no page shows by itself, read as it is opened (docs/plans/brakeman.md, PR-6): a check receipt's log, with
 * its SHA-256 beside the one kept when the brief was; a command receipt; a Dispatch receipt of the admin's; a comment, wherever it falls
 * among its ticket's. A link would carry no token, so these are fetched, never followed. The read is the injected access's
 * (useRefAccess, docs/plans/decision-inbox.md PR-4): today's reads the API with the token (`receiptPath` names the route); a decision
 * link's page reads a check's log through its lane.
 */
const props = defineProps<{ refName: string; label: string; keptSha256?: string; resolution?: RefResolution }>()

const { open, loading, error, text, sha256, shown, comment, read } = useRefReceipt({ ref: () => props.refName, resolution: () => props.resolution })
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
        <template v-else>
          <!-- A text that is part of the log its SHA-256 is of (a link's last 4 KiB, a dropped log's kept end) says so first. -->
          <p v-if="shown" class="text-xs text-muted text-pretty" data-ref-receipt-shown>{{ shown }}</p>
          <pre class="max-h-[60vh] overflow-auto whitespace-pre-wrap break-words rounded bg-elevated/40 p-2 font-mono text-xs" data-ref-receipt-text>{{ text }}</pre>
        </template>
        <p v-if="sha256" class="break-all font-mono text-[11px] text-muted" data-ref-receipt-sha256>
          SHA-256<template v-if="shown"> of the whole log</template> {{ sha256 }}<template v-if="keptSha256"> · {{ keptSha256 === sha256 ? 'the one kept with the brief' : `the brief kept ${keptSha256}` }}</template>
        </p>
      </div>
    </template>
  </UModal>
</template>
