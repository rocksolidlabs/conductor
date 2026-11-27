import { EXAMPLES_LINE } from './crews'

/**
 * What each choice of the welcome does (#374), the tooltip of its button: a label names the choice, its tooltip says what pressing it
 * does, in a few words. The example crews' tooltip is the line Crews and the palette say under theirs.
 */
export const welcomeTips = {
  /** An agent's button: the dialog opens with it picked; its version, when its check found one, rides here, not on the button. */
  agent: (name: string, version?: string) => `Opens the Launch dialog with ${name}${version ? ` ${version}` : ''} picked.`,
  more: 'Opens the Launch dialog with every agent this machine has.',
  agents: 'Opens Agents: the agents Conductor knows, and which of them this machine has.',
  /** The catalog not read: the dialog reads it again and says why. */
  launch: 'Opens the Launch dialog, which looks for the agents on this machine again.',
  host: 'Opens the Quick start: how to run conductor host on another machine.',
  examples: EXAMPLES_LINE,
} as const

/** The toast after the welcome loaded the example crews, in the words Crews says: how many were added, or that they were there. */
export function examplesToast(added: number): { title: string; color: 'success' | 'neutral' } {
  if (!added) return { title: 'The examples are here already', color: 'neutral' }
  return { title: `${added} example ${added === 1 ? 'crew' : 'crews'} added`, color: 'success' }
}

/** The toast's title when loading the examples failed; the server's words are its description. */
export const EXAMPLES_FAILED = 'Loading the examples failed'
