import { EXAMPLES_LINE } from './crews'

/**
 * What each choice of the welcome does (#374), the tooltip of its button: a label names the choice, its tooltip says what pressing it
 * does. The example crews' tooltip is the line drawn under them, the one Crews and the palette say.
 */
export const welcomeTips = {
  /** An agent's button: the dialog opens with it picked, and nothing runs yet. */
  agent: (name: string) => `Opens the Launch dialog with ${name} picked; nothing starts until you launch.`,
  more: 'Opens the Launch dialog, to pick one of the other agents this machine has.',
  agents: 'Opens Agents: the agents Conductor knows, and which of them this machine has.',
  /** The catalog not read: the dialog reads it again and says why. */
  launch: 'Opens the Launch dialog, which looks for the agents on this machine again.',
  host: 'Opens the Quick start in a new tab: how to run conductor host on another machine.',
  examples: EXAMPLES_LINE,
} as const
