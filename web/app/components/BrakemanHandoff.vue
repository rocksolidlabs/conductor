<script setup lang="ts">
import type { BriefItem, RefResolution } from '~/utils/brakeman'
import { refLabel } from '~/utils/brakeman'
import { handoffMarkdown, handoffTickets } from '~/utils/brakemanCards'
import { mintCommandId, sendCommand } from '~/utils/tasks'
import { dispatchFailureWords } from '~/composables/useDispatchOps'

/**
 * A handoff package (docs/plans/brakeman.md, PR-7), built from Conductor's records and never an agent's conversation: the objective,
 * the constraints, the decisions made, the revision, the open questions and the evidence, as Markdown; Copy; and, for a ticket its
 * refs name, "Comment on the ticket": Dispatch's comment operation as the admin, under the person's name.
 */
const props = defineProps<{ item: Extract<BriefItem, { kind: 'handoff/1' }>; refs: ReadonlyMap<string, RefResolution> }>()

const copy = useCopy()
const dispatch = useDispatchApi()
const identity = useIdentity()
const toast = useToast()

const markdown = computed(() => handoffMarkdown(props.item, (r) => refLabel(r)))
const tickets = computed(() => handoffTickets(props.item))
const busy = ref('')
const commented = ref<Record<string, number>>({})

async function comment(key: string) {
  busy.value = key
  try {
    const name = identity.name.value.trim()
    const reply = await sendCommand((b) => dispatch.addComment(key, b), { opId: mintCommandId(), body: markdown.value, ...(name ? { byName: name } : {}) })
    commented.value = { ...commented.value, [key]: reply.comment?.n ?? 0 }
    toast.add({ title: `The handoff is a comment on ${key}`, icon: 'i-lucide-message-square', color: 'success' })
  } catch (e) {
    toast.add({ title: `${key}: not commented`, description: dispatchFailureWords(e), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    busy.value = ''
  }
}
</script>

<template>
  <div class="flex flex-col gap-2 rounded-md border border-default p-3" data-brief-handoff>
    <MarkdownPreview :text="markdown" dir="" :relative-links="false" class="text-sm" data-handoff-markdown />
    <div class="flex flex-wrap items-center gap-2">
      <UButton label="Copy" icon="i-lucide-copy" size="sm" color="neutral" variant="outline" data-handoff-copy @click="copy(markdown, 'The handoff is copied')" />
      <UButton
        v-for="key in tickets"
        :key="key"
        :label="commented[key] !== undefined ? `Commented on ${key}` : `Comment on ${key}`"
        icon="i-lucide-message-square-plus"
        size="sm"
        color="neutral"
        variant="ghost"
        :loading="busy === key"
        :disabled="commented[key] !== undefined"
        :data-handoff-comment="key"
        @click="comment(key)"
      />
    </div>
  </div>
</template>
