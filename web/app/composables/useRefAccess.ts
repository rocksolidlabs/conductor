import { hasInjectionContext, inject } from 'vue'
import type { RefResolution } from '~/utils/brakeman'
import { defaultRefAccess, REF_ACCESS, refReadError, refReadShown, type RefAccess } from '~/utils/refAccess'
import type { TicketComment } from './useDispatch'

/**
 * The access an evidence chip and its record read through (utils/refAccess.ts): the one a page provides (REF_ACCESS: the join page's
 * Decide gives the link's), else today's, through useApi. useApi is asked only when nothing is provided, so a link's page never makes
 * a workbench request through a chip.
 */
export function useRefAccess(): RefAccess {
  const given = hasInjectionContext() ? inject(REF_ACCESS, null) : null
  return given ?? defaultRefAccess(useApi())
}

/**
 * A chip's record read as it is opened (BrakemanReceipt.vue): through the access (`useRefAccess`'s), each opening counted, so a reply
 * is shown only in the opening it was asked for, and one that comes after a later opening is dropped. A comment the reply does not hold
 * is said gone; a text that is part of the record its SHA-256 is of (a link's cut log, a dropped log's kept end) says what it is
 * (`shown`, refReadShown).
 */
export function useRefReceipt(o: { ref: () => string; resolution: () => RefResolution | undefined }, access: RefAccess = useRefAccess()) {
  const open = ref(false)
  const loading = ref(false)
  const error = ref('')
  const text = ref('')
  const sha256 = ref('')
  const shown = ref('')
  const comment = ref<TicketComment | null>(null)
  let opened = 0

  async function read() {
    const mine = ++opened
    open.value = true
    loading.value = true
    error.value = ''
    text.value = ''
    sha256.value = ''
    shown.value = ''
    comment.value = null
    const reader = access.read(o.ref(), o.resolution())
    try {
      if (!reader) {
        error.value = 'This record is not read here.'
        return
      }
      const r = await reader()
      if (mine !== opened) return
      if (r.kind === 'comment') {
        comment.value = r.comment
        if (!r.comment) error.value = 'The comment is not there any more.'
      } else {
        text.value = r.text
        sha256.value = r.sha256 ?? ''
        shown.value = refReadShown(r)
      }
    } catch (e) {
      if (mine === opened) error.value = refReadError(e)
    } finally {
      if (mine === opened) loading.value = false
    }
  }
  return { open, loading, error, text, sha256, shown, comment, read }
}
