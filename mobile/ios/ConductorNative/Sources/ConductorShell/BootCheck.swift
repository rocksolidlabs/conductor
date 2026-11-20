import Foundation

/**
 * The boot check's side in the app (the plan's `ShellBootsUnderItsPolicyTests`): when, and only when, the app is launched with
 * `argument` (a UI test's launch; a person's launch passes none), a script added before the page's own reports each policy
 * violation the page meets and, once Open a server is drawn, tries each known way around the shell's gates (a plugin named by its
 * native class, a Cordova message, a text-input prompt of the runtime's cookies kind, a file under the runtime's file prefix), checks
 * that each is refused and that the calls the shell means still answer, and reports the bridge's platform, whether the page has the
 * native plugins it needs, and the probes' result. The app counts the probe messages its bridge gate dropped and shows all it heard
 * in one label the UI test reads.
 */
public enum BootCheck {
    public static let argument = "--conductor-boot-check"
    public static let handler = "conductorBootCheck"
    public static let identifier = "conductor-boot-check"
    /** The probes' messages to the runtime's handler carry callback ids with this prefix; the gate's drops of them are counted. */
    public static let probePrefix = "boot-probe-"
    /** How many probe messages the page sends to the runtime's handler, each of which the gate must drop. */
    public static let probeMessages = 4

    /** Whether this launch is the boot check's. */
    public static func asked(_ arguments: [String] = ProcessInfo.processInfo.arguments) -> Bool {
        arguments.contains(argument)
    }

    /** The drop of a body the gate refused, as the report takes it: only the probes' own. */
    public static func drop(_ body: Any) -> String? {
        guard let d = body as? [String: Any], let id = d["callbackId"] as? String, id.hasPrefix(probePrefix) else { return nil }
        return "dropped " + id
    }

    /** Hands the page the absolute path of an image of the app's own bundle, for the file-prefix probe. */
    public static func fileScript(_ path: String) -> String {
        let quoted = (try? String(data: JSONSerialization.data(withJSONObject: [path]), encoding: .utf8)) ?? "[\"\"]"
        return "window.__conductorBootFile = \(quoted)[0];"
    }

    /** Run at the document's start, before the page's own scripts. */
    public static let script = """
    (() => {
      const post = (m) => { try { window.webkit.messageHandlers.\(handler).postMessage(String(m)) } catch (e) {} }
      document.addEventListener('securitypolicyviolation', (e) => post('violation ' + e.violatedDirective + ' ' + e.blockedURI))
      const names = ['ConductorKey', 'ConductorStore', 'PushNotifications', 'App', 'Browser', 'CapacitorHttp']
      const toRuntime = (b) => window.webkit.messageHandlers.bridge.postMessage(b)
      // Each probe's call has a bound of its own: one that is never answered says so instead of holding the report.
      const within = (p) => Promise.race([p, new Promise((_, no) => setTimeout(() => no({ code: 'no_answer' }), 30000))])
      const code = async (f) => { try { await within(f()); return 'answered' } catch (e) { return (e && e.code) || 'rejected' } }
      const value = async (f) => { try { return await within(f()) } catch (e) { return null } }
      const probes = async () => {
        const failed = []
        const check = (name, ok, seen) => { if (!ok) failed.push(seen === undefined ? name : name + ':' + seen) }
        const P = (window.Capacitor && window.Capacitor.Plugins) || {}
        // Each way around a gate: a plugin named by its native class, the Cordova kind.
        toRuntime({ type: 'message', pluginId: 'CAPWebViewPlugin', methodName: 'getServerBasePath', options: {}, callbackId: '\(probePrefix)webview' })
        toRuntime({ type: 'message', pluginId: 'CAPHttpPlugin', methodName: 'request', options: { url: 'https://unnamed.example/' }, callbackId: '\(probePrefix)http' })
        toRuntime({ type: 'message', pluginId: 'CAPCookiesPlugin', methodName: 'getCookies', options: {}, callbackId: '\(probePrefix)cookies' })
        toRuntime({ type: 'cordova', service: 'Device', action: 'getDeviceInfo', actionArgs: [], callbackId: '\(probePrefix)cordova' })
        // A text-input prompt of the runtime's cookies kind: no answer.
        check('prompt-set', prompt(JSON.stringify({ type: 'CapacitorCookies.set', domain: 'https://boot.example', action: 'planted=1; Path=/' })) === null)
        check('prompt-get', prompt(JSON.stringify({ type: 'CapacitorCookies.get' })) === null)
        // The calls the shell means still answer, and the native HTTP call is still the shell's own (the class-named one would
        // have replaced it and reached the origin not named). WebView and CapacitorCookies are not the page's: the gate drops them.
        check('http', (await code(() => P.CapacitorHttp.request({ url: 'https://unnamed.example/' }))) === 'origin_refused')
        // The key answers from the shell's own plugin: its state, or its own refusal where an unsigned simulator build has no
        // keychain to read (code keychain); never no answer.
        const key = await code(() => P.ConductorKey.state())
        check('key', key === 'answered' || key === 'keychain', key)
        check('store', Array.isArray(((await value(() => P.ConductorStore.list({ area: 'drafts' }))) || {}).keys))
        // A file under the runtime's file prefix: not served.
        const file = window.__conductorBootFile
        check('file', !!file && await new Promise((r) => { const i = new Image(); i.onload = () => r(false); i.onerror = () => r(true); i.src = '/_capacitor_file_' + file }))
        return failed.length ? 'failed:' + failed.join(',') : 'ok'
      }
      let done = false
      const look = () => {
        if (done || !document.querySelector('[data-shell-open-server]')) return done
        done = true
        const shell = window.conductorShell
        const plugins = (window.Capacitor && window.Capacitor.Plugins) || {}
        const head = 'ready platform=' + (shell ? shell.platform : 'none') + ' plugins=' + (names.every((n) => plugins[n]) ? 'ok' : 'missing')
        probes().then((p) => post(head + ' probes=' + p), (e) => post(head + ' probes=threw:' + e))
        return true
      }
      const watch = () => {
        if (look()) return
        const o = new MutationObserver(() => { if (look()) o.disconnect() })
        o.observe(document.documentElement, { childList: true, subtree: true })
      }
      const begin = () => { post('started'); watch() }
      if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', begin, { once: true })
      else begin()
    })();
    """
}

/** What the boot check heard: the page's first "ready", the probe messages the gate dropped, and every violation (the first twenty). */
public struct BootReport: Equatable, Sendable {
    public private(set) var ready: String?
    public private(set) var violations: [String] = []
    public private(set) var dropped: Set<String> = []
    public private(set) var started = false

    public init() {}

    public mutating func take(_ message: String) {
        if message.hasPrefix("violation ") {
            if violations.count < 20 { violations.append(String(message.dropFirst("violation ".count).prefix(200))) }
        } else if message.hasPrefix("dropped ") {
            if dropped.count < 20 { dropped.insert(String(message.dropFirst("dropped ".count).prefix(100))) }
        } else if message == "started" {
            started = true
        } else if message.hasPrefix("ready "), ready == nil {
            ready = String(message.dropFirst("ready ".count).prefix(200))
        }
    }

    /**
     * `ready platform=ios plugins=ok probes=ok dropped=4 violations=0` once ready; `waiting ...` before; the first violation after.
     */
    public var label: String {
        let v = "dropped=\(dropped.count) violations=\(violations.count)" + (violations.first.map { " first: \($0)" } ?? "")
        return ready.map { "ready \($0) \(v)" } ?? "waiting \(started ? "started" : "unstarted") \(v)"
    }
}
