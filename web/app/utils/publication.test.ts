import { createHash, randomBytes } from 'node:crypto'
import { describe, expect, it } from 'vitest'
import type { PublicationSummary } from '~/composables/useDispatch'
import {
  MAX_PUBLICATION_BODY,
  UNCONFIRMED_WORDS,
  bodyProblem,
  closeWords,
  commentLink,
  composeBody,
  itemRef,
  markerWords,
  needYouWords,
  needsPerson,
  normalBody,
  offeredParts,
  packageSourceWords,
  previewHolds,
  previewParts,
  publicationActions,
  publicationWords,
  publicationsStripWords,
  sha256Hex,
  targetIsIssue,
} from './publication'

// A ticket's publications to GitHub (IN1a PR-9, utils/publication.ts): only the offer's parts are inserted, as their exact Markdown and
// after the person's text; the preview is offered for Send only when it is the bytes its digest names; each state's words and actions.

const OFFER = {
  parts: [
    { name: 'key', markdown: 'Conductor ticket CND-12: Fix the loop' },
    { name: 'deliveries', markdown: 'Delivered in:\n\n- https://github.com/acme/api/pull/7 the fix' },
    { name: 'checks', markdown: 'Checks of the delivered candidate:\n\n- verify test: passed' },
  ],
  targets: [],
}

function pub(p: Partial<PublicationSummary> = {}): PublicationSummary {
  return {
    id: 'p1',
    ticket: 'CND-12',
    target: 'linked',
    targetKind: 'issue',
    targetUrl: 'https://github.com/acme/api/issues/612',
    targetRef: 'acme/api#612',
    targetNodeId: 'I_1',
    targetSpaceId: 'R_1',
    marker: '<!-- conductor-publication:p1 -->',
    postSha256: '0'.repeat(64),
    parts: [],
    state: 'draft',
    tries: 0,
    attempt: 0,
    closeTries: 0,
    createdAt: '2026-11-16T10:00:00Z',
    createdBy: 'admin',
    rev: 1,
    ...p,
  }
}

describe('the parts a composer offers', () => {
  it('offers the three parts in their order, each with its exact Markdown', () => {
    const parts = offeredParts(OFFER)
    expect(parts.map((p) => p.name)).toEqual(['deliveries', 'checks', 'key'])
    expect(parts.find((p) => p.name === 'key')!.markdown).toBe('Conductor ticket CND-12: Fix the loop')
  })

  it('offers nothing but the parts: an unknown name, a repeat, an empty part are not offered', () => {
    const parts = offeredParts({
      parts: [
        { name: 'comments', markdown: 'a private comment' },
        { name: 'plan', markdown: 'the plan' },
        { name: 'key', markdown: 'Conductor ticket CND-12: first' },
        { name: 'key', markdown: 'Conductor ticket CND-12: second' },
        { name: 'checks', markdown: '   ' },
      ],
    })
    expect(parts).toEqual([{ name: 'key', label: "The ticket's key and title", markdown: 'Conductor ticket CND-12: first' }])
    expect(offeredParts(null)).toEqual([])
  })
})

describe("a publication's body", () => {
  const offered = offeredParts(OFFER)

  it("is the person's text alone while no part is ticked", () => {
    expect(composeBody('Fixed in the release.\n', offered, [])).toEqual({ body: 'Fixed in the release.', parts: [] })
  })

  it("inserts each ticked part's exact Markdown after the text, in the offer's order, a blank line between", () => {
    expect(composeBody('Fixed.', offered, ['key', 'deliveries'])).toEqual({
      body: 'Fixed.\n\nDelivered in:\n\n- https://github.com/acme/api/pull/7 the fix\n\nConductor ticket CND-12: Fix the loop',
      parts: ['deliveries', 'key'],
    })
  })

  it('inserts nothing for a name the offer does not hold', () => {
    expect(composeBody('Fixed.', offered, ['comments', 'constraints', 'findings'])).toEqual({ body: 'Fixed.', parts: [] })
    expect(composeBody('Fixed.', offeredParts({ parts: [{ name: 'key', markdown: 'k' }] }), ['deliveries'])).toEqual({ body: 'Fixed.', parts: [] })
  })

  it('may be the parts alone', () => {
    expect(composeBody('  ', offered, ['checks']).body).toBe('Checks of the delivered candidate:\n\n- verify test: passed')
  })

  it('is kept as the server keeps it, and refused as the server refuses it', () => {
    expect(normalBody('a\r\nb\rc  \n\t\n')).toBe('a\nb\nc')
    expect(bodyProblem(' \n ')).toBe('Write the outcome, or tick a part')
    expect(bodyProblem('x'.repeat(MAX_PUBLICATION_BODY))).toBe('')
    expect(bodyProblem('é'.repeat(MAX_PUBLICATION_BODY / 2 + 1))).toMatch(/^At most 16 KiB/)
    expect(bodyProblem('a\u0007b')).toMatch(/control character/)
    expect(bodyProblem('a\tb\nc')).toBe('')
  })
})

describe('the preview', () => {
  const marker = '<!-- conductor-publication:01HZX -->'
  const preview = `Fixed.\n\nConductor ticket CND-12: Fix the loop\n\n${marker}`
  const digest = createHash('sha256').update(preview, 'utf8').digest('hex')

  it("hashes as the server does, byte for byte", () => {
    expect(sha256Hex('')).toBe('e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855')
    expect(sha256Hex('abc')).toBe('ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad')
    for (const n of [55, 56, 63, 64, 65, 119, 1000, 16 << 10]) {
      const s = randomBytes(n).toString('base64').slice(0, n) + 'é✓🚂'
      expect(sha256Hex(s), `length ${n}`).toBe(createHash('sha256').update(s, 'utf8').digest('hex'))
    }
  })

  it('is offered for Send only when it is the bytes its digest names, ending with its marker after a blank line', () => {
    expect(previewHolds(preview, digest, marker)).toBe(true)
    expect(previewParts(preview, marker)).toEqual({ body: 'Fixed.\n\nConductor ticket CND-12: Fix the loop', marker })
    // One byte other than the digest's bytes.
    expect(previewHolds(preview + ' ', digest, marker)).toBe(false)
    expect(previewHolds(preview.replace('Fixed', 'fixed'), digest, marker)).toBe(false)
    // Another publication's marker, a marker not at the end, no blank line before it, no body.
    expect(previewHolds(preview, digest, '<!-- conductor-publication:other -->')).toBe(false)
    expect(previewParts(`${marker}\n\nFixed.`, marker)).toBeNull()
    expect(previewParts(`Fixed.\n${marker}`, marker)).toBeNull()
    expect(previewParts(`\n\n${marker}`, marker)).toBeNull()
    expect(previewHolds(preview, digest.toUpperCase(), marker)).toBe(false)
    expect(previewHolds(preview, '', marker)).toBe(false)
  })

  it('says the marker in words: hidden on GitHub, the random id, nothing of the ticket', () => {
    expect(markerWords(marker)).toContain(marker)
    expect(markerWords(marker)).toMatch(/hidden on GitHub/)
    expect(markerWords(marker)).toMatch(/says nothing of the ticket/)
  })
})

describe('targets', () => {
  it('offers the close only for an issue', () => {
    expect(targetIsIssue('linked', 'https://github.com/acme/api/issues/612', 'issue')).toBe(true)
    expect(targetIsIssue('linked', 'https://github.com/acme/api/issues/612', 'change')).toBe(false)
    expect(targetIsIssue('link:L1', 'https://github.com/acme/api/issues/3')).toBe(true)
    expect(targetIsIssue('link:L1', 'https://github.com/acme/api/pull/3')).toBe(false)
    expect(targetIsIssue('link:L1', 'not a url')).toBe(false)
    expect(itemRef('http://127.0.0.1:4100/acme/api/pull/7')).toBe('acme/api#7')
    expect(itemRef('https://example.com/acme')).toBe('')
  })
})

describe("a publication's state and actions", () => {
  const now = Date.parse('2026-11-16T15:00:00Z')

  it('says who it was published as, where and when', () => {
    const w = publicationWords(pub({ state: 'sent', sentAsLogin: 'alice', sentAt: '2026-11-16T14:02:00Z' }), now)
    expect(w.text).toMatch(/^Published to acme\/api#612 by @alice at \S+/)
    expect(w.tone).toBe('default')
  })

  it("says a publication not confirmed in the plan's words, never in amber", () => {
    const w = publicationWords(pub({ state: 'unconfirmed' }), now)
    expect(w.text).toContain(UNCONFIRMED_WORDS)
    expect(w.tone).toBe('muted')
    expect(publicationWords(pub({ state: 'failed', error: 'GitHub refused the token (token_refused)' }), now)).toEqual({ text: 'Not sent to acme/api#612: GitHub refused the token (token_refused)', tone: 'error' })
  })

  it('offers no Send for one not confirmed, one being sent, or one restored', () => {
    expect(publicationActions(pub({ state: 'unconfirmed' }))).toEqual(['check', 'new', 'discard'])
    expect(publicationActions(pub({ state: 'sending' }))).toEqual(['check'])
    expect(publicationActions(pub({ state: 'failed', restored: true }))).toEqual(['check', 'new', 'discard'])
    expect(publicationActions(pub({ state: 'draft', restored: true }))).toEqual(['check', 'new', 'discard'])
    for (const p of [pub({ state: 'unconfirmed' }), pub({ state: 'sending' }), pub({ state: 'failed', restored: true }), pub({ state: 'draft', restored: true }), pub({ state: 'sent', restored: true })]) {
      expect(publicationActions(p)).not.toContain('send-again')
    }
  })

  it('offers Send again for a failed one of this store, Close now for a close not finished, Discard for a draft', () => {
    expect(publicationActions(pub({ state: 'failed' }))).toEqual(['send-again', 'discard'])
    expect(publicationActions(pub({ state: 'draft' }))).toEqual(['discard'])
    expect(publicationActions(pub({ state: 'sent', closeIssue: 'completed', closeState: 'refused' }))).toEqual(['close-now'])
    expect(publicationActions(pub({ state: 'sent', closeIssue: 'completed', closeState: 'closed' }))).toEqual([])
    expect(publicationActions(pub({ state: 'sent', restored: true, closeIssue: 'completed', closeState: 'pending' }))).toEqual([])
    expect(publicationActions(pub({ state: 'discarded' }))).toEqual([])
  })

  it("counts those a person can act on as the server's needYou does", () => {
    expect(needsPerson(pub({ state: 'draft' }))).toBe(false)
    expect(needsPerson(pub({ state: 'draft', restored: true }))).toBe(true)
    expect(needsPerson(pub({ state: 'sent', restored: true, closeIssue: 'completed', closeState: 'pending' }))).toBe(false)
    expect(needsPerson(pub({ state: 'sent', closeIssue: 'completed', closeState: 'closing' }))).toBe(true)
    expect(needYouWords(0)).toBe('')
    expect(needYouWords(1)).toBe('1 older publication needs you')
    expect(needYouWords(3)).toBe('3 older publications need you')
    expect(publicationsStripWords([pub({ state: 'failed' }), pub({ state: 'sent' })], 2)).toBe('3 publications need you')
    expect(publicationsStripWords([pub({ state: 'sent' })], 0)).toBe('')
  })

  it("says the issue's close", () => {
    expect(closeWords(pub()).text).toBe('')
    expect(closeWords(pub({ state: 'sent', closeIssue: 'completed', closeState: 'closed' })).text).toBe('The issue is closed as completed')
    expect(closeWords(pub({ state: 'sent', closeIssue: 'completed', closeState: 'refused', closeError: 'GitHub refused the close (tracker_unavailable)' })).tone).toBe('error')
  })

  it("draws the comment's address as a link only on the target's own page", () => {
    const base = { targetUrl: 'https://github.com/acme/api/issues/612' }
    expect(commentLink({ ...base, remoteUrl: 'https://github.com/acme/api/issues/612#issuecomment-99' })).toBe('https://github.com/acme/api/issues/612#issuecomment-99')
    expect(commentLink({ ...base, remoteUrl: 'https://evil.example/acme/api/issues/612#issuecomment-99' })).toBe('')
    expect(commentLink({ ...base, remoteUrl: 'javascript:alert(1)' })).toBe('')
    expect(commentLink({ ...base, remoteUrl: 'http://github.com/acme/api/issues/612#issuecomment-99' })).toBe('')
    expect(commentLink({ ...base, remoteUrl: 'https://github.com/acme/api/issues/613#issuecomment-99' })).toBe('')
    expect(commentLink({ ...base, remoteUrl: 'https://github.com/acme/api/issues/612?x=1#issuecomment-99' })).toBe('')
    expect(commentLink({ ...base, remoteUrl: 'https://github.com/acme/api/issues/612#x' })).toBe('')
    expect(commentLink({ ...base, remoteUrl: '' })).toBe('')
  })
})

describe("a run's source", () => {
  it("names the GitHub item a launch's package names as its source", () => {
    expect(packageSourceWords({ source: { system: 'github', ref: 'acme/api#612', titleFromGitHub: true } })).toBe('GitHub acme/api#612')
  })

  it('names nothing for no source, one held unlinked, another system or a reference that is not one', () => {
    expect(packageSourceWords({ version: 2 })).toBe('')
    expect(packageSourceWords(null)).toBe('')
    expect(packageSourceWords({ source: { unlinked: true, titleFromGitHub: true } })).toBe('')
    expect(packageSourceWords({ source: { system: 'jira', ref: 'acme/api#612' } })).toBe('')
    expect(packageSourceWords({ source: { system: 'github', ref: 'acme/api#612 <b>x</b>' } })).toBe('')
    expect(packageSourceWords({ source: { system: 'github', ref: 12 } })).toBe('')
  })
})
