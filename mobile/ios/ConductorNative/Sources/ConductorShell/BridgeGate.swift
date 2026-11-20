import Foundation
import WebKit

/**
 * The gate at the native runtime's script message handler (`bridge`), the one way a page's script calls native code by message.
 * The runtime reads five fields of a message (`type`, `pluginId`, `methodName`, `callbackId`, `options`), makes a plugin from a
 * native class when `pluginId` names no registered plugin, hands the Cordova kind to its Cordova layer, and writes `callbackId`,
 * `pluginId` and `methodName` back into script it evaluates in the main frame. The gate forwards a message only when:
 * - it comes from the shell's own page: the main frame of the shell's own view, at the app's own origin;
 * - its kind is exactly `message`;
 * - its plugin and method are one pair of a closed table, the calls the bridge (mobile/src/bridge.ts) makes and the listener calls
 *   the runtime's page script makes for the plugins the bridge listens to;
 * - its `callbackId` is of the runtime's own form, `-1` or digits;
 * - its `options` is an object or absent;
 * - it has no other key.
 * Everything else is dropped.
 */
public enum BridgeGate {
    /** The runtime's handler name, which the gate takes over. */
    public static let handler = "bridge"

    /** The calls the page makes: each plugin with its methods, by the names the page uses. */
    public static let calls: [String: Set<String>] = [
        "ConductorKey": ["create", "publicKey", "state", "bind", "sign", "ending", "remove"],
        "ConductorStore": ["put", "get", "list", "remove", "clear"],
        "CapacitorHttp": ["request"],
        "PushNotifications": ["checkPermissions", "requestPermissions", "register", "addListener", "removeListener"],
        "App": ["getLaunchUrl", "addListener", "removeListener"],
        "Browser": ["open"],
    ]

    /** The plugin names the page reaches. */
    public static var names: Set<String> { Set(calls.keys) }

    /** The keys a message may carry: those the runtime's handler reads. */
    public static let keys: Set<String> = ["type", "pluginId", "methodName", "callbackId", "options"]

    /** Who sent a message: its frame, the origin of that frame, and whether the view is the shell's own. */
    public struct Sender: Equatable, Sendable {
        public let mainFrame: Bool
        public let scheme: String
        public let host: String
        public let port: Int
        public let ownView: Bool
        public init(mainFrame: Bool, scheme: String, host: String, port: Int, ownView: Bool) {
            self.mainFrame = mainFrame
            self.scheme = scheme
            self.host = host
            self.port = port
            self.ownView = ownView
        }
    }

    /** Whether the sender is the shell's own page: the main frame of its own view at the app's origin (`app`, its local address). */
    public static func fromShell(_ sender: Sender, app: URL) -> Bool {
        sender.mainFrame && sender.ownView && sender.scheme.lowercased() == app.scheme?.lowercased()
            && sender.host.lowercased() == app.host?.lowercased() && sender.port == (app.port ?? 0)
    }

    /** `-1`, or 1 to 16 digits: the forms the runtime's page script gives a call. */
    public static func callbackForm(_ s: String) -> Bool {
        if s == "-1" { return true }
        return (1...16).contains(s.utf8.count) && s.utf8.allSatisfy { $0 >= 0x30 && $0 <= 0x39 }
    }

    /** Whether a message body goes on to the runtime, its sender aside. */
    public static func admits(_ body: Any) -> Bool {
        guard let d = body as? [String: Any], Set(d.keys).isSubset(of: keys), d["type"] as? String == "message",
              let plugin = d["pluginId"] as? String, let method = d["methodName"] as? String, calls[plugin]?.contains(method) == true,
              let callback = d["callbackId"] as? String, callbackForm(callback)
        else { return false }
        if let options = d["options"], !(options is [String: Any]) { return false }
        return true
    }

    /** The whole decision: the sender, then the body. */
    public static func admits(_ body: Any, from sender: Sender, app: URL) -> Bool {
        fromShell(sender, app: app) && admits(body)
    }
}

/**
 * The gate as the web view's handler under the runtime's name: a message the gate admits goes to the runtime's handler, any other is
 * dropped and told to `dropped` (the boot check's count). The handler the gate forwards to is the runtime's, which the runtime keeps.
 */
public final class BridgeGateHandler: NSObject, WKScriptMessageHandler {
    public weak var inner: (NSObject & WKScriptMessageHandler)?
    public weak var view: WKWebView?
    public let app: URL
    public var dropped: ((Any) -> Void)?
    /** The decision, BridgeGate's: a field so that a test can hand its own. */
    let decide: (Any, BridgeGate.Sender, URL) -> Bool

    public init(forwardingTo inner: NSObject & WKScriptMessageHandler, view: WKWebView?, app: URL,
                decide: @escaping (Any, BridgeGate.Sender, URL) -> Bool = BridgeGate.admits(_:from:app:)) {
        self.inner = inner
        self.view = view
        self.app = app
        self.decide = decide
    }

    public func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        let origin = message.frameInfo.securityOrigin
        let sender = BridgeGate.Sender(mainFrame: message.frameInfo.isMainFrame, scheme: origin.protocol, host: origin.host,
                                       port: origin.port, ownView: view != nil && message.webView === view)
        take(message.body, from: sender) { self.inner?.userContentController(userContentController, didReceive: message) }
    }

    /** The decision for one message: `forward` when admitted, the drop told otherwise. */
    func take(_ body: Any, from sender: BridgeGate.Sender, forward: () -> Void) {
        if decide(body, sender, app) {
            forward()
        } else {
            dropped?(body)
        }
    }
}
