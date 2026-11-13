import { takeCredentials } from '~/utils/identityPass'

/**
 * A credential in the URL fragment, which never reaches the server: "Open in browser" from the desktop app opens the workbench with the
 * workbench token (`#token=…`), and an identity provider sends a person to the decide page with a pass (`#pass=…`). Both keys are
 * dropped from the address bar before anything else runs, whether or not their values were taken; then the token goes into the
 * workbench token store and the pass into the tab's pass store. A refused value is stored nowhere.
 */
export default defineNuxtPlugin(() => {
  const { token, pass, href } = takeCredentials(location.href)
  if (href !== location.href) history.replaceState(history.state, '', href)
  if (token) useWorkbenchToken().set(token)
  if (pass) useIdentityPass().set(pass)
})
