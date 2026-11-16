<script setup lang="ts">
import type { SectionKey } from '~/utils/sidebar'
import { statusOfKey, type StatusKey } from '~/utils/status'

/**
 * A section of the sidebar's list (design 3c): a header that folds it, with the count and, while folded, a preview of what is
 * inside as status marks (StatusMark, the keys of sectionPreview). The choice is the list's to keep (useSidebarFolds); this only draws it.
 */
const props = withDefaults(defineProps<{ id: SectionKey; title: string; count: number; tone?: 'warning' | 'neutral'; preview?: StatusKey[] }>(), { tone: 'neutral', preview: () => [] })
const folded = defineModel<boolean>('folded', { default: false })

const bodyId = computed(() => `sidebar-section-${props.id}`)
</script>

<template>
  <section class="flex flex-col gap-0.5" :data-sidebar-section="id" :data-folded="folded ? 'true' : 'false'">
    <button
      type="button"
      class="flex w-full items-center gap-1.5 rounded-sm px-2 py-1 text-left text-[11px] font-semibold uppercase tracking-wider outline-none hover:bg-elevated/60 focus-visible:ring-2 focus-visible:ring-primary"
      :class="tone === 'warning' ? 'text-warning' : 'text-muted'"
      :aria-expanded="!folded"
      :aria-controls="bodyId"
      :aria-label="`${title}, ${count}`"
      @click="folded = !folded"
    >
      <UIcon :name="folded ? 'i-lucide-chevron-right' : 'i-lucide-chevron-down'" class="size-3 flex-none" />
      <!-- The title gives way first, shortened with its whole text on hover; the count keeps one line (#906). -->
      <span class="truncate" :title="title" data-section-title>{{ title }}</span>
      <span v-if="tone === 'warning'" class="flex-none whitespace-nowrap rounded-full bg-warning/20 px-1.5 text-warning tracking-normal" data-section-count>{{ count }}</span>
      <span v-else class="flex-none whitespace-nowrap tracking-normal" data-section-count>· {{ count }}</span>
      <span v-if="folded && preview.length" class="ml-auto flex items-center gap-0.5" data-section-preview aria-hidden="true">
        <StatusMark v-for="(p, i) in preview" :key="i" :status="statusOfKey(p)" />
      </span>
    </button>
    <!-- A folded section renders nothing: its rows are not in the page, for keys and tests alike. -->
    <div v-if="!folded" :id="bodyId" class="flex flex-col gap-0.5">
      <slot />
    </div>
  </section>
</template>
