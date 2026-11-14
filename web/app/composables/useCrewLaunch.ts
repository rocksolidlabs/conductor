/**
 * A launch of a crew asked for (docs/plans/capacity.md, PR-7): every launch of a crew from the app's screens opens the crews page's launch
 * sheet, and only the sheet's Launch starts a run. `ask` names the saved crew (none: the crew on the crews page's screen) and the run's
 * name when one was typed. Away from the crews page, or on another crew's page, it shows that crew's page first, where the sheet opens;
 * an ask whose navigation ends anywhere else is dropped. The page takes each ask once (`asked`, then null): none waits for a later visit.
 */
export function useCrewLaunch() {
  const asked = useState<{ id: string; label: string } | null>('crewLaunchAsked', () => null)
  const router = useRouter()
  /** Whether the router is at the crew's own page (`home`: or at the Crews home, whose sheet opens there). */
  const at = (id: string, home: boolean) => {
    const r = router.currentRoute.value
    return (home && r.path === '/crews') || (r.path.startsWith('/crews/') && r.params.id === id)
  }
  async function ask(id = '', label = '') {
    asked.value = null
    if (id && !at(id, true) && ((await router.push(`/crews/${encodeURIComponent(id)}`)) || !at(id, false))) return
    asked.value = { id, label }
  }
  return { asked, ask }
}
