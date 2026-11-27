import { effectScope, reactive, watch } from 'vue'
import { OwnLines, RowDrafts } from '~/utils/rowDrafts'

/**
 * The chat boxes' lines, by session (UX3 R4), kept by the app as the sidebar keeps its rows' lines (RowDrafts): a line typed for one
 * agent is there again when the person comes back to it after answering another, with the question it was begun for. It leaves when
 * it is sent, when the person empties the box, or when its session is no longer listed (`listed`, once the list is read); nothing
 * outlives the page, so a reload drops every line. Its watch lives in the effect scope it is made in.
 */
export function boxDrafts(listed: () => readonly string[] | undefined): RowDrafts {
  const drafts = reactive(new RowDrafts()) as RowDrafts
  watch(listed, (ids) => {
    if (ids) drafts.keep(ids)
  })
  return drafts
}

/**
 * Which kept lines the person typed or pasted themselves (UX3 R10, OwnLines), by session: dropped with the lines, when the session is
 * no longer listed. Its watch lives in the effect scope it is made in.
 */
export function ownLines(listed: () => readonly string[] | undefined): OwnLines {
  const own = new OwnLines()
  watch(listed, (ids) => {
    if (ids) own.keep(ids)
  })
  return own
}

let shared: RowDrafts | undefined
let sharedOwn: OwnLines | undefined

/**
 * The app's one store of chat box lines, by session: its sessions as the live store lists them. Made in a scope of the app's own,
 * never a component's, so that the line and the list it is kept by outlive the box that drew it.
 */
export function useBoxDrafts(): RowDrafts {
  if (!shared) {
    shared = effectScope(true).run(() => {
      const attention = useAttention()
      const listed = () => (attention.loaded.value ? attention.sessions.value.map((s) => s.id) : undefined)
      sharedOwn = ownLines(listed)
      return boxDrafts(listed)
    })!
  }
  return shared
}

/** The app's one record of which kept lines are the person's own typing, beside useBoxDrafts' lines. */
export function useOwnLines(): OwnLines {
  useBoxDrafts()
  return sharedOwn!
}
