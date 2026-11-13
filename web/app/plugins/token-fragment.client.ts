import { takeCredentials } from '~/utils/identityPass'

/**
 * A credential in the URL fragment, which never reaches the server: "Open in browser" from the desktop app opens the workbench with the
 * workbench token (`#token=…`), and an identity provider sends a person to the decide page with a pass (`#pass=…`). A navigation the
 * browser starts (a load, a fragment change, back or forward) never lets the app's router read a fragment that carries either key, in
 * any spelling: at a load this runs before Nuxt's router plugin (`nuxt:router`, `enforce: 'pre'`, order -20), and later its capture
 * listener of the window's `popstate`, registered before the router's, takes the event first and ends it there. Either way the address
 * is rewritten without the fragment, whether or not its values were taken; then the token goes into the workbench token store and the
 * pass into the tab's pass store (a fragment that holds both keys gives neither); a fragment that arrives after the load then loads
 * the page again at the clean address, so nothing drawn for an earlier credential stays. A refused value is stored nowhere. A fragment with no key is
 * left to the router.
 */
export default defineNuxtPlugin({
  name: 'conductor:fragment-credentials',
  order: -25,
  setup() {
    const workbench = useWorkbenchToken()
    const passes = useIdentityPass()
    /** Takes what the address's fragment carries into its store; false when it carries no key and is left as it is. */
    const take = () => {
      const { token, pass, href } = takeCredentials(location.href)
      if (href === location.href) return false
      history.replaceState(history.state, '', href)
      if (token) workbench.set(token)
      if (pass) passes.set(pass)
      return true
    }
    take()
    window.addEventListener(
      'popstate',
      (e) => {
        if (!take()) return
        e.stopImmediatePropagation()
        location.reload()
      },
      { capture: true },
    )
  },
})
