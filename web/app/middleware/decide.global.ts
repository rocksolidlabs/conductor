import { decideRedirect } from '~/utils/identityPass'
import { passHeld } from '~/composables/useIdentityPass'

/**
 * The decide page's gate (docs/plans/decide-grant.md, "web/app"), synchronous over the two stores and the fragment itself, so it judges
 * right whether it runs before or after the fragment plugin: a workbench token held opens the operator's decision page for a decide
 * address; a pass held without one keeps the tab on the decide page, so nothing of the workbench's layout or pages is set up for it.
 * A fragment that carries a credential in any spelling, or that cannot be read, is taken off whole: the guard keeps no fragment it
 * did not recognise.
 */
export default defineNuxtRouteMiddleware((to) => {
  const target = decideRedirect(to.fullPath, useWorkbenchToken().hasToken.value, passHeld())
  if (target !== null) return navigateTo(target, { replace: true })
})
