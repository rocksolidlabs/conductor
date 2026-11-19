<script setup lang="ts">
import { itemId, readRefusal, SHELL_BOUNDS } from '~/utils/shell'

/**
 * A notification tapped (docs/plans/phone-shell.md, OSS-2b, "The routes into the page"): `/shell/open?h=<handle>` asks the provider
 * which item the handle names and opens it, read at that moment. A tap with no handle, or one of more than 256 bytes, opens the
 * inbox; a handle no longer current, or a provider that cannot be reached, opens the inbox with its words.
 */
definePageMeta({ layout: 'shell' })
useHead({ title: 'Opening' })
const route = useRoute()
const shell = useShell()

onMounted(async () => {
  const h = route.query.h
  const p = shell.provider
  if (!p || typeof h !== 'string' || !h || new TextEncoder().encode(h).length > SHELL_BOUNDS.handle) return void navigateTo('/shell', { replace: true })
  let note = 'unreachable'
  try {
    const r = await p.inbox.resolve(h)
    const id = r && 'id' in r ? itemId(r.id) : null
    if (id) return void navigateTo(`/shell/decisions/${encodeURIComponent(id)}`, { replace: true })
    if (readRefusal(r).code === 'unknown_handle') note = 'unknown_handle'
  } catch {
    note = 'unreachable'
  }
  await navigateTo({ path: '/shell', query: { note } }, { replace: true })
})
</script>

<template>
  <ShellHeader :title="shell.name" />
  <p class="flex items-center gap-2 text-sm text-muted" role="status" data-shell-opening><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Opening the notification…</p>
</template>
