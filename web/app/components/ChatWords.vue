<script setup lang="ts">
import { renderMarkdown } from '~/utils/markdown'

/**
 * One block of an agent's words in a chat (docs/plans/chat-or-terminal.md, design 11): Markdown without HTML (renderMarkdown never
 * renders raw HTML), a link only to https with its address as its title, no image. Rendered again only when its text changes, so a
 * long chat streams without rendering every block at each change.
 */
const props = defineProps<{ text: string }>()
const html = computed(() => renderMarkdown(props.text, { dir: '', relativeLinks: false, agentWords: true }).html)
</script>

<template>
  <!-- eslint-disable-next-line vue/no-v-html -->
  <div class="chat-words min-w-0 text-sm text-default" data-chat-words v-html="html" />
</template>

<style scoped>
.chat-words :deep(p) {
  margin: 0.4rem 0;
}
.chat-words :deep(pre) {
  margin: 0.5rem 0;
  overflow-x: auto;
  border-radius: 0.375rem;
  background: var(--color-forest-950);
  color: var(--color-forest-100);
  padding: 0.6rem 0.75rem;
  font-family: var(--font-mono);
  font-size: 0.75rem;
}
.chat-words :deep(code) {
  font-family: var(--font-mono);
  font-size: 0.8em;
}
.chat-words :deep(:not(pre) > code) {
  border-radius: 0.25rem;
  background: var(--ui-bg-elevated);
  padding: 0.05rem 0.3rem;
}
.chat-words :deep(ul),
.chat-words :deep(ol) {
  margin: 0.4rem 0;
  padding-left: 1.25rem;
}
.chat-words :deep(ul) {
  list-style: disc;
}
.chat-words :deep(ol) {
  list-style: decimal;
}
.chat-words :deep(a) {
  color: var(--ui-primary);
  text-decoration: underline;
  text-underline-offset: 2px;
}
.chat-words :deep(h1),
.chat-words :deep(h2),
.chat-words :deep(h3) {
  margin: 0.6rem 0 0.3rem;
  font-weight: 600;
}
.chat-words :deep(table) {
  display: block;
  overflow-x: auto;
}
</style>
