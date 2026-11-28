/**
 * What the server says of itself on GET /api/whoami, read once per app: its host name (for the "Not installed on <host>" notes) and
 * whether it is a switchyard, a coordinator of hosted sessions that launches nothing (the Agents and Crews pages say so). Empty and
 * false until known or when the server cannot tell.
 */
export function useServerHost() {
  const host = useState<string>('serverHost', () => '')
  const switchyard = useState<boolean>('serverSwitchyard', () => false)
  /** The server user's home directory; '' until known or when it has none. */
  const home = useState<string>('serverHome', () => '')
  /** The server's version (`conductor version`); '' until known. */
  const version = useState<string>('serverVersion', () => '')
  /** Where the server's folders come from: `app` when the desktop app that started it may allow one while it runs, `config`; '' until known. */
  const folders = useState<string>('serverFolders', () => '')
  const asked = useState<boolean>('serverHostAsked', () => false)
  const api = useSessions()
  async function load() {
    if (asked.value) return
    asked.value = true
    try {
      const me = await api.whoami()
      host.value = me.host ?? ''
      switchyard.value = me.switchyard === true
      home.value = me.home ?? ''
      version.value = me.version ?? ''
      folders.value = me.folders ?? ''
    } catch {
      asked.value = false
    }
  }
  return { host, switchyard, home, version, folders, load }
}
