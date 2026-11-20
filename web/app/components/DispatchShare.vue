<script setup lang="ts">
import type { DispatchProject, ProjectReply } from '~/composables/useDispatch'
import { dispatchFailureWords } from '~/composables/useDispatchOps'
import { mintCommandId } from '~/utils/tasks'
import { shareWords } from '~/utils/dispatchShare'

/**
 * A project's share (docs/plans/sync-seams.md, OS-1b and OS-4), shown only with a sync store registered: its state and words, **Share
 * this project** while it is not shared, and **Give this project another key** for a project refused for its key or holding another
 * machine's rows back for one (project.rekey). The project read after a write is the caller's to apply (`changed`, `rekeyed`).
 */
const props = defineProps<{ project: DispatchProject; archived: boolean }>()
const emit = defineEmits<{ changed: [project: DispatchProject]; rekeyed: [key: string] }>()
const caps = useCapabilities()
const { request } = useApi()
const shown = computed(() => caps.has('sync'))
const words = computed(() => shareWords(props.project))
const canShare = computed(() => !props.archived && (!props.project.share || props.project.share.state === 'refused'))
const canRekey = computed(() => !props.archived && (props.project.share?.state === 'refused' || !!props.project.heldBy))
const busy = ref(false)
const failure = ref('')
const newKey = ref('')

async function send(path: string, body: Record<string, unknown>): Promise<ProjectReply | null> {
  busy.value = true
  failure.value = ''
  try {
    return await request<ProjectReply>(path, { method: 'POST', body: { opId: mintCommandId(), ...body } })
  } catch (e) {
    failure.value = dispatchFailureWords(e)
    return null
  } finally {
    busy.value = false
  }
}

async function share() {
  const r = await send(`/api/dispatch/projects/${encodeURIComponent(props.project.key)}/share`, {})
  if (r?.project) emit('changed', r.project)
}

async function rekey() {
  const key = newKey.value.trim().toUpperCase()
  const r = await send(`/api/dispatch/projects/${encodeURIComponent(props.project.key)}/rekey`, { key })
  if (r?.receipt.status === 'done') emit('rekeyed', key)
}
</script>

<template>
  <section
    v-if="shown"
    class="flex flex-col gap-2 rounded-lg p-3 text-sm ring ring-default"
    data-project-share
    :data-share-state="project.share?.state ?? 'none'"
  >
    <p class="text-default" data-project-share-words>{{ words }}</p>
    <div class="flex flex-wrap items-center gap-2">
      <UButton v-if="canShare" label="Share this project" icon="i-lucide-share-2" size="sm" :loading="busy" data-project-share-button @click="share" />
      <template v-if="canRekey">
        <UInput v-model="newKey" aria-label="Another key" placeholder="NEW" size="sm" class="w-28" maxlength="6" data-project-rekey-input />
        <UButton
          label="Give this project another key"
          icon="i-lucide-key-round"
          size="sm"
          color="neutral"
          variant="outline"
          :loading="busy"
          :disabled="!newKey.trim()"
          data-project-rekey-button
          @click="rekey"
        />
      </template>
    </div>
    <p v-if="failure" class="text-xs text-error" data-project-share-failure>{{ failure }}</p>
  </section>
</template>
