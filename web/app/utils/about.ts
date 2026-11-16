/** What the app says of itself: Settings → The app, the join page's credit line, the desktop splash. */

/** The sponsor line, the same words everywhere it shows (the Sponsor Kit): footers and About screens, never the workspace. */
export const CREDIT = 'Sponsored and maintained by RockSolid Labs'
export const SPONSOR_NAME = 'RockSolid Labs'
export const SPONSOR_URL = 'https://rocksolidlabs.io'
export const SPONSOR_HOST = 'rocksolidlabs.io'
export const SOURCE_URL = 'https://github.com/rocksolidlabs/conductor'
/** The README's Quick start, where bringing in a session from another machine (`conductor host`) is shown. */
export const QUICK_START_URL = `${SOURCE_URL}#quick-start`
/** The holder, as LICENSE, NOTICE and the desktop package name it. */
export const COPYRIGHT_HOLDER = 'the Conductor Authors and RockSolid Labs, Inc.'
export const LICENSE = 'Apache-2.0'
export const LICENSE_URL = `${SOURCE_URL}/blob/main/LICENSE`
/** The licences of what the binary and the app ship (THIRD_PARTY_NOTICES), as the server serves them, a switchyard too. */
export const NOTICES_PATH = '/third-party-notices.txt'
/** The copyright line: the year of the first release, through `year` when later. */
export function copyrightLine(year: number = new Date().getFullYear()): string {
  const first = 2026
  return `© ${year > first ? `${first}–${year}` : first} ${COPYRIGHT_HOLDER}`
}

/** "0.6.0 · naterdev-win": a version with the machine it runs on, either part left out when unknown. */
export function versionOn(version: string, host: string): string {
  return [version, host].filter(Boolean).join(' · ')
}
