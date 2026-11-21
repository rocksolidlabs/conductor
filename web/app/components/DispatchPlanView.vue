<script setup lang="ts">
import { shortCommit } from '~/utils/dispatchPlans'
import { viewTaskLines, type PlanViewDoc } from '~/utils/planView'

/**
 * A plan revision made on another machine, drawn from its view (sync's seams, OS-1a): the commit it starts from, its tasks with who does
 * each, the agents by name, the references, what it hands over, the constraints, the decisions and the note. Its crew, its commands and
 * its paths stay on the machine that made it, which alone launches it. One column.
 */
const props = defineProps<{ view: PlanViewDoc }>()

const tasks = computed(() => viewTaskLines(props.view))
</script>

<template>
  <div class="flex flex-col gap-3 text-sm" data-plan-view>
    <p class="text-muted">
      Starts from <code class="font-mono text-xs" data-mono="commit" :title="view.base">{{ shortCommit(view.base) }}</code>, with
      {{ view.agents.length === 1 ? 'the agent' : 'the agents' }} {{ view.agents.join(', ') }}.
    </p>
    <div class="flex flex-col gap-1">
      <h4 class="text-xs font-semibold text-muted">Tasks</h4>
      <ul class="flex flex-col gap-1.5">
        <li v-for="t in tasks" :key="t.id" class="flex flex-col gap-0.5" :data-plan-view-task="t.id">
          <span class="font-medium text-highlighted">{{ t.line }}</span>
          <span class="whitespace-pre-wrap text-muted">{{ t.instructions }}</span>
        </li>
      </ul>
    </div>
    <div v-if="view.references.length" class="flex flex-col gap-1">
      <h4 class="text-xs font-semibold text-muted">References</h4>
      <ul class="flex flex-col gap-0.5">
        <li v-for="r in view.references" :key="r.path + (r.start ?? '')" class="font-mono text-xs" data-mono="path">
          {{ r.path }}<template v-if="r.start">:{{ r.start }}<template v-if="r.end">-{{ r.end }}</template></template>
          <span v-if="r.note" class="font-sans text-muted"> {{ r.note }}</span>
        </li>
      </ul>
    </div>
    <p v-if="view.attachments.length || view.comments.length" class="text-muted" data-plan-view-handed>
      Hands over {{ view.attachments.map((a) => a.name).join(', ') }}<template v-if="view.attachments.length && view.comments.length"> and </template><template v-if="view.comments.length">{{ view.comments.length === 1 ? 'a comment' : `${view.comments.length} comments` }}</template>.
    </p>
    <p v-if="view.constraints" class="whitespace-pre-wrap"><span class="text-muted">Constraints:</span> {{ view.constraints }}</p>
    <ul v-if="view.decisions.length" class="flex flex-col gap-0.5">
      <li v-for="d in view.decisions" :key="d.id"><span class="text-muted">Decision before {{ d.before.join(', ') }}:</span> {{ d.question }}</li>
    </ul>
    <p v-if="view.note" class="whitespace-pre-wrap"><span class="text-muted">Note:</span> {{ view.note }}</p>
  </div>
</template>
