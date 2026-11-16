<script setup lang="ts">
/**
 * A dark code block: `text` shown as it is with one Copy, or `commands` as
 * shell lines with a Copy each. The text stays selectable for pages served
 * without a clipboard API.
 */
defineProps<{
  text?: string
  commands?: string[]
  /** What the text is, for the toast after Copy. */
  name?: string
  /** Commands wrap instead of being cut short, and one click selects a whole one: a command to copy whole, such as one carrying a token. */
  wrap?: boolean
  /** Shown dimmed, Copy off: the command is not ready. */
  disabled?: boolean
  /** The toast after Copy; the description replaces the copied text, which must not be shown when it is a secret. */
  copyTitle?: string
  copyDescription?: string
}>()

const copy = useCopy()
const copyClass = 'text-active-300 hover:bg-forest-900 hover:text-active-200'
</script>

<template>
  <div class="relative rounded-md bg-forest-950 text-forest-100" :class="disabled && 'opacity-60'" data-code-block>
    <ul v-if="commands" class="px-3 py-2 font-mono text-xs leading-relaxed">
      <li v-for="c in commands" :key="c" class="flex gap-2" :class="wrap ? 'items-start' : 'items-center'">
        <span class="select-none text-forest-400" aria-hidden="true">$</span>
        <span class="min-w-0 flex-1" :class="wrap ? 'break-all select-all' : 'truncate select-text'" :title="c">{{ c }}</span>
        <IconButton icon="i-lucide-copy" size="xs" color="neutral" variant="ghost" :label="`Copy ${c}`" :class="copyClass" :disabled="disabled" @click="copy(c, copyTitle ?? 'Command copied', copyDescription ?? c)" />
      </li>
    </ul>
    <template v-else-if="text">
      <pre class="max-h-64 overflow-auto p-3 pe-20 font-mono text-xs leading-relaxed select-text">{{ text }}</pre>
      <UButton label="Copy" icon="i-lucide-copy" size="xs" color="neutral" variant="ghost" :class="['absolute top-1.5 end-1.5', copyClass]" :disabled="disabled" @click="copy(text, copyTitle ?? 'Snippet copied', copyDescription ?? name)" />
    </template>
  </div>
</template>
