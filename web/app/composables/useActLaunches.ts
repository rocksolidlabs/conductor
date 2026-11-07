import type { LaunchSummary } from '~/composables/useDispatch'

/**
 * The launches Brakeman's acts name, as the page last read them, by the launch's id (actLaunchId), for the app: an act's card reads its
 * launch into it (followLaunch), and the card and the proposal that names the act both draw what they show from it and the request's
 * act (actShownFrom), so a re-read of the request, which hands the same act as a new object, keeps what they show. At most
 * MAX_KEPT_LAUNCHES (keepLaunch).
 */
export function useActLaunches() {
  return useState<Record<string, LaunchSummary>>('brakemanActLaunches', () => ({}))
}
