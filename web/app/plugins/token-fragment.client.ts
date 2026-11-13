import { takeCredentials } from '~/utils/identityPass'

/**
 * A credential in the URL fragment, which never reaches the server: "Open in browser" from the desktop app opens the workbench with the
 * workbench token (`#token=…`), and an identity provider sends a person to the decide page with a pass (`#pass=…`). A navigation the
 * browser starts (a load, a fragment change, back or forward) never lets the app's router read a fragment that carries either key, in
 * any spelling: at a load this runs before Nuxt's router plugin (`nuxt:router`, `enforce: 'pre'`, order -20), and later its capture
 * listener of the window's `popstate`, registered before the router's, takes the event first and ends it there. Either way the address
 * is rewritten without the fragment, whether or not its values were taken (a fragment that holds both keys gives neither). At a load the
 * token goes into the workbench token store and the pass into the tab's pass store. A fragment that arrives after the load is written
 * to the browser's storage for its credential alone, never to what the running page holds and sends, and the page is loaded again only
 * when that write was taken and changed what the storage held; a load drops what the page held (drafts, an answer whose outcome was not
 * known). A refused value is stored nowhere. A fragment with no key, that can be read, is left to the router.
 */
export default defineNuxtPlugin({
  name: 'conductor:fragment-credentials',
  order: -25,
  setup() {
    const workbench = useWorkbenchToken()
    const passes = useIdentityPass()
    /** The address rewritten without its fragment when that carries a key; what it carried, or null when it carries none and stays. */
    const takeOff = () => {
      const taken = takeCredentials(location.href)
      if (taken.href === location.href) return null
      history.replaceState(history.state, '', taken.href)
      return taken
    }
    const atLoad = takeOff()
    if (atLoad?.token) workbench.set(atLoad.token)
    if (atLoad?.pass) passes.set(atLoad.pass)
    window.addEventListener(
      'popstate',
      (e) => {
        const later = takeOff()
        if (!later) return
        e.stopImmediatePropagation()
        if ((later.token && workbench.stored(later.token)) || (later.pass && passes.stored(later.pass))) location.reload()
      },
      { capture: true },
    )
  },
})
