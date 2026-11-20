import { shellRedirect, shellBridge } from '~/utils/shell'

/**
 * The phone shell's gate (docs/plans/phone-shell.md, OSS-1b), synchronous over the bridge: inside a shell every address but /shell
 * and its children goes to /shell, so nothing of the workbench's layout, sidebar, token dialog or event stream is set up; outside a
 * shell /shell and its children go to /.
 */
export default defineNuxtRouteMiddleware((to) => {
  const target = shellRedirect(to.path, shellBridge() !== null)
  if (target !== null) return navigateTo(target, { replace: true })
})
