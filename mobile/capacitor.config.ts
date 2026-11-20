import type { CapacitorConfig } from '@capacitor/cli'

// The shell's native runtime (docs/plans/phone-shell.md, mobile/capacitor.config.ts): the development app id (a packaging that
// signs its own build sets its own), the bundle `make mobile-sync` fills, and navigation inside the web view allowed to nothing but
// that bundle. Open a server and a provider's sign-in page go to the in-app browser (@capacitor/browser); any other link leaves
// for the system's browser and never loads in the shell's page.
const config: CapacitorConfig = {
  appId: 'dev.conductor.mobile',
  appName: 'Conductor',
  webDir: 'www',
  server: {
    allowNavigation: [],
  },
}

export default config
