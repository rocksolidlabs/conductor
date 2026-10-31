<script setup lang="ts">
import { headingId, renderMarkdown } from '~/utils/markdown'
import { renderMermaidIsolated } from '~/utils/mermaidFrame'
/**
 * A Markdown file rendered (#166): the editor's Preview. The HTML is
 * renderMarkdown's (raw HTML never rendered, links and images under its
 * rules), set into the host and then enhanced: fenced code highlighted with
 * shiki in the editor's theme, and Mermaid blocks drawn by Mermaid in the
 * sandboxed frame (mermaidFrame.ts: an opaque origin under a Content
 * Security Policy that lets nothing be fetched; Mermaid there runs with
 * securityLevel strict, errors kept out of the document, and a DOMPurify
 * configuration forbidding every label element and attribute that loads or
 * navigates), made at the first diagram on the page. The SVG that comes
 * back is scrubbed of anything that would load, navigate or run, then
 * shown as an image (an img with a data URL): a browser loads nothing
 * from inside an SVG it shows as an image, runs nothing in it, and follows
 * no link, whatever its CSS says (a custom property holding an image-set,
 * say, which no scrub would know), so the display is as closed as the
 * frame. A block Mermaid cannot draw keeps its source with Mermaid's error
 * above it. The theme switch draws the diagrams again.
 * Each pass of enhancement is numbered; a pass overtaken by a later one,
 * or by the preview going, stops at its next step. A click on a relative
 * link asks the editor to open that file; a `#fragment` link scrolls to
 * its heading in the preview.
 */
const props = withDefaults(
  defineProps<{
    text: string
    /** The file's folder, which relative links and images resolve against. */
    dir: string
    rawUrl?: (path: string) => string | null
    /** Whether a relative link opens its file; off where the text has no folder (a Dispatch ticket's), its words kept as words. */
    relativeLinks?: boolean
    /**
     * A Dispatch ticket's images of its attachments (`![…](attachment:<id>)`): the object URL of an attachment's content, fetched with
     * the token (an `img` cannot carry a header), or null for one that is no image the page shows (an SVG, a PDF, one removed), whose
     * alt text stays. None: such an image is held as its alt text.
     */
    attachmentUrl?: (id: string) => Promise<string | null>
  }>(),
  { rawUrl: undefined, relativeLinks: true, attachmentUrl: undefined },
)
const emit = defineEmits<{ open: [path: string] }>()

const host = ref<HTMLElement | null>(null)
const colorMode = useColorMode()
const dark = computed(() => colorMode.value === 'dark')
const rendered = computed(() => renderMarkdown(props.text, { dir: props.dir, rawUrl: props.rawUrl, relativeLinks: props.relativeLinks, attachments: !!props.attachmentUrl }))

/** Code shiki highlights at most; past it the block stays plain. */
const HIGHLIGHT_LIMIT = 100_000
let pass = 0

async function enhance() {
  const el = host.value
  if (!el) return
  const n = ++pass
  el.innerHTML = rendered.value.html
  void showAttachments(el, n)
  await highlightCode(el, n)
  if (n !== pass) return
  if (rendered.value.mermaid > 0) await drawMermaid(el, n)
}

/** Each attachment's image given the object URL its content was fetched into; one that is no image the page shows keeps its alt text. */
async function showAttachments(el: HTMLElement, n: number) {
  const resolve = props.attachmentUrl
  if (!resolve) return
  await Promise.all(
    Array.from(el.querySelectorAll<HTMLImageElement>('img[data-md-attachment]')).map(async (img) => {
      const id = img.dataset.mdAttachment ?? ''
      const url = await resolve(id).catch(() => null)
      if (n !== pass || !img.isConnected) return
      if (url) {
        img.src = url
        return
      }
      const held = document.createElement('span')
      held.className = 'md-image-held'
      held.title = 'An attachment that is no image shown here'
      held.textContent = img.alt || `attachment:${id}`
      img.replaceWith(held)
    }),
  )
}

async function highlightCode(el: HTMLElement, n: number) {
  const blocks = Array.from(el.querySelectorAll<HTMLElement>('pre[data-code-lang]'))
  if (!blocks.length) return
  const shiki = await import('shiki')
  for (const pre of blocks) {
    if (n !== pass) return
    const lang = pre.dataset.codeLang ?? ''
    const source = pre.textContent ?? ''
    if (!lang || source.length > HIGHLIGHT_LIMIT) continue
    try {
      const html = await shiki.codeToHtml(source, {
        lang,
        theme: dark.value ? 'github-dark' : 'github-light',
        transformers: [
          {
            pre(node) {
              node.properties.style = ''
              node.properties.class = 'shiki md-code'
            },
          },
        ],
      })
      if (n !== pass) return
      pre.outerHTML = html
    } catch {
      // A language shiki does not have: the block stays as it is.
    }
  }
}

async function drawMermaid(el: HTMLElement, n: number) {
  const blocks = Array.from(el.querySelectorAll<HTMLElement>('pre[data-mermaid]'))
  for (const pre of blocks) {
    if (n !== pass) return
    const source = pre.textContent ?? ''
    try {
      const svg = await renderMermaidIsolated(source, dark.value ? 'dark' : 'default')
      if (n !== pass) return
      const figure = document.createElement('div')
      figure.className = 'md-diagram'
      figure.setAttribute('data-mermaid-rendered', '')
      figure.append(asImage(svg, source))
      pre.replaceWith(figure)
    } catch (e) {
      if (n !== pass) return
      pre.classList.add('md-mermaid-error')
      pre.setAttribute('data-mermaid-error', '')
      const note = document.createElement('div')
      note.className = 'md-error'
      note.textContent = `Mermaid could not draw this block: ${(e as Error).message || String(e)}`
      pre.prepend(note)
    }
  }
}

/**
 * The SVG Mermaid gave, as an image: parsed the way Mermaid serialised it (as HTML), scrubbed, serialised again as XML (an
 * image document is parsed strictly) and set as a data URL on an img sized by the diagram's viewBox, so it scales down with
 * the pane. The alt text is the diagram's first line.
 */
function asImage(svg: string, source: string): HTMLImageElement {
  const tpl = document.createElement('template')
  tpl.innerHTML = svg
  const root = tpl.content.querySelector('svg')
  if (!root) throw new Error('no diagram came back')
  scrub(root)
  const img = document.createElement('img')
  img.alt = `Diagram: ${source.trim().split('\n')[0] ?? ''}`
  img.decoding = 'async'
  const vb = (root.getAttribute('viewBox') ?? '').trim().split(/[\s,]+/).map(Number)
  if (vb.length === 4 && vb[2]! > 0 && vb[3]! > 0) {
    img.width = Math.ceil(vb[2]!)
    img.height = Math.ceil(vb[3]!)
  }
  img.src = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(new XMLSerializer().serializeToString(root))}`
  return img
}

const LOADING_TAGS = new Set(['image', 'img', 'picture', 'source', 'video', 'audio', 'iframe', 'object', 'embed', 'link', 'use', 'script', 'form', 'meta', 'base'])
const URL_ATTRS = ['src', 'srcset', 'href', 'xlink:href', 'poster', 'background', 'action', 'formaction', 'data', 'ping']
/** A url( in CSS that points anywhere but into the document (`url(#marker)` is a diagram's own arrowhead or gradient). */
const OUTSIDE_URL = /url\s*\(\s*(?!["']?\s*#)[^)]*\)/gi
/**
 * Takes out of Mermaid's SVG anything that would load a resource, navigate or run: the elements that do, a URL attribute
 * pointing anywhere but into the document, every on* handler, a url( or @import in a style. The diagram's own style stays.
 * The image display (asImage) is what closes the door; this keeps the SVG honest on its way there.
 */
function scrub(root: Element) {
  for (const el of [root, ...Array.from(root.querySelectorAll('*'))]) {
    if (LOADING_TAGS.has(el.localName)) {
      el.remove()
      continue
    }
    if (el.localName === 'a') {
      // A `click A href "…"` wraps the node in an anchor: the node stays (as a group, its transform and class kept), the link goes.
      const g = document.createElementNS('http://www.w3.org/2000/svg', 'g')
      for (const name of el.getAttributeNames()) {
        if (!URL_ATTRS.includes(name) && name !== 'target' && !/^on/i.test(name)) g.setAttribute(name, el.getAttribute(name) ?? '')
      }
      g.append(...Array.from(el.childNodes))
      el.replaceWith(g)
      continue
    }
    for (const a of URL_ATTRS) {
      const v = el.getAttribute(a)
      if (v !== null && !v.startsWith('#')) el.removeAttribute(a)
    }
    for (const name of el.getAttributeNames()) {
      if (/^on/i.test(name)) el.removeAttribute(name)
    }
    const style = el.getAttribute('style')
    if (style && (OUTSIDE_URL.test(style) || /@import/i.test(style))) el.setAttribute('style', style.replace(OUTSIDE_URL, 'none').replace(/@import[^;]*;?/gi, ''))
    OUTSIDE_URL.lastIndex = 0
    if (el.localName === 'style' && (OUTSIDE_URL.test(el.textContent ?? '') || /@import/i.test(el.textContent ?? ''))) el.textContent = (el.textContent ?? '').replace(OUTSIDE_URL, 'none').replace(/@import[^;]*;?/gi, '')
    OUTSIDE_URL.lastIndex = 0
  }
}

function onClick(e: MouseEvent) {
  const a = (e.target as HTMLElement | null)?.closest?.('a') as HTMLAnchorElement | null
  if (!a || !host.value?.contains(a)) return
  const path = a.getAttribute('data-md-link')
  if (path) {
    e.preventDefault()
    emit('open', path)
    return
  }
  const href = a.getAttribute('href') ?? ''
  if (href.startsWith('#')) {
    // A fragment: its heading in the preview, by the id renderMarkdown gave it; the page's URL does not change.
    e.preventDefault()
    const id = href.slice(1)
    const target = id ? host.value.querySelector(`#${CSS.escape(headingId(decodeURIComponent(id)))}`) : null
    target?.scrollIntoView({ block: 'start' })
    return
  }
  // Anything else that is not a link to the web (which opens in a new tab): nothing, never a navigation of the page.
  if (a.target !== '_blank') e.preventDefault()
}

onMounted(() => void enhance())
onBeforeUnmount(() => {
  pass++
})
// A resolver given anew (a ticket's attachments read again) shows its images again: one attached since may be named already.
watch([rendered, dark, () => props.attachmentUrl], () => void nextTick(enhance))
defineExpose({ focus: () => host.value?.focus() })
</script>

<template>
  <div ref="host" class="md-preview h-full overflow-auto px-5 py-4 text-sm leading-relaxed text-default outline-none" data-md-preview tabindex="-1" @click="onClick" />
</template>

<style>
.md-preview h1,
.md-preview h2,
.md-preview h3,
.md-preview h4 {
  font-weight: 600;
  color: var(--ui-text-highlighted);
  margin: 1.25em 0 0.5em;
  line-height: 1.25;
}
.md-preview h1 {
  font-size: 1.5rem;
  margin-top: 0;
}
.md-preview h2 {
  font-size: 1.25rem;
  border-bottom: 1px solid var(--ui-border);
  padding-bottom: 0.25em;
}
.md-preview h3 {
  font-size: 1.05rem;
}
.md-preview p,
.md-preview ul,
.md-preview ol,
.md-preview blockquote,
.md-preview table,
.md-preview pre,
.md-preview .md-diagram {
  margin: 0.75em 0;
}
.md-preview ul {
  list-style: disc;
  padding-left: 1.5em;
}
.md-preview ol {
  list-style: decimal;
  padding-left: 1.5em;
}
.md-preview li.task {
  list-style: none;
  margin-left: -1.25em;
}
.md-preview li.task input {
  margin-right: 0.4em;
  vertical-align: -0.1em;
}
.md-preview a {
  color: var(--ui-primary);
  text-decoration: underline;
  text-underline-offset: 2px;
}
.md-preview code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.9em;
  background: var(--ui-bg-elevated);
  border-radius: 0.25rem;
  padding: 0.1em 0.3em;
}
.md-preview pre {
  overflow-x: auto;
  background: var(--ui-bg-elevated);
  border: 1px solid var(--ui-border);
  border-radius: 0.375rem;
  padding: 0.75em 1em;
}
.md-preview pre code {
  background: none;
  padding: 0;
  font-size: 0.85em;
}
.md-preview blockquote {
  border-left: 3px solid var(--ui-border-accented);
  padding-left: 1em;
  color: var(--ui-text-muted);
}
.md-preview table {
  border-collapse: collapse;
  display: block;
  overflow-x: auto;
}
.md-preview th,
.md-preview td {
  border: 1px solid var(--ui-border);
  padding: 0.3em 0.75em;
  text-align: left;
}
.md-preview th {
  background: var(--ui-bg-elevated);
  font-weight: 600;
}
.md-preview img {
  max-width: 100%;
}
.md-preview .md-image-held {
  color: var(--ui-text-muted);
  font-style: italic;
}
.md-preview .md-diagram img {
  display: block;
  max-width: 100%;
  height: auto;
}
.md-preview .md-mermaid-error {
  border-color: var(--ui-error);
}
.md-preview .md-error {
  color: var(--ui-error);
  font-family: inherit;
  margin-bottom: 0.5em;
  white-space: pre-wrap;
}
</style>
