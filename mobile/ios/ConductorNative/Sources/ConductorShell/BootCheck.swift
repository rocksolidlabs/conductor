import Foundation

/**
 * The boot check's side in the app (the plan's `ShellBootsUnderItsPolicyTests`): when, and only when, the app is launched with
 * `argument` (a UI test's launch; a person's launch passes none), a script added before the page's own reports each policy
 * violation the page meets and, once Open a server is drawn, tries each known way around the shell's gates and checks that each is
 * refused while the calls the shell means still answer. It reports the bridge's platform, whether the page has the native plugins it
 * needs, and the probes' result; the app counts the probe messages its bridge gate dropped and the probe loads its load gate
 * refused, and shows all it heard in one label the UI test reads.
 */
public enum BootCheck {
    public static let argument = "--conductor-boot-check"
    public static let handler = "conductorBootCheck"
    public static let identifier = "conductor-boot-check"
    /**
     * The probes' messages to the runtime's handler carry callback ids that begin with this, of the runtime's own digits form and past
     * any the runtime's page script reaches (it counts up from below 2^27); the gate's drops of them are counted.
     */
    public static let probePrefix = "90000000"
    /** The probes' loads carry this in their address; the load gate's refusals of them are counted. */
    public static let loadMarker = "boot-probe"
    /**
     * How many probe messages the page sends to the runtime's handler, and how many probe loads reach the load gate (the two frames
     * and the three location changes; a script's window opened without a person's tap is refused by WebKit before any delegate).
     */
    public static let probeMessages = 8
    public static let probeLoads = 5
    /** What the app evaluates in the page when the load gate refuses a probe load, so the page waits for that before its next one. */
    public static let refusedScript = "window.__conductorBootRefusals = (window.__conductorBootRefusals || 0) + 1"
    /**
     * A provider's id with characters outside the path's alphabet, and the segment the shell's screens carry it in (decisionPath in
     * web/app/utils/shell.ts: its UTF-8 bytes in unpadded base64url; the screens' tests hold the same pair).
     */
    public static let decisionId = "org/repo#12 a:b~é"
    public static let decisionSegment = "b3JnL3JlcG8jMTIgYTpifsOp"

    /** Whether this launch is the boot check's. */
    public static func asked(_ arguments: [String] = ProcessInfo.processInfo.arguments) -> Bool {
        arguments.contains(argument)
    }

    /** The drop of a body the gate refused, as the report takes it: only the probes' own. */
    public static func drop(_ body: Any) -> String? {
        guard let d = body as? [String: Any], let id = d["callbackId"] as? String, id.hasPrefix(probePrefix) else { return nil }
        return "dropped " + String(id.prefix(10))
    }

    /** A load the load gate refused, as the report takes it: only the probes' own. */
    public static func refusal(_ url: URL) -> String? {
        let s = url.absoluteString
        guard s.contains(loadMarker) else { return nil }
        return "refused " + String(s.prefix(120))
    }

    /** A string as a JavaScript literal. */
    static func literal(_ s: String) -> String {
        (try? String(data: JSONSerialization.data(withJSONObject: [s]), encoding: .utf8)).map { "\($0)[0]" } ?? "''"
    }

    /**
     * Hands the page what the file probes ask for: the absolute path of an image of the app's own bundle, and the path of an image
     * outside it, from the bundle's folder, its separators escaped (`..%2F`).
     */
    public static func fileScript(_ path: String, climb: String) -> String {
        "window.__conductorBootFile = \(literal(path)); window.__conductorBootClimb = \(literal(climb));"
    }

    /** The climb from the bundle's folder (`base`) to `target`, each separator escaped: what the router must answer as nowhere. */
    public static func climb(from base: String, to target: String) -> String {
        let ups = URL(fileURLWithPath: base).pathComponents.count - 1
        let rest = URL(fileURLWithPath: target).pathComponents.dropFirst().joined(separator: "%2F")
        return "/" + String(repeating: "..%2F", count: ups) + rest
    }

    /** Run at the document's start, before the page's own scripts. */
    public static let script = """
    (() => {
      const post = (m) => { try { window.webkit.messageHandlers.\(handler).postMessage(String(m)) } catch (e) {} }
      document.addEventListener('securitypolicyviolation', (e) => post('violation ' + e.violatedDirective + ' ' + e.blockedURI))
      const names = ['ConductorKey', 'ConductorStore', 'PushNotifications', 'App', 'Browser', 'CapacitorHttp']
      const toRuntime = (b, w) => (w || window).webkit.messageHandlers.bridge.postMessage(b)
      // Each probe's wait has a bound of its own: one never answered says so instead of holding the report.
      const within = (p) => Promise.race([p, new Promise((_, no) => setTimeout(() => no({ code: 'no_answer' }), 30000))])
      const code = async (f) => { try { await within(f()); return 'answered' } catch (e) { return (e && e.code) || 'rejected' } }
      const value = async (f) => { try { return await within(f()) } catch (e) { return null } }
      const script = (src) => within(new Promise((r) => { const s = document.createElement('script'); s.onload = () => r('loaded'); s.onerror = () => r('refused'); s.src = src; document.head.appendChild(s) })).catch(() => 'no_answer')
      const refusals = () => window.__conductorBootRefusals || 0
      const refusedBy = (n) => within(new Promise((r) => { const t = setInterval(() => { if (refusals() >= n) { clearInterval(t); r(true) } }, 50) })).catch(() => false)
      const image = (src) => within(new Promise((r) => { const i = new Image(); i.onload = () => r('loaded'); i.onerror = () => r('refused'); i.src = src })).catch(() => 'no_answer')
      const id = (n) => '\(probePrefix)' + String(n).padStart(2, '0')
      const probes = async () => {
        const failed = []
        const check = (name, ok, seen) => { if (!ok) failed.push(seen === undefined ? name : name + ':' + seen) }
        const P = (window.Capacitor && window.Capacitor.Plugins) || {}
        // The bridge gate: a plugin named by its native class, the Cordova kind, a method outside the table on a plugin the page reaches,
        // a callback id that is no callback id (its script, were it written into the page, would set a global), each dropped.
        toRuntime({ type: 'message', pluginId: 'CAPWebViewPlugin', methodName: 'getServerBasePath', options: {}, callbackId: id(1) })
        toRuntime({ type: 'message', pluginId: 'CAPHttpPlugin', methodName: 'request', options: { url: 'https://unnamed.example/' }, callbackId: id(2) })
        toRuntime({ type: 'message', pluginId: 'CAPCookiesPlugin', methodName: 'getCookies', options: {}, callbackId: id(3) })
        toRuntime({ type: 'cordova', service: 'Device', action: 'getDeviceInfo', actionArgs: [], callbackId: id(4) })
        toRuntime({ type: 'message', pluginId: 'PushNotifications', methodName: 'getDeliveredNotifications', options: {}, callbackId: id(5) })
        toRuntime({ type: 'message', pluginId: 'ConductorKey', methodName: 'state', options: {}, callbackId: id(6) + "'}); window.__conductorBootInjected = 1; ({a:'" })
        // A frame of the page's own (a srcdoc frame, as the diagram's is): it draws, and its call to the runtime is dropped.
        const frame = document.createElement('iframe')
        frame.srcdoc = '<p id="drawn">drawn</p>'
        const drawn = within(new Promise((r) => { frame.onload = () => r(true) })).catch(() => false)
        document.body.appendChild(frame)
        await drawn
        check('srcdoc', !!(frame.contentDocument && frame.contentDocument.getElementById('drawn')))
        try { toRuntime({ type: 'message', pluginId: 'ConductorKey', methodName: 'state', options: {}, callbackId: id(7) }, frame.contentWindow) } catch (e) { check('frame-post', false, String(e)) }
        // The load gate: a frame at an outside address and at the app's own; then main-frame loads one at a time (a location change
        // takes the place of one not yet decided), each waited on until the app has said it refused it: to a custom scheme, and to the
        // app's own address with its host in another case and with an escape in it, neither of which the runtime would take as the
        // app's; and a new window to a custom scheme.
        for (const src of ['https://example.com/?\(loadMarker)-frame', '/shell?\(loadMarker)-frame']) {
          const f = document.createElement('iframe')
          f.src = src
          document.body.appendChild(f)
        }
        check('frames', await refusedBy(2), refusals())
        const before = location.href
        const host = location.host
        const away = [
          ['location', 'conductor-probe://\(loadMarker)-location'],
          ['case', location.protocol + '//' + host.toUpperCase() + '/?\(loadMarker)-case'],
          ['escape', location.protocol + '//' + host[0] + '%' + host.charCodeAt(1).toString(16) + host.slice(2) + '/?\(loadMarker)-escape'],
        ]
        for (const [name, to] of away) {
          const n = refusals()
          try { location.href = to } catch (e) {}
          check(name, await refusedBy(n + 1))
        }
        const opened = window.open('conductor-probe://\(loadMarker)-window')
        check('window', opened === null)
        // A text-input prompt of the runtime's cookies kind: no answer.
        check('prompt-set', prompt(JSON.stringify({ type: 'CapacitorCookies.set', domain: 'https://boot.example', action: 'planted=1; Path=/' })) === null)
        check('prompt-get', prompt(JSON.stringify({ type: 'CapacitorCookies.get' })) === null)
        // The calls the shell means still answer, and the native HTTP call is still the shell's own.
        check('http', (await code(() => P.CapacitorHttp.request({ url: 'https://unnamed.example/' }))) === 'origin_refused')
        const key = await code(() => P.ConductorKey.state())
        check('key', key === 'answered' || key === 'keychain', key)
        check('store', Array.isArray(((await value(() => P.ConductorStore.list({ area: 'drafts' }))) || {}).keys))
        check('injected', window.__conductorBootInjected === undefined)
        check('location', location.href === before)
        // The scheme handler: the file prefix, plain and escaped, and a climb out of the bundle, none served.
        const file = window.__conductorBootFile
        check('file', !!file && (await image('/_capacitor_file_' + file)) === 'refused')
        check('file-escaped', !!file && (await image('/%5Fcapacitor_file_' + file)) === 'refused')
        check('climb', !!window.__conductorBootClimb && (await image(window.__conductorBootClimb)) === 'refused')
        // A decision's page address as the shell's screens build it for an id outside the path's alphabet: let through (the bundle's
        // page answers); the same id written into the path as it stands: refused.
        check('decision-carried', (await script('/shell/decisions/\(decisionSegment)')) === 'loaded')
        check('decision-spelled', (await script('/shell/decisions/' + encodeURIComponent(\(literal(decisionId))))) === 'refused')
        // A listener call with no event, which the runtime's base plugin cannot take: dropped, and the app still answers.
        toRuntime({ type: 'message', pluginId: 'App', methodName: 'addListener', options: {}, callbackId: id(8) })
        const after = await code(() => P.ConductorKey.state())
        check('after-listener', after === 'answered' || after === 'keychain', after)
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

/**
 * What the boot check heard: the page's first "ready", the probe messages the gate dropped, the probe loads the load gate refused, and
 * every violation (the first twenty).
 */
public struct BootReport: Equatable, Sendable {
    public private(set) var ready: String?
    public private(set) var violations: [String] = []
    public private(set) var dropped: Set<String> = []
    public private(set) var refused: Set<String> = []
    public private(set) var started = false

    public init() {}

    public mutating func take(_ message: String) {
        if message.hasPrefix("violation ") {
            if violations.count < 20 { violations.append(String(message.dropFirst("violation ".count).prefix(200))) }
        } else if message.hasPrefix("dropped ") {
            if dropped.count < 20 { dropped.insert(String(message.dropFirst("dropped ".count).prefix(100))) }
        } else if message.hasPrefix("refused ") {
            if refused.count < 20 { refused.insert(String(message.dropFirst("refused ".count).prefix(120))) }
        } else if message == "started" {
            started = true
        } else if message.hasPrefix("ready "), ready == nil {
            ready = String(message.dropFirst("ready ".count).prefix(400))
        }
    }

    /**
     * `ready platform=ios plugins=ok probes=ok dropped=8 refused=5 violations=0` once ready; `waiting ...` before; the first violation
     * after.
     */
    public var label: String {
        let v = "dropped=\(dropped.count) refused=\(refused.count) violations=\(violations.count)" + (violations.first.map { " first: \($0)" } ?? "")
        return ready.map { "ready \($0) \(v)" } ?? "waiting \(started ? "started" : "unstarted") \(v)"
    }
}
