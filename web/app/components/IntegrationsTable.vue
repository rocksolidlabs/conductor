<script setup lang="ts">
import type { Integration } from '~/composables/useSessions'
import { ApiError } from '~/composables/useApi'
import { noInstallText, reportStatus, snippetLeft } from '~/utils/integrations'
import { shortCwd } from '~/utils/sessions'

/**
 * Every hook adapter as a row: whether its hooks reach the agent, what it
 * reports, where an install writes, and Install on this machine. One snippet
 * at a time opens below the table: the first agent nothing wires, until the
 * person opens another or hides it.
 */
const props = defineProps<{
  integrations: Integration[]
  /** The machine the server runs on: where an install writes. */
  host: string
  /** Whether the server knows its user's home directory. */
  homeKnown: boolean
}>()
/** An install ran: the list is stale, even after a failure (files written before it stopped stay written). */
const emit = defineEmits<{ changed: [] }>()

const api = useSessions()
const toast = useToast()
const installing = ref('')

/** Conductor's hooks reach the agent, from its own config or through a launch from this server (reportStatus, the one rule). */
function wired(it: Integration): boolean {
  return reportStatus(it).key === 'reports'
}

/**
 * The status in the table, in its own longer words; the snippet's header names the host. Installed and injected are drawn neutral:
 * green is for a live agent.
 */
function status(it: Integration, long = false): { label: string; cls: string; dot: string } {
  switch (reportStatus(it).key) {
    case 'reports':
      return it.installed
        ? { label: `${it.events.length} ${it.events.length === 1 ? 'hook' : 'hooks'} installed`, cls: 'text-muted', dot: 'bg-neutral-400' }
        : { label: 'injected at launch', cls: 'text-muted', dot: 'bg-neutral-400' }
    case 'experimental':
      return { label: 'experimental', cls: 'text-muted', dot: 'bg-neutral-400' }
    default:
      return { label: long ? `not configured on ${props.host}` : 'not configured', cls: 'text-warning', dot: 'bg-warning' }
  }
}

// The open snippet: the first agent nothing wires, until the person picks another or hides it.
const chosen = ref<string | null | undefined>(undefined)
const openId = computed(() => (chosen.value !== undefined ? chosen.value : (props.integrations.find((i) => !wired(i) && i.snippet)?.id ?? null)))
const open = computed(() => props.integrations.find((i) => i.id === openId.value))
function toggle(id: string) {
  chosen.value = openId.value === id ? null : id
}

const pathsUi = { description: 'whitespace-pre-line break-all' }
const messageUi = { description: 'whitespace-pre-line [overflow-wrap:anywhere]' }

async function install(it: Integration) {
  installing.value = it.id
  try {
    const changed = await api.installIntegration(it.id)
    if (changed.length) {
      toast.add({ title: `Installed ${it.name} hooks`, description: changed.map(shortCwd).join('\n'), icon: 'i-lucide-download', color: 'success', ui: pathsUi })
    } else {
      toast.add({ title: 'Nothing to change', description: `${it.name} already has Conductor's hooks on ${props.host}.`, icon: 'i-lucide-check', color: 'neutral' })
    }
  } catch (e) {
    const byHand = e instanceof ApiError && e.code === 'no_file_route'
    if (e instanceof ApiError && snippetLeft(e)) chosen.value = it.id
    toast.add({
      title: byHand ? `${it.name}: finish by hand` : `Installing ${it.name} failed`,
      description: (e as Error).message,
      icon: 'i-lucide-triangle-alert',
      color: byHand ? 'warning' : 'error',
      ui: messageUi,
    })
  } finally {
    installing.value = ''
    emit('changed')
  }
}

const cols = 'grid-cols-[minmax(0,1fr)_auto] @min-[64rem]:grid-cols-[12rem_9rem_minmax(14rem,1fr)_minmax(0,16rem)_auto]'
</script>

<template>
  <div class="flex flex-col gap-3.5">
    <div class="@container overflow-hidden rounded-lg ring ring-default" data-integrations>
      <div class="hidden gap-4 border-b border-default bg-muted px-4 py-2.5 text-[11px] font-semibold uppercase tracking-wider text-muted @min-[64rem]:grid" :class="cols" aria-hidden="true">
        <span>Agent</span><span>Hooks</span><span>Reports</span><span>Config file</span><span />
      </div>
      <p v-if="!integrations.length" class="px-4 py-4 text-sm text-muted">No hook adapters listed.</p>
      <div
        v-for="it in integrations"
        :key="it.id"
        class="grid scroll-mt-4 items-center gap-x-4 gap-y-2 border-b border-default px-4 py-3 last:border-b-0"
        :class="[cols, openId === it.id && 'bg-warning/8']"
        data-integration
        :data-id="it.id"
        :id="`reporting-${it.id}`"
      >
        <span class="col-span-2 col-start-1 row-start-1 flex min-w-0 items-center gap-2 @min-[64rem]:col-span-1">
          <SessionAvatar :agent-id="it.id" :solid="wired(it)" />
          <span class="truncate text-sm font-medium text-highlighted">{{ it.name }}</span>
        </span>
        <span class="col-span-2 row-start-2 flex items-center gap-1.5 text-[12.5px] whitespace-nowrap @min-[64rem]:col-span-1 @min-[64rem]:col-start-2 @min-[64rem]:row-start-1" :class="status(it).cls" data-status>
          <span class="size-[7px] flex-none rounded-full" :class="status(it).dot" />{{ status(it).label }}
        </span>
        <ul class="col-span-2 row-start-3 flex flex-wrap gap-1 @min-[64rem]:col-span-1 @min-[64rem]:col-start-3 @min-[64rem]:row-start-1" :aria-label="`What ${it.name} reports`">
          <li v-for="ev in it.events" :key="ev" class="rounded bg-elevated px-1.5 py-0.5 font-mono text-[10.5px] font-medium">{{ ev }}</li>
        </ul>
        <span class="col-span-2 row-start-4 min-w-0 text-xs text-muted @min-[64rem]:col-span-1 @min-[64rem]:col-start-4 @min-[64rem]:row-start-1">
          <code v-if="it.where" class="block truncate font-mono text-[11.5px]" :title="it.where">{{ shortCwd(it.where) }}</code>
          <span v-else class="line-clamp-2" :title="noInstallText(it, homeKnown)" data-no-install>{{ noInstallText(it, homeKnown) }}</span>
        </span>
        <span class="col-span-2 row-start-5 flex flex-wrap items-center gap-1.5 @min-[64rem]:col-span-1 @min-[64rem]:col-start-5 @min-[64rem]:row-start-1 @min-[64rem]:justify-end">
          <UButton v-if="it.snippet" :label="openId === it.id ? 'Hide snippet' : 'Snippet'" size="xs" color="neutral" variant="link" :aria-expanded="openId === it.id" data-snippet-toggle @click="toggle(it.id)" />
          <UButton v-if="it.where" label="Install on this machine" icon="i-lucide-download" size="xs" :color="it.installed ? 'neutral' : 'primary'" :variant="it.installed ? 'outline' : 'solid'" :loading="installing === it.id" @click="install(it)" />
        </span>
      </div>
    </div>

    <section v-if="open?.snippet" class="overflow-hidden rounded-lg ring ring-warning/40" :data-snippet-for="open.id">
      <div class="flex flex-wrap items-center gap-x-2.5 gap-y-1 bg-warning/10 px-4 py-3">
        <UIcon name="i-lucide-wrench" class="size-4 flex-none text-warning" />
        <span class="text-[13.5px] font-semibold text-highlighted">{{ open.name }} · {{ status(open, true).label }}</span>
        <span v-if="open.where" class="truncate font-mono text-xs text-muted" :title="open.where">{{ shortCwd(open.where) }}</span>
        <UButton label="Hide" size="xs" color="neutral" variant="link" class="ml-auto" @click="chosen = null" />
      </div>
      <CodeBlock :text="open.snippet" :name="open.name" class="rounded-none" data-snippet />
    </section>
  </div>
</template>
