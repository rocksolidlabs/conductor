<script setup lang="ts">
import { PAGES, type PageId } from '~/utils/pages'

/**
 * The one shape of a page with nothing to show (the UX review's P23, invariant 5): the page's own sentence from the pages' table
 * (`data-page-about`), why it is empty and what fills it (`data-page-why`), and the one or two actions that fill it, the first filled,
 * each with its key. While the page reads it says so, with a spinner; when its first read failed it says the server's words and offers
 * Read again; a thing not found (`missing`) says so with the way back, and no first line. The root carries `data-page-empty` and
 * `data-state`, and the attributes it is given (a page's own `data-*`).
 */
export interface PageEmptyAction {
  label: string
  icon?: string
  /** The key that does the same, drawn beside the label. */
  kbd?: string
  to?: string
  /** A link out of the app (the README), opened in a new tab. */
  external?: boolean
  onClick?: () => void
  loading?: boolean
  /** Offered beside Read again when the first read failed (the home's Set token), and not with the empty shape. */
  failed?: boolean
  /** What pressing it does, where its label alone does not say: its tooltip (TipButton). */
  tip?: string
}

const props = withDefaults(
  defineProps<{
    /** The page's id in the pages' table: its sentence is the first line. None for a page that is not a destination (the home). */
    page?: PageId
    state: 'reading' | 'failed' | 'empty' | 'missing'
    /** The second line: why the page is empty and what fills it; for `missing`, what was not found. */
    words?: string
    /** What is being read, for the reading line: "Reading the <thing>…". */
    thing?: string
    /** The server's words, when the first read failed. */
    error?: string
    actions?: PageEmptyAction[]
    /** The icon, when the page has none of its own in the table (the home). */
    icon?: string
  }>(),
  { page: undefined, words: '', thing: '', error: '', actions: () => [], icon: undefined },
)
const emit = defineEmits<{ again: [] }>()

const entry = computed(() => (props.page ? PAGES.find((p) => p.id === props.page) : undefined))
const about = computed(() => (props.state === 'missing' ? '' : (entry.value?.about ?? '')))
const glyph = computed(() => {
  if (props.state === 'missing') return 'i-lucide-search-x'
  if (props.state === 'failed') return 'i-lucide-cloud-alert'
  return props.icon ?? entry.value?.icon ?? 'i-lucide-inbox'
})
const why = computed(() => {
  if (props.state === 'reading') return props.thing ? `Reading the ${props.thing}…` : 'Reading…'
  if (props.state === 'failed') return props.error || 'The server did not answer.'
  return props.words
})
const shown = computed(() => {
  if (props.state === 'failed') return props.actions.filter((a) => a.failed).slice(0, 1)
  return props.state === 'empty' || props.state === 'missing' ? props.actions.filter((a) => !a.failed).slice(0, 2) : []
})
</script>

<template>
  <UEmpty :icon="glyph" :loading="state === 'reading'" variant="naked" size="lg" class="flex-1" :data-page-empty="page ?? ''" :data-state="state" :aria-busy="state === 'reading' ? 'true' : undefined">
    <template v-if="about" #title>
      <span class="block" data-page-about>{{ about }}</span>
    </template>
    <template #description>
      <span class="block" :class="state === 'missing' ? 'text-highlighted' : ''" data-page-why>{{ why }}</span>
    </template>
    <template v-if="shown.length || state === 'failed'" #actions>
      <UButton v-if="state === 'failed'" label="Read again" icon="i-lucide-refresh-cw" color="neutral" variant="outline" class="pointer-coarse:min-h-11" data-page-again @click="emit('again')" />
      <TipButton
        v-for="(a, i) in shown"
        :key="a.label"
        :tip="a.tip ?? ''"
        :label="a.label"
        :icon="a.icon"
        :to="a.to"
        :target="a.external ? '_blank' : undefined"
        :color="i === 0 && state !== 'failed' ? 'primary' : 'neutral'"
        :variant="i === 0 && state !== 'failed' ? 'solid' : 'outline'"
        :loading="a.loading"
        class="pointer-coarse:min-h-11"
        data-page-action
        @click="a.onClick?.()"
      >
        <template v-if="a.kbd" #trailing><UKbd :value="a.kbd" size="sm" class="opacity-70" data-page-kbd /></template>
      </TipButton>
    </template>
    <!-- What a page shows besides, under its shape (the page of what needs you: what shows up there, what was decided). -->
    <template v-if="$slots.default" #footer><slot /></template>
  </UEmpty>
</template>
