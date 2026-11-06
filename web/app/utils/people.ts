import type { KnownPerson, PersonTask } from '~/composables/usePeople'
import type { PaletteItem } from '~/composables/usePalette'
import { responsibilityWords } from './crews'

// The people Conductor knows, as the app shows them (docs/plans/decisions.md, HM1, PR-11): a person's page and the palette's `@` rows.
// A person is a local label, never a session: their page lists their work, and a row opens it.

/** A person's page: their name exactly, as the server matches it. */
export function personPath(name: string): string {
  return `/people/${encodeURIComponent(name)}`
}

/**
 * Where a person is known from, in words: what they hold in each crew that lists them ("architecture owner in api"), a crew where they
 * hold nothing by its name alone ("in web"), "and more crews" past the server's 20, and Dispatch's people when no crew lists them.
 */
export function personDetail(p: KnownPerson): string {
  const crews = p.crews.map((c) => {
    const words = responsibilityWords(c.responsibilities)
    return words ? `${words} in ${c.name}` : `in ${c.name}`
  })
  if (p.crewsMore) crews.push('and more crews')
  if (crews.length) return crews.join(', ')
  return p.dispatch ? "in Dispatch's people" : ''
}

/**
 * The palette's People rows (the `@` mode, between the page's people and You): one per person, by name (code unit order, as the server
 * pages them), "nate · architecture owner in api", Enter opening their page through `open`, ⌘↵ in a new tab.
 */
export function personRows(people: readonly KnownPerson[], open: (path: string) => void): PaletteItem[] {
  return [...people]
    .sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
    .map((p) => {
      const href = personPath(p.name)
      return { key: `person:${p.name}`, label: p.name, detail: personDetail(p) || undefined, icon: 'i-lucide-user-round', href, run: () => open(href) }
    })
}

/** How a person came to own a task, in words: "the acceptance owner" when the task names no owner of its own, '' when it names them. */
export function ownedHow(t: Pick<PersonTask, 'ownerFrom'>): string {
  return t.ownerFrom === 'acceptance' ? 'as the acceptance owner' : ''
}
