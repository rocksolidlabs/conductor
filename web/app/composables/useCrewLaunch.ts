/**
 * A launch of a crew asked for (docs/plans/capacity.md, PR-7): every launch of a crew from the app's screens opens the crews page's launch
 * sheet, and only the sheet's Launch starts a run. `ask` names the saved crew (none: the crew on the crews page's screen) and the run's
 * name when one was typed. Away from the crews page, or on another crew's page, it shows that crew's page first, where the sheet opens;
 * the page takes each ask once (`asked`, then null).
 */
export function useCrewLaunch() {
  const asked = useState<{ id: string; label: string } | null>('crewLaunchAsked', () => null)
  const route = useRoute()
  const router = useRouter()
  async function ask(id = '', label = '') {
    const here = route.path === '/crews' || (route.path.startsWith('/crews/') && route.params.id === id)
    if (id && !here && (await router.push(`/crews/${encodeURIComponent(id)}`))) return
    asked.value = { id, label }
  }
  return { asked, ask }
}
