<script setup lang="ts">
import type { BriefItem, RefResolution } from '~/utils/brakeman'
import { refLabel } from '~/utils/brakeman'
import { COMMENT_MAX, handoffMarkdown, handoffTickets, utf8Bytes } from '~/utils/brakemanCards'

/**
 * A handoff package (docs/plans/brakeman.md, PR-7), built from Conductor's records and never an agent's conversation: the objective,
 * the constraints, the decisions made, the revision, the open questions and the evidence, as Markdown; Copy; and, for a ticket its
 * refs name, "Comment on the ticket…": the shared comment dialog (BrakemanCommentTicket), Dispatch's comment operation as the admin,
 * under the person's name. A package longer than a comment holds (Dispatch's 16 KiB) is commented as the person shortens it in the
 * dialog: Copy keeps the whole.
 */
const props = defineProps<{ item: Extract<BriefItem, { kind: 'handoff/1' }>; refs: ReadonlyMap<string, RefResolution> }>()

const copy = useCopy()

const markdown = computed(() => handoffMarkdown(props.item, (r) => refLabel(r)))
const tickets = computed(() => handoffTickets(props.item))
const over = computed(() => utf8Bytes(markdown.value) > COMMENT_MAX)
const kib = (n: number) => `${(n / 1024).toFixed(1)} KiB`
const commented = ref<Record<string, number>>({})
const commenting = ref(false)
const pick = ref('')
function comment(key: string) {
  pick.value = key
  commenting.value = true
}
</script>

<template>
  <div class="flex flex-col gap-2 rounded-md border border-default p-3" data-brief-handoff>
    <MarkdownPreview :text="markdown" dir="" :relative-links="false" class="text-sm" data-handoff-markdown />
    <p v-if="over && tickets.length" class="text-xs text-muted" data-handoff-bound>
      The package is {{ kib(utf8Bytes(markdown)) }}, more than a ticket's comment holds ({{ kib(COMMENT_MAX) }}). Copy keeps the whole; a comment sends what you shorten it to.
    </p>
    <div class="flex flex-wrap items-center gap-2">
      <UButton label="Copy" icon="i-lucide-copy" size="sm" color="neutral" variant="outline" class="max-sm:min-h-11" data-handoff-copy @click="copy(markdown, 'The handoff is copied')" />
      <UButton
        v-for="key in tickets"
        :key="key"
        :label="commented[key] !== undefined ? `Commented on ${key}` : `Comment on ${key}…`"
        icon="i-lucide-message-square-plus"
        size="sm"
        color="neutral"
        variant="ghost"
        class="max-sm:min-h-11"
        :disabled="commented[key] !== undefined"
        :data-handoff-comment="key"
        @click="comment(key)"
      />
    </div>
    <BrakemanCommentTicket v-if="tickets.length" v-model:open="commenting" :text="markdown" :keys="tickets" :pick="pick" what="The handoff" @commented="(k, n) => (commented = { ...commented, [k]: n })" />
  </div>
</template>
