export default defineAppConfig({
  ui: {
    // Below lg the bottom bar sits under the layout group's padding; a panel at least a screen tall (the theme's
    // min-h-svh) would run under it and clip what sits at its foot (a phone's terminal bar, the reply bar).
    dashboardPanel: { slots: { root: 'max-lg:min-h-0' } },
    // The sidebar (the rail, the full sidebar and the phone's slideover) is drawn on the canvas, a step off the page (main.css).
    dashboardSidebar: { slots: { root: 'bg-canvas', content: 'bg-canvas' } },
    // A solid amber fill carries ink text in both themes: white on signal-500 is 2.94:1 in light.
    button: { compoundVariants: [{ color: 'warning', variant: 'solid', class: 'text-(--ui-color-neutral-900)' }] },
    badge: { compoundVariants: [{ color: 'warning', variant: 'solid', class: 'text-(--ui-color-neutral-900)' }] },
    // Brand palette from docs/design/brand.md: forest for primary actions,
    // terracotta only as the restrained accent (never a status light).
    // Operational status uses its own scales: amber needs-input, green running,
    // and the brand's info and error tokens (web/public/brand/tokens.css).
    // The neutral is ink: the brand's sage at 100 and its ink at 900 (main.css), so every grey of every screen is the brand's.
    colors: {
      primary: 'forest',
      secondary: 'terracotta',
      neutral: 'ink',
      warning: 'signal',
      success: 'active',
      info: 'harbor',
      error: 'brick',
    },
  },
})
