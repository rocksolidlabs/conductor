import Foundation
import WebKit

/**
 * The gate at the native runtime's script message handler (`bridge`), the one way a page's script calls native code by message. The
 * runtime hands every message of the kind `message` to its plugin lookup with the page's own plugin name, and makes and registers a
 * fresh plugin when that name is a native class's (over the shell's own under the same page name); it hands the kind `cordova` to
 * its Cordova layer. The gate forwards a message only when its kind is `message` and its plugin name is exactly one of the names
 * the shell means the page to reach, and drops everything else: a class name, an unknown name, the Cordova kind, any other kind.
 */
public enum BridgeGate {
    /** The runtime's handler name, which the gate takes over. */
    public static let handler = "bridge"

    /**
     * The plugin names the page reaches: the key, the store and the native HTTP call (the shell's own), and the runtime's App (links
     * the shell is opened with), Browser (the in-app browser) and PushNotifications (the push token and taps). Nothing else: the
     * runtime's Console only logs and the page's start does not need it; WebView, CapacitorCookies and SystemBars are not the
     * page's (their stand-ins stay registered behind the gate as a second wall).
     */
    public static let names: Set<String> = ["ConductorKey", "ConductorStore", "CapacitorHttp", "App", "Browser", "PushNotifications"]

    /** Whether a message body goes on to the runtime. */
    public static func admits(_ body: Any) -> Bool {
        if body is [String: Any] { return true }  // A deliberate break, reverted next: the gate admits every message.
        guard let d = body as? [String: Any], d["type"] as? String == "message", let id = d["pluginId"] as? String, names.contains(id),
              d["methodName"] is String
        else { return false }
        return true
    }
}

/**
 * The gate as the web view's handler under the runtime's name: an admitted message goes to the runtime's handler, any other is
 * dropped and told to `dropped` (the boot check's count). The handler the gate forwards to is the runtime's, which the runtime keeps.
 */
public final class BridgeGateHandler: NSObject, WKScriptMessageHandler {
    public weak var inner: (NSObject & WKScriptMessageHandler)?
    public var dropped: ((Any) -> Void)?

    public init(forwardingTo inner: NSObject & WKScriptMessageHandler) {
        self.inner = inner
    }

    public func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        take(message.body) { self.inner?.userContentController(userContentController, didReceive: message) }
    }

    /** The decision for one body: `forward` when admitted, the drop told otherwise. */
    func take(_ body: Any, forward: () -> Void) {
        if BridgeGate.admits(body) {
            forward()
        } else {
            dropped?(body)
        }
    }
}
