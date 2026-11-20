import Foundation

/**
 * The boot check's side in the app (the plan's `ShellBootsUnderItsPolicyTests`): when, and only when, the app is launched with
 * `argument` (a UI test's launch; a person's launch passes none), a script added before the page's own reports each policy
 * violation the page meets and, once Open a server is drawn, the bridge's platform and whether the page has the native plugins it
 * needs; the app shows what it heard in one label the UI test reads.
 */
public enum BootCheck {
    public static let argument = "--conductor-boot-check"
    public static let handler = "conductorBootCheck"
    public static let identifier = "conductor-boot-check"

    /** Whether this launch is the boot check's. */
    public static func asked(_ arguments: [String] = ProcessInfo.processInfo.arguments) -> Bool {
        arguments.contains(argument)
    }

    /** Run at the document's start, before the page's own scripts. */
    public static let script = """
    (() => {
      const post = (m) => { try { window.webkit.messageHandlers.\(handler).postMessage(String(m)) } catch (e) {} }
      document.addEventListener('securitypolicyviolation', (e) => post('violation ' + e.violatedDirective + ' ' + e.blockedURI))
      const names = ['ConductorKey', 'ConductorStore', 'PushNotifications', 'App', 'Browser', 'CapacitorHttp']
      const look = () => {
        if (!document.querySelector('[data-shell-open-server]')) return false
        const shell = window.conductorShell
        const plugins = (window.Capacitor && window.Capacitor.Plugins) || {}
        post('ready platform=' + (shell ? shell.platform : 'none') + ' plugins=' + (names.every((n) => plugins[n]) ? 'ok' : 'missing'))
        return true
      }
      const watch = () => {
        if (look()) return
        const o = new MutationObserver(() => { if (look()) o.disconnect() })
        o.observe(document.documentElement, { childList: true, subtree: true })
      }
      if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', watch, { once: true })
      else watch()
    })();
    """
}

/** What the boot check heard: the page's first "ready" and every violation (the first twenty), as one line. */
public struct BootReport: Equatable, Sendable {
    public private(set) var ready: String?
    public private(set) var violations: [String] = []

    public init() {}

    public mutating func take(_ message: String) {
        if message.hasPrefix("violation ") {
            if violations.count < 20 { violations.append(String(message.dropFirst("violation ".count).prefix(200))) }
        } else if message.hasPrefix("ready "), ready == nil {
            ready = String(message.dropFirst("ready ".count).prefix(100))
        }
    }

    /** `ready platform=ios plugins=ok violations=0` once ready; `waiting violations=N` before; the first violation after. */
    public var label: String {
        let v = "violations=\(violations.count)" + (violations.first.map { " first: \($0)" } ?? "")
        return ready.map { "ready \($0) \(v)" } ?? "waiting \(v)"
    }
}
