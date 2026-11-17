<script setup lang="ts">
import { MEASURED_BY, depsWords, notMeasured, notMeasuredLine, notObservedWords, fileLines, fileStatusWord, independenceWords, diffSizeWords, verifyFilesWords, type ComparedCandidate } from '~/utils/compare'

/**
 * What a comparison measured of each candidate against the base (Program item CP1, docs/plans/compare-crew.md, PR-4), one row each,
 * every row measured by Conductor from git and the ledger alone (I6): the size (files changed, lines added and removed, files added
 * and deleted, with the list of files), the dependency changes (the files, and for go.mod and package.json each module added, removed
 * or moved), the test and build files, and independence (shared commits and same patches), with what independence cannot see said
 * beside it (`notObserved`, the server's words when it gives them).
 */
const props = defineProps<{ candidates: ComparedCandidate[]; notObserved?: string }>()

/** By task: why each row says nothing of it ('' where the row holds what was measured), the server's words. */
const unread = computed(() => new Map(props.candidates.map((c) => [c.task, notMeasured(c)])))
const why = (c: ComparedCandidate, row: 'size' | 'deps' | 'verify' | 'independence') => unread.value.get(c.task)?.[row] ?? ''
/** The other lanes a candidate's independence was not compared with: those git could not be read for, or with no commit. */
const notComparedWith = (c: ComparedCandidate) => props.candidates.filter((x) => x.task !== c.task && why(x, 'independence')).map((x) => x.task)
const cols = computed(() => (props.candidates.length >= 4 ? 'sm:grid-cols-2 xl:grid-cols-4' : props.candidates.length === 3 ? 'sm:grid-cols-2 lg:grid-cols-3' : 'sm:grid-cols-2'))
const rows = [
  { key: 'size', title: 'Size', words: 'against the base' },
  { key: 'deps', title: 'Dependency changes', words: 'the dependency files changed, and the modules of go.mod and package.json' },
  { key: 'verify', title: 'Test and build files', words: 'files that can change what a check runs' },
  { key: 'independence', title: 'Independence', words: 'shared commits and same patches' },
] as const
</script>

<template>
  <section class="flex flex-col gap-3" data-compare-rows>
    <section v-for="row in rows" :key="row.key" class="flex flex-col gap-2" :data-compare-row="row.key">
      <div class="flex flex-wrap items-baseline gap-x-2">
        <h3 class="text-sm font-semibold text-highlighted">{{ row.title }}</h3>
        <span class="text-xs text-muted">{{ row.words }} · {{ MEASURED_BY }}</span>
      </div>
      <div class="grid grid-cols-1 gap-2" :class="cols">
        <template v-for="c in candidates" :key="c.task">
          <!-- Size: the counts, then the files. -->
          <div v-if="row.key === 'size'" class="flex min-w-0 flex-col gap-1 rounded-md bg-elevated/40 px-2.5 py-1.5 text-xs" :data-compare-size="c.task">
            <span class="font-mono text-muted" data-mono="id">{{ c.task }}</span>
            <span v-if="why(c, 'size')" class="text-muted" data-compare-not-measured>{{ notMeasuredLine(why(c, 'size')) }}</span>
            <template v-else-if="diffSizeWords(c.size)">
              <span class="text-default" data-compare-size-lines>{{ diffSizeWords(c.size)!.lines }}</span>
              <span v-if="diffSizeWords(c.size)!.files" class="text-muted" data-compare-size-files>{{ diffSizeWords(c.size)!.files }}</span>
              <UCollapsible v-if="c.size?.list?.length" :ui="{ content: 'pt-1' }">
                <UButton :label="`The files (${c.size.list.length})`" icon="i-lucide-chevron-right" size="xs" color="neutral" variant="ghost" class="-ml-1.5 w-fit" data-compare-size-toggle />
                <template #content>
                  <ul class="flex flex-col gap-0.5" data-compare-size-list>
                    <li v-for="f in c.size.list" :key="f.path" class="flex min-w-0 items-baseline gap-2" :data-compare-file="f.path">
                      <span class="min-w-0 flex-1 break-all font-mono" data-mono="path">{{ f.path }}</span>
                      <span class="flex-none text-muted">{{ fileStatusWord(f.status) }}</span>
                      <span class="flex-none font-mono text-muted" data-mono="measure">{{ fileLines(f) }}</span>
                    </li>
                  </ul>
                  <p v-if="diffSizeWords(c.size)!.more" class="pt-0.5 text-muted">{{ diffSizeWords(c.size)!.more }}</p>
                </template>
              </UCollapsible>
            </template>
          </div>
          <!-- Dependency changes. -->
          <div v-else-if="row.key === 'deps'" class="flex min-w-0 flex-col gap-1 rounded-md bg-elevated/40 px-2.5 py-1.5 text-xs" :data-compare-deps="c.task">
            <span class="font-mono text-muted" data-mono="id">{{ c.task }}</span>
            <span v-if="why(c, 'deps')" class="text-muted" data-compare-deps-summary data-compare-not-measured>{{ notMeasuredLine(why(c, 'deps')) }}</span>
            <span v-else class="text-default" data-compare-deps-summary>{{ depsWords(c.deps).summary }}</span>
            <ul v-if="depsWords(c.deps).files.length" class="flex flex-col gap-0.5">
              <li v-for="f in depsWords(c.deps).files" :key="f" class="break-all font-mono" data-mono="path" data-compare-deps-file>{{ f }}</li>
            </ul>
            <ul v-if="depsWords(c.deps).modules.length" class="flex flex-col gap-0.5">
              <li v-for="(m, i) in depsWords(c.deps).modules" :key="i" class="break-words" data-compare-deps-module>{{ m.words }} <span class="text-muted">in {{ m.file }}</span></li>
            </ul>
            <span v-if="depsWords(c.deps).more" class="text-muted">{{ depsWords(c.deps).more }}</span>
          </div>
          <!-- Test and build files. -->
          <div v-else-if="row.key === 'verify'" class="flex min-w-0 flex-col gap-1 rounded-md bg-elevated/40 px-2.5 py-1.5 text-xs" :data-compare-verify="c.task">
            <span class="font-mono text-muted" data-mono="id">{{ c.task }}</span>
            <span v-if="why(c, 'verify')" class="text-muted" data-compare-not-measured>{{ notMeasuredLine(why(c, 'verify')) }}</span>
            <span v-else class="text-default">{{ verifyFilesWords(c.task, c.verifyFiles).summary }}</span>
            <ul v-if="verifyFilesWords(c.task, c.verifyFiles).files.length" class="flex flex-col gap-0.5">
              <li v-for="f in verifyFilesWords(c.task, c.verifyFiles).files" :key="f" class="break-all">{{ f }}</li>
            </ul>
            <span v-if="verifyFilesWords(c.task, c.verifyFiles).more" class="text-muted">{{ verifyFilesWords(c.task, c.verifyFiles).more }}</span>
          </div>
          <!-- Independence. -->
          <div v-else class="flex min-w-0 flex-col gap-1 rounded-md bg-elevated/40 px-2.5 py-1.5 text-xs" :data-compare-independence="c.task">
            <span class="font-mono text-muted" data-mono="id">{{ c.task }}</span>
            <span v-if="why(c, 'independence')" class="text-muted" data-compare-not-measured>{{ notMeasuredLine(why(c, 'independence')) }}</span>
            <template v-else>
              <span v-for="(w, i) in independenceWords(c)" :key="i" class="break-words text-default" data-compare-independence-words>{{ w }}</span>
              <span v-if="notComparedWith(c).length" class="break-words text-muted" data-compare-not-compared>not compared with {{ notComparedWith(c).join(', ') }}: not measured</span>
            </template>
          </div>
        </template>
      </div>
      <p v-if="row.key === 'independence'" class="text-xs text-muted" data-compare-not-observed>{{ notObservedWords(notObserved) }}</p>
    </section>
  </section>
</template>
