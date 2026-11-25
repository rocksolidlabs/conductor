// TEMPORARY (#1110): says on the console what makes this page load again in the phone shell's boot test, whose log
// keeps the page's console. Removed before the pull request merges.
export default defineNuxtPlugin((nuxtApp) => {
  const say = (what: string) => console.log('[diagnose-reload] ' + what)
  const nav = performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming | undefined
  say('start at ' + location.pathname + ' kind=' + (nav ? nav.type : 'unknown'))
  nuxtApp.hook('app:chunkError', (e: unknown) => say('chunk error: ' + String((e as { error?: unknown }).error ?? e)))
  nuxtApp.hook('app:error', (e: unknown) => say('app error: ' + String(e)))
  nuxtApp.hook('vue:error', (e: unknown, _i: unknown, info: unknown) => say('vue error: ' + String(e) + ' in ' + String(info)))
  nuxtApp.hook('app:manifest:update', () => say('manifest update'))
  window.addEventListener('vite:preloadError', (e) => say('preload error: ' + String((e as Event & { payload?: unknown }).payload)))
  window.addEventListener('error', (e) => say('window error: ' + e.message + ' at ' + e.filename + ':' + e.lineno))
  window.addEventListener('unhandledrejection', (e) => say('rejection: ' + String(e.reason)))
  window.addEventListener('beforeunload', () => say('beforeunload at ' + location.pathname))
  window.addEventListener('pagehide', () => say('pagehide at ' + location.pathname))
  const router = useRouter()
  router.beforeEach((to, from) => {
    say('route ' + from.fullPath + ' -> ' + to.fullPath)
  })
  router.onError((error, to) => say('router error to ' + to.fullPath + ': ' + String(error)))
})
