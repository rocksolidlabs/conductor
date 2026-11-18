import type { ReachInfo } from '~/composables/useSessions'
import { linkReach, type LinkReach } from './reach'

/** What a link just made reaches, in one line for the Share dialog. */
export type ShareReach = {
  /** `remote`: minted at the switchyard, works from anywhere; `unpublished`: a switchyard is configured but the session is not there; `local`: made here. */
  kind: 'remote' | 'unpublished' | 'local'
  level: LinkReach['level'] | 'remote'
  title: string
  text: string
}

/** The link as the create reply described it: where it was minted, or why not at the switchyard. */
export interface CreatedLink {
  url: string
  remote?: boolean
  rendezvous?: { server: string; error: string }
}

/**
 * reuseHeld says whether the Share dialog shows again the link it made for this target rather than make one: while that link is
 * listed and not revoked, unless the switchyard ended the publication since (republish: a link made now publishes it again), or
 * the link was made here because the session was not at the switchyard (rendezvous) and it is now (published).
 */
export function reuseHeld(
  created: { linkId: string; rendezvous?: unknown } | null,
  forThisTarget: boolean,
  links: ReadonlyArray<{ id: string; revoked?: boolean }>,
  publication: { republish: boolean; published: boolean },
): boolean {
  if (!created || !forThisTarget || publication.republish) return false
  if (created.rendezvous && publication.published) return false
  return links.some((l) => l.id === created.linkId && !l.revoked)
}

/** The host of a URL, for "shared through <host>"; the URL itself when it does not parse. */
export function hostOf(url: string): string {
  try {
    return new URL(url).host
  } catch {
    return url
  }
}

/**
 * shareReach says what a link reaches: a link minted at the switchyard works from anywhere; one made here although a switchyard is
 * configured says why the session (or no member of the crew) is not there, then what the local link reaches.
 */
export function shareReach(created: CreatedLink, reach: ReachInfo | null | undefined, run = false): ShareReach {
  if (created.remote) {
    return {
      kind: 'remote',
      level: 'remote',
      title: 'Works from anywhere',
      text: run
        ? `Shared through ${hostOf(created.url)}, which mints and checks the link: it opens every agent of the crew there, members who join later included, and each terminal comes to this machine over WebRTC, or through the switchyard's relay when no direct path forms.`
        : `Shared through ${hostOf(created.url)}, which mints and checks the link: it opens there, and the terminal comes to this machine over WebRTC, or through the switchyard's relay when no direct path forms.`,
    }
  }
  const local = linkReach(created.url, reach)
  if (created.rendezvous) {
    return {
      kind: 'unpublished',
      level: local.level,
      title: 'Not published to the switchyard',
      text: `${hostOf(created.rendezvous.server)}: ${created.rendezvous.error}. This link works where this machine is reachable. ${local.text}`,
    }
  }
  return { kind: 'local', level: local.level, title: local.title, text: local.text }
}

/** Why Share made no link, in the dialog's words: a title and its sentence. */
export interface ShareRefusal {
  title: string
  text: string
}

/**
 * shareRefusal says why no link can be made, from the code of the server's refusal of a link route: `run_record`, a run the server
 * keeps only as its record (stopped, then the server restarted, or let go with the oldest runs), which has no links and is given none
 * (docs/protocol.md, the run links' routes). Null for any other failure, which the dialog shows as the error it is.
 */
export function shareRefusal(code: string): ShareRefusal | null {
  if (code !== 'run_record') return null
  return {
    title: 'No link for this run',
    text: 'The server keeps this run only as its record: it ended before the server last started, or was let go with the oldest runs. A link opens only a run the server holds, so none was made. A selection the run still holds is answered on Needs you, not through a link.',
  }
}

/**
 * The Share dialog's line under its title, true for the dialog as it stands: it says the link is copied only while the dialog shows a
 * link it made and copied (`shown`: a link revoked since is shown no more, and the line goes with it); while one is being made, or when
 * making or copying it failed, it says what a link opens; refused, that none was made.
 */
export function shareDescription(o: { run: boolean; copied: boolean; shown: boolean; refused: boolean }): string {
  if (o.refused) return o.run ? 'No link was made for this run.' : 'No link was made.'
  if (o.copied && o.shown) return o.run ? 'The link is copied: it opens every agent of the crew, one added later too. Revoking disconnects whoever used it.' : 'The link is copied. Revoking disconnects whoever used it.'
  return o.run ? 'A link opens every agent of the crew, one added later too. Revoking disconnects whoever used it.' : 'A link opens this session. Revoking disconnects whoever used it.'
}
