import { takeCredentials } from '~/utils/identityPass'

/**
 * A credential in the URL fragment, which never reaches the server: "Open in browser" from the desktop app opens the workbench with the
 * workbench token (`#token=…`), and an identity provider sends a person to the decide page with a pass (`#pass=…`). A fragment that
 * carries either key, in any spelling, is dropped from the address bar whether or not its values were taken; then the token goes into
 * the workbench token store and the pass into the tab's pass store. A refused value is stored nowhere. It runs before Nuxt's router
 * plugin (`nuxt:router`, `enforce: 'pre'`, order -20): the router's first address, its routes and its history state never hold it.
 */
export default defineNuxtPlugin({
  name: 'conductor:fragment-credentials',
  order: -25,
  setup() {
    const { token, pass, href } = takeCredentials(location.href)
    if (href !== location.href) history.replaceState(history.state, '', href)
    if (token) useWorkbenchToken().set(token)
    if (pass) useIdentityPass().set(pass)
  },
})
