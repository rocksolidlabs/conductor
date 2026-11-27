<script setup lang="ts">
// The one fold of a form's rarely changed fields, named More options (Launch agent, the agent trial's form). Closed, the line beside
// its button names only what the person changed inside (utils/moreOptions.ts makes it), so an untouched fold says nothing. Its body
// stays mounted while closed: the form's values and checks inside it live on, and a check can open it to show a field.
// Marks: data-more-options (the button), data-more-line (the closed line), data-more (the body). A spec reaches the body through
// openMore (web/e2e/fixtures.ts). What a person uses all the time (Yolo) never goes in here (docs/design/system.md).
const open = defineModel<boolean>('open', { default: false })
const props = withDefaults(defineProps<{
  /** What the person changed inside, said while closed; '' says nothing. */
  line?: string
  /** The button's words. */
  label?: string
}>(), { line: '', label: 'More options' })
defineSlots<{
  /** The fields. */
  default(): unknown
  /** The closed line drawn by the caller (a part in the code font), in place of `line`. */
  line?(): unknown
}>()

const bodyId = useId()
</script>

<template>
  <div class="flex min-w-0 flex-col">
    <div class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
      <UButton
        :label="props.label"
        :icon="open ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'"
        color="neutral"
        variant="ghost"
        size="sm"
        class="-ml-2.5 max-sm:min-h-11"
        :aria-expanded="open"
        :aria-controls="bodyId"
        data-more-options
        @click="open = !open"
      />
      <span v-if="!open && ($slots.line || props.line)" class="min-w-0 text-xs text-muted [overflow-wrap:anywhere]" data-more-line>
        <slot name="line">{{ props.line }}</slot>
      </span>
    </div>
    <UCollapsible v-model:open="open" :unmount-on-hide="false" :ui="{ content: 'pt-3' }">
      <template #content>
        <div :id="bodyId" class="flex flex-col gap-4" data-more>
          <slot />
        </div>
      </template>
    </UCollapsible>
  </div>
</template>
