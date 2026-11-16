import type { Integration } from '~/composables/useSessions'

/** Where an agent stands with Conductor's hooks: the one rule the Agents page's cards, its table and the Events page's aside read. */
export type ReportKey = 'reports' | 'experimental' | 'unset' | 'none'

/**
 * Whether an agent reports to Conductor, from its adapter's integration (`undefined`: an agent with no adapter). Its hooks reach it
 * when they are installed or a launch from this server injects them; an adapter still in preview says so; any other is not set up.
 */
export function reportStatus(it: Pick<Integration, 'installed' | 'launchInjection' | 'experimental'> | undefined): { key: ReportKey; words: string } {
  if (!it) return { key: 'none', words: 'No hooks: it reports by the bell or a screen pattern' }
  if (it.installed || it.launchInjection) return { key: 'reports', words: 'Reports to Conductor' }
  if (it.experimental) return { key: 'experimental', words: 'Experimental' }
  return { key: 'unset', words: 'Not set up' }
}

/** Why an integration card has no Install button. An adapter with no file to install into says so first, whatever the server's home. */
export function noInstallText(it: Pick<Integration, 'installable' | 'launchInjection'>, homeKnown: boolean): string {
  if (!it.installable) return it.launchInjection ? 'Nothing to install: this server wires it at launch; anywhere else, paste the snippet.' : 'Nothing to install: paste the snippet.'
  if (!homeKnown) return 'The server has no home directory to install into: paste the snippet.'
  return 'Nothing to install into on this machine: paste the snippet.'
}

/**
 * Whether an install left the agent's hooks to wire by hand, which the card's
 * snippet does: the server's `no_file_route` reply carries the snippet only
 * then, and not when a skill file of the user's own is all that is left.
 */
export function snippetLeft(error: { code: string; details: Record<string, unknown> }): boolean {
  return error.code === 'no_file_route' && typeof error.details.snippet === 'string' && error.details.snippet !== ''
}
