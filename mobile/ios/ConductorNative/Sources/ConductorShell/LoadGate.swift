import Foundation
import WebKit

/** What the shell does with a load the web view asks about. */
public enum LoadDecision: Equatable, Sendable {
    /** The app's own page in the main frame: on to the native runtime's own handling. */
    case forward
    /** A frame's `about:srcdoc` or `about:blank`. */
    case allow
    /** Never loaded, handed nowhere. */
    case cancel
    /** Never loaded in the view; handed to iOS, which opens it as for a link tapped in Mail (the person's browser, or the app iOS ties to it). */
    case handOff(URL)
}

/**
 * The shell's decision over every load of its web view, ahead of the native runtime's own rule:
 * - the main frame: the app's own address (`own`: the runtime's own test of it, or narrower) goes on to the runtime; a link the person activated (WebKit's link type, which a
 *   script's own click on a link also gets) to an `https` or `http` address, while the app is in the foreground, is handed to iOS;
 *   anything else is cancelled (a location change, a form, a redirect, a refresh away, every other scheme);
 * - a new window (a link with a target, or a script's `window.open`, which the sign-in page uses for the provider's page): an
 *   `https` or `http` address, while the app is in the foreground, is handed to iOS; anything else gets no window;
 * - a frame: `about:srcdoc` and `about:blank` load; anything else is cancelled.
 * No other scheme is ever handed on (`tel:`, `sms:`, `mailto:`, `itms-apps:`, `file:`, `data:`, `blob:`, `javascript:`, `about:`, a
 * custom one).
 */
public enum LoadGate {
    public static let handed: Set<String> = ["https", "http"]

    /**
     * `frame` is the target frame's kind: `true` the main frame, `false` a frame, `nil` a new window. `app` is the app's local address.
     */
    public static func decide(frame: Bool?, linkActivated: Bool, url: URL, app: URL, foreground: Bool) -> LoadDecision {
        let scheme = url.scheme?.lowercased() ?? ""
        let out = handed.contains(scheme) && url.host?.isEmpty == false && foreground
        switch frame {
        case .some(true):
            if own(url, app: app) { return .forward }
            return linkActivated && out ? .handOff(url) : .cancel
        case .some(false):
            return ["about:srcdoc", "about:blank"].contains(url.absoluteString.lowercased()) ? .allow : .cancel
        case .none:
            return out ? .handOff(url) : .cancel
        }
    }

    /**
     * Whether a load is the app's own page, read as the runtime reads it: the runtime asks whether the address, as a string, begins
     * with its local address, and hands a top-level load that does not to iOS. So the gate takes the string itself too, case as
     * written and nothing decoded, and narrower: the local address exactly, or it followed by `/`, `?` or `#`. A host in another
     * case, with an escape, a port, a user part or a longer name is not the app's.
     */
    public static func own(_ url: URL, app: URL) -> Bool {
        var a = Array(app.absoluteString.utf8)
        if a.last == UInt8(ascii: "/") { a.removeLast() }
        let s = Array(url.absoluteString.utf8)
        guard s.count >= a.count, Array(s[..<a.count]) == a else { return false }
        guard s.count > a.count else { return true }
        return [UInt8(ascii: "/"), UInt8(ascii: "?"), UInt8(ascii: "#")].contains(s[a.count])
    }
}

/**
 * The web view's navigation delegate as the shell sets it: every navigation action decided by LoadGate, the app's own page handed on
 * to the delegate it wraps (the native runtime's own), whose other calls (a load started, finished, failed) it hands on unchanged.
 * `handOff` opens an address in iOS; `refused` is told each cancelled load (the boot check's count); `foreground` says whether the
 * app is in the foreground.
 */
public final class NavigationGate: NSObject, WKNavigationDelegate {
    public weak var inner: (NSObject & WKNavigationDelegate)?
    public let app: URL
    public var foreground: () -> Bool = { false }
    public var handOff: (URL) -> Void = { _ in }
    public var refused: ((URL) -> Void)?

    public init(wrapping inner: (NSObject & WKNavigationDelegate)?, app: URL) {
        self.inner = inner
        self.app = app
    }

    /** The decision for one action, carried out; `forward` hands the action to the runtime's own rule. */
    func take(frame: Bool?, linkActivated: Bool, url: URL?, forward: () -> Void, decided: (WKNavigationActionPolicy) -> Void) {
        guard let url else { return decided(.cancel) }
        switch LoadGate.decide(frame: frame, linkActivated: linkActivated, url: url, app: app, foreground: foreground()) {
        case .forward: forward()
        case .allow: decided(.allow)
        case .cancel:
            refused?(url)
            decided(.cancel)
        case .handOff(let u):
            handOff(u)
            decided(.cancel)
        }
    }

    public func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction, decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        let frame = navigationAction.targetFrame.map { $0.isMainFrame }
        take(frame: frame, linkActivated: navigationAction.navigationType == .linkActivated, url: navigationAction.request.url,
             forward: { if let inner = self.inner { inner.webView?(webView, decidePolicyFor: navigationAction, decisionHandler: decisionHandler) } else { decisionHandler(.cancel) } },
             decided: decisionHandler)
    }

    /** A new window the page asks for (the UI delegate's createWebView): decided as one, never a window in the shell. */
    public func newWindow(_ navigationAction: WKNavigationAction) {
        take(frame: nil, linkActivated: navigationAction.navigationType == .linkActivated, url: navigationAction.request.url, forward: {}, decided: { _ in })
    }

    public override func responds(to selector: Selector!) -> Bool {
        super.responds(to: selector) || (inner?.responds(to: selector) ?? false)
    }

    public override func forwardingTarget(for selector: Selector!) -> Any? {
        if super.responds(to: selector) { return nil }
        return inner?.responds(to: selector) == true ? inner : nil
    }
}
