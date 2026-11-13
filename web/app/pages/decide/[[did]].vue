<script setup lang="ts">
import { DECIDE_CARDS } from '~/utils/protocol'
import { shownName } from '~/utils/identityPass'

/**
 * The decide page (docs/plans/decide-grant.md, "web/app"): what an identity provider's decide principal sees, alone, at `/decide` and
 * `/decide/<did>` (that decision drawn first). No sidebar, palette, Needs you or token dialog: the bare layout, and the decide
 * middleware keeps a tab holding a pass on this page. "Signed in as" the name an answer is shown under; the open cards one under
 * another, then the list's other decisions as rows with Open. Everything it reads and sends is useDecide's.
 */
definePageMeta({ layout: 'bare' })
useHead({ title: 'Decide · Conductor' })

const route = useRoute()
const d = useDecide(typeof route.params.did === 'string' ? route.params.did : undefined)
const page = d.page
const shown = computed(() => page.cards.filter((c) => c.open || c.end))
const rows = computed(() => page.list.entries.filter((e) => !shown.value.some((c) => c.id === e.id)))
const full = computed(() => d.openCount() >= DECIDE_CARDS)
</script>

<template>
  <main class="flex min-h-0 flex-1 flex-col overflow-y-auto" data-decide-page>
    <div class="mx-auto flex w-full max-w-3xl flex-col gap-4 px-4 py-6">
      <header class="flex min-w-0 items-center gap-3">
        <img src="/brand/conductor-mark.svg" alt="" class="size-7 dark:hidden" />
        <img src="/brand/conductor-mark-reversed.svg" alt="" class="hidden size-7 dark:block" />
        <div class="min-w-0">
          <h1 class="text-lg font-semibold tracking-tight">Decisions for you</h1>
          <p v-if="page.me" class="break-words text-sm text-muted" data-decide-signed-in>Signed in as {{ shownName(page.me.name, page.me.subject) }}</p>
        </div>
      </header>

      <UAlert v-if="page.ended" color="error" variant="subtle" icon="i-lucide-log-out" title="Your access to this server has ended. Open it again from your workspace page." data-decide-ended />
      <UAlert v-else-if="page.notForIt" color="warning" variant="subtle" icon="i-lucide-shield-alert" title="This page answers a decision routed to you; this pass is not for it." data-decide-not-for-it />
      <p v-else-if="!page.me" class="flex items-center gap-2 text-sm text-muted"><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Checking your access…</p>

      <DecideCard v-for="c in shown" :key="c.id" :card="c" :answerable="d.answerable(c)" :closable="d.closable(c)" :ended="page.ended" @send="d.send(c.id)" @close="d.close(c.id)" @dismiss="d.dismiss(c.id)" />

      <UAlert v-if="page.list.loaded && page.list.suspended && !page.ended" color="neutral" variant="subtle" icon="i-lucide-loader-circle" title="Checking which decisions await you here." data-decide-list-suspended />
      <UAlert v-if="page.list.cut" color="neutral" variant="subtle" icon="i-lucide-info" title="More may await your answer than this page lists; a decision's own link opens it." data-decide-list-cut />
      <ul v-if="rows.length" class="flex flex-col gap-2" data-decide-rows>
        <li v-for="e in rows" :key="e.id" class="flex min-w-0 items-center justify-between gap-3 rounded-md border border-default px-3 py-2" :data-decide-row="e.id">
          <span class="min-w-0 break-words text-sm">{{ e.id }} awaits your answer.</span>
          <UButton label="Open" color="neutral" variant="outline" size="sm" class="min-h-11" :disabled="full || page.list.suspended || page.ended" data-decide-open @click="d.open(e.id)" />
        </li>
      </ul>
      <p v-if="rows.length && full" class="text-xs text-muted" data-decide-full>Four are open: close one to open another.</p>
      <p v-if="page.list.loaded && !page.list.entries.length && !shown.length && !page.ended" class="py-6 text-center text-sm text-muted" data-decide-empty>Nothing awaits your answer here.</p>
    </div>
  </main>
</template>
