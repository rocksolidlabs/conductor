<script setup lang="ts">
import type { CaptureSource, StoredElement } from '~/utils/capture'
import { elementSummary, hitWords, sourceLink, elementStateWords } from '~/utils/capture'
import type { ElementReport } from '~/utils/protocol'

/**
 * An element of a capture (docs/plans/browser-evidence.md, "The editor"): its summary (tag, accessible name, a selector, its box), what
 * a click at its centre reaches when something covers it, its disabled, hidden, inert and aria state, the sources the server checked
 * (each a link opening the file in the attempt's editor at its line, only when the server found it in the tree), and, folded, its
 * HTML, its neighbourhood and its computed styles. Everything the page gave is drawn as text, never as HTML: the HTML in CodeBlock.
 */
const props = defineProps<{
  element: ElementReport | StoredElement
  sources?: CaptureSource[]
  /** The attempt's session, where a found source opens; none for a capture whose session is gone. */
  session?: string
}>()

const summary = computed(() => elementSummary(props.element))
const hit = computed(() => hitWords(props.element))
const state = computed(() => elementStateWords(props.element.state))
const box = computed(() => {
  const b = props.element.box
  return `${Math.round(b.width)} × ${Math.round(b.height)} at ${Math.round(b.x)}, ${Math.round(b.y)}`
})
const styles = computed(() => (props.element.styles ?? []).map(([k, v]) => `${k}: ${v};`).join('\n'))
/** The hints the page gave, before the server checked them (a draft's). */
const hints = computed(() => (props.sources ? [] : (props.element.hints ?? []).map((h) => `${h.file}${h.line ? `:${h.line}` : ''}${h.component ? ` · ${h.how} component ${h.component}` : ` · ${h.how}`}`)))
function sourceTo(s: CaptureSource): string | undefined {
  const link = sourceLink(s)
  if (!link || !props.session) return undefined
  return `/sessions/${encodeURIComponent(props.session)}?open=${encodeURIComponent(link.path)}${link.line ? `&line=${link.line}` : ''}`
}
function sourceWords(s: CaptureSource): string {
  const where = s.outside ? 'outside the tree' : s.found ? `${s.class}, in the tree` : `${s.class}, not in the tree`
  return `${s.component ? `${s.how} component ${s.component} · ` : ''}${where}`
}
</script>

<template>
  <div class="flex min-w-0 flex-col gap-1.5 text-xs" data-capture-element>
    <div class="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5">
      <code class="min-w-0 break-all text-highlighted" data-capture-element-summary>{{ summary }}</code>
      <span class="text-muted" data-mono="measure">{{ box }}</span>
    </div>
    <code class="min-w-0 break-all text-muted" data-capture-element-selector>{{ element.selector }}</code>
    <p v-if="hit" class="text-default" data-annotation-hit>{{ hit }}</p>
    <p v-if="state.length" class="text-muted" data-capture-element-state>{{ state.join(' · ') }}</p>
    <ul v-if="sources?.length" class="flex flex-col gap-0.5" data-capture-sources>
      <li v-for="(s, i) in sources" :key="i" class="flex min-w-0 flex-wrap items-baseline gap-x-2" :data-capture-source="s.path" :data-source-found="s.found ? '' : undefined">
        <NuxtLink v-if="sourceTo(s)" :to="sourceTo(s)" class="min-w-0 break-all text-primary hover:underline" data-mono="path">{{ s.path }}{{ s.line ? `:${s.line}` : '' }}</NuxtLink>
        <span v-else class="min-w-0 break-all font-mono" data-mono="path">{{ s.path }}{{ s.line ? `:${s.line}` : '' }}</span>
        <span class="text-muted">{{ sourceWords(s) }}</span>
      </li>
    </ul>
    <p v-else-if="sources" class="text-muted">The page gives no source.</p>
    <ul v-else-if="hints.length" class="flex flex-col gap-0.5" data-capture-hints>
      <li v-for="(h, i) in hints" :key="i" class="min-w-0 break-all font-mono text-muted" data-mono="path">{{ h }}</li>
    </ul>
    <p v-else class="text-muted">The page gives no source.</p>
    <details v-if="element.html" class="min-w-0">
      <summary class="cursor-pointer text-muted">HTML</summary>
      <CodeBlock :text="element.html" name="The element's HTML" />
    </details>
    <details v-if="element.neighbourhood" class="min-w-0">
      <summary class="cursor-pointer text-muted">Neighbourhood</summary>
      <CodeBlock :text="element.neighbourhood" name="The element's neighbourhood" />
    </details>
    <details v-if="styles" class="min-w-0">
      <summary class="cursor-pointer text-muted">Styles</summary>
      <CodeBlock :text="styles" name="The element's styles" />
    </details>
  </div>
</template>
