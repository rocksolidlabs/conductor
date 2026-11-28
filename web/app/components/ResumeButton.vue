<script setup lang="ts">
import { sessionPlace } from '~/utils/memberStrip'
import type { ResumeResult, SessionInfo } from '~/composables/useSessions'
import { resumeLabel } from '~/utils/yolo'
import { memberOffer, sessionResumeOffered } from '~/utils/runs'

/**
 * Starts an ended session again (POST /api/sessions/{id}/resume), or an ended member of a run that has no session left on the server
 * (`runId` and `member`: POST /api/runs/{run}/members/{name}/resume): Resume when its agent has a conversation to resume, else Relaunch.
 * The new session opens unless `stay` (the run page, where its tile appears); a relaunch toasts why it did not resume.
 */
const props = withDefaults(
  defineProps<{ session?: SessionInfo; runId?: string; member?: string; stay?: boolean; iconOnly?: boolean; size?: 'xs' | 'sm' | 'md' }>(),
  { stay: false, iconOnly: false, size: 'sm' },
)
const emit = defineEmits<{ resumed: [result: ResumeResult] }>()

const api = useSessions()
const live = useAttention()
const toast = useToast()
const busy = ref(false)
const look = computed(() => resumeLabel(props.session ?? live.runOf(props.runId ?? '')?.members.find((m) => m.name === props.member)))
/** Drawn only where the engine takes it: never for a managed run's member or attempt, nor a Brakeman turn's, nor a run not known yet (memberOffer). */
const offered = computed(() => (props.session ? sessionResumeOffered(props.session, live.runOf) : memberOffer('ended', live.runOf(props.runId ?? '')) === 'resume'))

async function go() {
  busy.value = true
  try {
    const r = props.session ? await api.resumeSession(props.session.id) : await api.resumeRunMember(props.runId!, props.member!)
    if (r.notice) toast.add({ title: `${r.session.name} started anew`, description: r.notice, icon: 'i-lucide-rotate-ccw', color: 'neutral' })
    else toast.add({ title: `${r.session.name} resumed`, icon: 'i-lucide-history', color: 'success' })
    emit('resumed', r)
    if (!props.stay) await navigateTo(sessionPlace(r.session, r.session.id))
  } catch (e) {
    toast.add({ title: 'It did not start', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <UTooltip v-if="offered" :text="look.tooltip">
    <UButton
      :icon="look.icon"
      :label="iconOnly ? undefined : look.label"
      :aria-label="look.label"
      :size="size"
      color="neutral"
      variant="outline"
      :loading="busy"
      data-resume
      :data-resume-kind="iconOnly ? 'icon' : 'label'"
      @click.stop.prevent="go"
    />
  </UTooltip>
</template>
