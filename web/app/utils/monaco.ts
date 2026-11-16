/**
 * Monaco, the VS Code editor core (design 4b), loaded only when the first
 * file opens: this module is imported dynamically, and with it the editor,
 * its workers (Vite builds each as its own file; the package's export map puts
 * `esm/vs` behind `monaco-editor/<path>`) and the two themes drawn
 * from the brand palette. `languageFor` names Monaco's language for a path.
 */
import * as monaco from 'monaco-editor'
import EditorWorker from 'monaco-editor/editor/editor.worker.js?worker'
import JsonWorker from 'monaco-editor/language/json/json.worker.js?worker'
import CssWorker from 'monaco-editor/language/css/css.worker.js?worker'
import HtmlWorker from 'monaco-editor/language/html/html.worker.js?worker'
import TsWorker from 'monaco-editor/language/typescript/ts.worker.js?worker'
import { MONACO_COLORS } from './monacoColors'

export type Monaco = typeof monaco

const byExt: Record<string, string> = {
  go: 'go', ts: 'typescript', mts: 'typescript', cts: 'typescript', tsx: 'typescript', js: 'javascript', mjs: 'javascript', cjs: 'javascript', jsx: 'javascript',
  vue: 'html', svelte: 'html', html: 'html', htm: 'html', xml: 'xml', svg: 'xml', css: 'css', scss: 'scss', less: 'less',
  py: 'python', rs: 'rust', java: 'java', kt: 'kotlin', rb: 'ruby', php: 'php', c: 'c', h: 'c', cc: 'cpp', cpp: 'cpp', hpp: 'cpp', cs: 'csharp', swift: 'swift',
  json: 'json', jsonc: 'json', yaml: 'yaml', yml: 'yaml', toml: 'ini', ini: 'ini', cfg: 'ini', conf: 'ini', env: 'ini', md: 'markdown', mdx: 'markdown',
  sh: 'shell', bash: 'shell', zsh: 'shell', sql: 'sql', graphql: 'graphql', gql: 'graphql', tf: 'hcl', hcl: 'hcl', lua: 'lua', dart: 'dart', r: 'r', scala: 'scala', ps1: 'powershell',
  dockerfile: 'dockerfile', mod: 'go', sum: 'plaintext', txt: 'plaintext', log: 'plaintext',
}
const byName: Record<string, string> = { dockerfile: 'dockerfile', makefile: 'plaintext', 'go.mod': 'go', 'go.sum': 'plaintext' }

/** Monaco's language id for a path; `plaintext` when it has none. */
export function languageFor(path: string): string {
  const name = (path.split('/').pop() || '').toLowerCase()
  if (byName[name]) return byName[name]!
  const ext = name.includes('.') ? name.split('.').pop()! : ''
  return byExt[ext] ?? 'plaintext'
}

let ready = false

/** Monaco with its workers wired and the brand themes defined; once. */
export function loadMonaco(): Monaco {
  if (ready) return monaco
  ready = true
  self.MonacoEnvironment = {
    getWorker(_: unknown, label: string) {
      switch (label) {
        case 'json':
          return new JsonWorker()
        case 'css':
        case 'scss':
        case 'less':
          return new CssWorker()
        case 'html':
        case 'handlebars':
        case 'razor':
          return new HtmlWorker()
        case 'typescript':
        case 'javascript':
          return new TsWorker()
        default:
          return new EditorWorker()
      }
    },
  }
  // The brand palette (docs/design/brand.md): forest, terracotta and harbor for the tokens, the app's ink scale for the surfaces
  // (monacoColors.ts).
  monaco.editor.defineTheme('conductor-dark', {
    base: 'vs-dark',
    inherit: true,
    rules: [
      { token: 'comment', foreground: '7b887c', fontStyle: 'italic' },
      { token: 'keyword', foreground: 'df8259' },
      { token: 'string', foreground: '9bb3a3' },
      { token: 'number', foreground: 'dba63e' },
      { token: 'type', foreground: '75a4ca' },
      { token: 'type.identifier', foreground: '75a4ca' },
    ],
    colors: { ...MONACO_COLORS.dark },
  })
  monaco.editor.defineTheme('conductor-light', {
    base: 'vs',
    inherit: true,
    rules: [
      { token: 'comment', foreground: '6e7781', fontStyle: 'italic' },
      { token: 'keyword', foreground: 'a44727' },
      { token: 'string', foreground: '263d35' },
      { token: 'number', foreground: '0550ae' },
      { token: 'type', foreground: '245d85' },
      { token: 'type.identifier', foreground: '245d85' },
    ],
    colors: { ...MONACO_COLORS.light },
  })
  return monaco
}
