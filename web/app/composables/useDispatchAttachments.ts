import type { TicketAttachment } from './useDispatch'
import { AttachmentImages, inlineImage } from '~/utils/dispatch'

/**
 * A ticket page's attachments as the browser shows them (docs/plans/dispatch.md, Owner decisions, 8): every content fetched with the
 * workbench token and shown through an object URL, since an `img` or a link the browser follows would carry no header; never an `img`
 * whose source is the API. An attachment's content never changes (it is named by its hash, and an attachment is added and removed,
 * never edited), so each is fetched once by its id while the page is open, and every object URL is revoked when it goes; an image whose
 * fetch answers after that makes none (AttachmentImages).
 */
export function useDispatchAttachments(attachments: () => readonly TicketAttachment[]) {
  const api = useDispatchApi()
  const images = new AttachmentImages((id) => api.attachment(id))

  /**
   * The object URL of an attachment's content as an image, or null when the server serves it as no image the page shows (an SVG comes
   * as application/octet-stream, and is never one). A fetch that failed is forgotten, to be asked again by the next render.
   */
  function image(id: string): Promise<string | null> {
    return images.image(id)
  }

  /** The live image attachments by id, as one string, so the resolver below is made again only when they change. */
  const imageIds = computed(() =>
    attachments()
      .filter((a) => !a.deletedAt && inlineImage(a.contentType))
      .map((a) => a.id)
      .join(' '),
  )

  /**
   * What a Markdown preview of the ticket's text resolves `attachment:<id>` images by (MarkdownPreview's attachmentUrl): an image of
   * the ticket's live attachments, else null, so a name of another ticket's or of a removed one shows its alt text. Made again when the
   * ticket's images change, which draws the previews again.
   */
  const resolver = computed(() => {
    const ids = new Set(imageIds.value.split(' ').filter(Boolean))
    return (id: string): Promise<string | null> => (ids.has(id) ? image(id) : Promise.resolve(null))
  })

  /** An attachment saved by the browser under its name: its content fetched with the token, handed over as an object URL. */
  async function download(a: Pick<TicketAttachment, 'id' | 'name'>): Promise<void> {
    const blob = await api.attachment(a.id)
    const url = URL.createObjectURL(blob)
    try {
      const link = document.createElement('a')
      link.href = url
      link.download = a.name
      link.rel = 'noopener'
      document.body.append(link)
      link.click()
      link.remove()
    } finally {
      // The download has its own copy once it has begun.
      setTimeout(() => URL.revokeObjectURL(url), 60_000)
    }
  }

  onBeforeUnmount(() => images.dispose())

  return { image, resolver, download }
}
