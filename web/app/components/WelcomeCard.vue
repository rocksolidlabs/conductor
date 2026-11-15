<script setup lang="ts">
// The first-run welcome (#359): what the home and the Yard show when the server has no session at all, which is not the
// sidebar's own empty line, so that a wide window never shows the same picture twice.
const api = useSessions()
const toast = useToast()
const router = useRouter()
const launch = useLaunchModal()

// The example crews: loaded once, then the Crews page shows them.
const loadingExamples = ref(false)
async function loadExamples() {
  loadingExamples.value = true
  try {
    const r = await api.loadExampleCrews()
    toast.add({ title: `${r.added.length} added${r.skipped.length ? `, ${r.skipped.length} already there` : ''}`, icon: 'i-lucide-package-open', color: 'success' })
    await router.push('/crews')
  } catch (e) {
    toast.add({ title: 'Load the examples', description: (e as Error).message, color: 'error' })
  } finally {
    loadingExamples.value = false
  }
}
</script>

<template>
  <div class="flex-1 flex flex-col items-center justify-center p-8" data-welcome>
    <div class="w-full max-w-lg rounded-lg border border-default bg-elevated/50 p-6 flex flex-col gap-4">
      <div class="flex items-center gap-3">
        <UIcon name="i-lucide-train-front" class="size-8 text-primary" />
        <div>
          <h2 class="text-base font-semibold text-highlighted">Welcome to Conductor</h2>
          <p class="text-sm text-muted">Nothing is running yet. Start with one of these.</p>
        </div>
      </div>
      <!-- The three choices one to a row, at the card's width: the card (32 rem) has no row wide enough for three equal choices
           with "Load the example crews" whole, and two in a row with the third stretched under them reads as unbalanced (#374, #906). -->
      <div class="flex flex-col gap-2" data-welcome-choices>
        <UButton label="Launch agent" icon="i-lucide-play" block @click="launch.show()" />
        <UButton label="Load the example crews" icon="i-lucide-package-open" color="neutral" variant="outline" block :loading="loadingExamples" data-welcome-examples @click="loadExamples" />
        <UButton label="Settings" icon="i-lucide-settings" color="neutral" variant="outline" block to="/settings" />
      </div>
      <p class="text-xs text-muted">Or run <code>conductor host</code> on another machine to bring its agents here. The <a class="underline" href="https://github.com/rocksolidlabs/conductor#readme" target="_blank" rel="noopener">README</a> has the rest.</p>
    </div>
  </div>
</template>
