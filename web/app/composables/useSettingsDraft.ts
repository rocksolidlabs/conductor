import type { BrakemanConfig } from '~/utils/brakeman'
import type { DesktopSettings } from '~/utils/desktop'
import type { ConfigDraft } from '~/utils/settingsSave'

/**
 * Settings' edits not saved yet, kept while the window lives (UX3 R81): a visit to another page leaves them, and the page shows them
 * again on its return, its bar saying what changed. A reload, a closed app or a new server drops them, as before: this is the page's
 * memory, never the server's or the disk's. On return, a field whose saved value changed meanwhile takes the saved value
 * (`draftBack` in utils/settingsSave.ts), and a draft of Brakeman's made under another token is dropped.
 */
export interface SettingsDraft {
  /** The app's form as it was left, and the settings it was read against. */
  app?: { form: DesktopSettings; saved: DesktopSettings }
  /** Brakeman's form as it was left (its configForm's `keep`). */
  brakeman?: ConfigDraft<BrakemanConfig>
}

export function useSettingsDraft() {
  return useState<SettingsDraft>('settingsDraft', () => ({}))
}
