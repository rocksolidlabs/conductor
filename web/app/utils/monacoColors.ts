/**
 * The Files editor's two themes as plain data, drawn from the ink scale of `main.css` (the app's neutral): the editor's ground is the
 * page's `bg` in each theme, its text the page's text, the line numbers at the dimmed step and the current one at the toned step, the
 * current line one step off the ground. `monaco.ts` defines the themes with these tables; `themeContrast.test.ts` holds them to the
 * page's tokens and to their floors, so a change of the scale that the editor does not follow fails there.
 */

/** The ink scale's steps the editor draws with (main.css, `--color-ink-*`). */
const INK = {
  50: '#f6f9f3',
  200: '#dcdfd7',
  300: '#bfc9be',
  400: '#8f948d',
  500: '#666e67',
  600: '#4d554e',
  700: '#373f39',
  800: '#242c27',
  900: '#18211c',
} as const

/** Monaco's colour ids for `conductor-dark` and `conductor-light`. */
export const MONACO_COLORS = {
  dark: {
    'editor.background': INK[900],
    'editor.foreground': INK[200],
    'editorLineNumber.foreground': INK[500],
    'editorLineNumber.activeForeground': INK[300],
    'editor.lineHighlightBackground': INK[800],
    'editor.selectionBackground': '#35514a',
    'editorCursor.foreground': '#d26b3f',
    'editorIndentGuide.background1': INK[800],
    'editorGutter.background': INK[900],
    'minimap.background': INK[900],
    'scrollbarSlider.background': `${INK[700]}80`,
    'editorWidget.background': INK[800],
    'editorWidget.border': INK[700],
    'input.background': INK[800],
    'focusBorder': '#9bb3a3',
  },
  light: {
    'editor.background': '#ffffff',
    'editor.foreground': INK[900],
    'editorLineNumber.foreground': INK[400],
    'editorLineNumber.activeForeground': INK[600],
    'editor.lineHighlightBackground': INK[50],
    'editor.selectionBackground': '#c3d2c7',
    'editorCursor.foreground': '#d26b3f',
    'editorIndentGuide.background1': INK[200],
    'minimap.background': '#ffffff',
    'editorWidget.background': INK[50],
    'editorWidget.border': INK[200],
    'focusBorder': '#263d35',
  },
} as const satisfies Record<'dark' | 'light', Record<string, string>>
