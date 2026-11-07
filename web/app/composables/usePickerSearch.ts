import { watch, type Ref } from 'vue'

/**
 * What a name picker says to the owner of the names it offers (PersonPicker given `names`, CrewPeopleTable's one usePeople for every row):
 * `open` as it opens, and `search` with its search text, only while it is open. The pickers of a table share one list of names, the reply
 * to the newest read any of them asked for, so a closed picker says nothing: USelectMenu clears its search 100 ms after it closes, and
 * that clearing said as a search would read the first page over the results another picker, opened meanwhile, is searching. A picker
 * opened again before that clearing keeps its text, and says it again after `open`, so its list is the text's and not the first page.
 */
export function usePickerSearch(open: Readonly<Ref<boolean>>, term: Readonly<Ref<string>>, say: { open: () => void; search: (q: string) => void }) {
  watch(open, (o) => {
    if (!o) return
    say.open()
    if (term.value) say.search(term.value)
  })
  watch(term, (q) => {
    if (open.value) say.search(q)
  })
}
