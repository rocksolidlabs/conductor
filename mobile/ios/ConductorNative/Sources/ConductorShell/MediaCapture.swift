import Foundation
import WebKit

/**
 * The web view's UI delegate as the shell sets it: every media capture request (a microphone, a camera) answered `.deny`, so no
 * page in the shell can open either, and everything else handed to the delegate it wraps (the native runtime's own, which shows
 * the page's alerts and would grant a capture). The web view holds its UI delegate weakly: whoever sets this one keeps it.
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

    public override func responds(to selector: Selector!) -> Bool {
        super.responds(to: selector) || (inner?.responds(to: selector) ?? false)
    }

    public override func forwardingTarget(for selector: Selector!) -> Any? {
        if super.responds(to: selector) { return nil }
        return inner?.responds(to: selector) == true ? inner : nil
    }
}
