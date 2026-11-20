<script setup lang="ts">
import { decisionPath, itemId, readRefusal, SHELL_BOUNDS, withinWait } from '~/utils/shell'

/**
 * A notification tapped (docs/plans/phone-shell.md, OSS-2b, "The routes into the page"): `/shell/open?h=<handle>` asks the provider
 * which item the handle names and opens it, read at that moment. A tap with no handle, or one of more than 256 bytes, opens the
 * inbox; a handle no longer current, or a provider that cannot be reached or does not answer within its wait, opens the inbox with
 * its words. Back and Open a server are there while it waits; once the person has left, or tapped another notification (the same
 * page, its handle changed), the first answer moves no one and the new handle is opened.
 */
definePageMeta({ layout: 'shell' })
useHead({ title: 'Opening' })
const route = useRoute()
const shell = useShell()
let alive = true
onBeforeUnmount(() => (alive = false))
/** The handle the page's address names now: a second notification tapped while one opens changes it on the same page. */
const handle = () => (typeof route.query.h === 'string' ? route.query.h : '')

/** Opens h's item, judging after each wait that the page is still here and still opening h: a handle passed over moves no one. */
async function openHandle(h: string) {
  const p = shell.provider
  const still = () => alive && handle() === h
  if (!p || !h || new TextEncoder().encode(h).length > SHELL_BOUNDS.handle) return void navigateTo('/shell', { replace: true })
  let note = 'unreachable'
  try {
    const r = await withinWait(p.inbox.resolve(h), `${shell.name} did not answer`)
    if (!still()) return
    const id = r && 'id' in r ? itemId(r.id) : null
    if (id) return void navigateTo(decisionPath(id), { replace: true })
    if (readRefusal(r).code === 'unknown_handle') note = 'unknown_handle'
  } catch {
    note = 'unreachable'
  }
  if (!still()) return
  await navigateTo({ path: '/shell', query: { note } }, { replace: true })
}

onMounted(() => void openHandle(handle()))
watch(
  () => route.query.h,
  () => {
    if (alive) void openHandle(handle())
  },
)
</script>

<template>
  <ShellHeader :title="shell.name" back />
  <p class="flex items-center gap-2 text-sm text-muted" role="status" data-shell-opening><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Opening the notification…</p>
  <ULink to="/shell/server" class="inline-flex min-h-11 items-center gap-2 self-start text-sm text-primary" data-shell-to-server><UIcon name="i-lucide-server" class="size-4" /> Open a server</ULink>
</template>
