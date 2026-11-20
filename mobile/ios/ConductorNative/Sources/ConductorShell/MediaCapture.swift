import Foundation
import WebKit

/**
 * The web view's UI delegate as the shell sets it: every media capture request (a microphone, a camera) answered `.deny`, so no
 * page in the shell can open either; every text-input prompt answered nil and handed to no one (the native runtime reads a prompt
 * whose text is JSON as a native call, its cookies code among them; the shell's pages use no prompt); every new window decided by
 * the shell's load gate and never opened in the shell; and everything else (an alert, a confirm) handed to the delegate it wraps,
 * the native runtime's own. The web view holds its UI delegate weakly:
 * whoever sets this one keeps it.
 */
public final class MediaCaptureRefused: NSObject, WKUIDelegate {
    public weak var inner: (NSObject & WKUIDelegate)?

    public init(wrapping inner: (NSObject & WKUIDelegate)?) {
        self.inner = inner
    }

    /** Every media capture request: denied. */
    public static let decision: WKPermissionDecision = .deny

    public func webView(
        _ webView: WKWebView,
        requestMediaCapturePermissionFor origin: WKSecurityOrigin,
        initiatedByFrame frame: WKFrameInfo,
        type: WKMediaCaptureType,
        decisionHandler: @escaping (WKPermissionDecision) -> Void
    ) {
        decisionHandler(Self.decision)
    }

    /** A new window the page asks for: decided by the shell's load gate (`newWindow`), never a window in the shell. */
    public var newWindow: ((WKNavigationAction) -> Void)?

    public func webView(
        _ webView: WKWebView,
        createWebViewWith configuration: WKWebViewConfiguration,
        for navigationAction: WKNavigationAction,
        windowFeatures: WKWindowFeatures
    ) -> WKWebView? {
        newWindow?(navigationAction)
        return nil
    }

    /** Every text-input prompt: no answer, and never handed on. */
    public func webView(
        _ webView: WKWebView,
        runJavaScriptTextInputPanelWithPrompt prompt: String,
        defaultText: String?,
        initiatedByFrame frame: WKFrameInfo,
        completionHandler: @escaping (String?) -> Void
    ) {
        completionHandler(nil)
    }

    public override func responds(to selector: Selector!) -> Bool {
        super.responds(to: selector) || (inner?.responds(to: selector) ?? false)
    }

    public override func forwardingTarget(for selector: Selector!) -> Any? {
        if super.responds(to: selector) { return nil }
        return inner?.responds(to: selector) == true ? inner : nil
    }
}
