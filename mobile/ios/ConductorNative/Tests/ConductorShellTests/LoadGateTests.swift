import Foundation
import Testing
import WebKit

@testable import ConductorShell

// The shell's decision over every load: the app's own page on to the runtime; a link to https or http handed to iOS in the
// foreground; a new window likewise; a frame only about:srcdoc and about:blank; nothing else loaded or handed on.

@Suite struct LoadGateTests {
    let app = URL(string: "capacitor://localhost")!

    func decide(_ frame: Bool?, _ link: Bool, _ url: String, foreground: Bool = true) -> LoadDecision {
        LoadGate.decide(frame: frame, linkActivated: link, url: URL(string: url)!, app: app, foreground: foreground)
    }

    @Test func theMainFrameLoadsOnlyTheAppsOwnPageAndHandsOnOnlyALinkToTheWeb() {
        for own in ["capacitor://localhost", "capacitor://localhost/", "capacitor://localhost/shell/open?h=x", "CAPACITOR://LOCALHOST/index.html"] {
            #expect(decide(true, false, own) == .forward, "\(own)")
        }
        #expect(decide(true, true, "https://example.com/a") == .handOff(URL(string: "https://example.com/a")!))
        #expect(decide(true, true, "http://192.0.2.1:8080/") == .handOff(URL(string: "http://192.0.2.1:8080/")!))
        // Not a link's load: a location change, a form, a redirect, a refresh away.
        #expect(decide(true, false, "https://example.com/") == .cancel)
        // Not in the foreground.
        #expect(decide(true, true, "https://example.com/", foreground: false) == .cancel)
        // No other scheme, link or not.
        for other in ["tel:+15555550100", "sms:+15555550100", "mailto:a@example.com", "itms-apps://apps.apple.com/app/id1", "file:///etc/hosts",
                      "data:text/html,x", "blob:capacitor://localhost/1", "javascript:alert(1)", "about:blank", "conductor-probe://x", "https:///nohost",
                      "capacitor://elsewhere/", "capacitor://localhost:8080/", "capacitor://user@localhost/", "facetime:a@example.com"] {
            #expect(decide(true, true, other) == .cancel, "\(other)")
        }
    }

    @Test func aNewWindowIsHandedOnOnlyForTheWebAndAFrameLoadsOnlyItsOwnDocument() {
        #expect(decide(nil, false, "https://id.example/device") == .handOff(URL(string: "https://id.example/device")!))
        #expect(decide(nil, true, "http://example.com/") == .handOff(URL(string: "http://example.com/")!))
        #expect(decide(nil, false, "https://id.example/device", foreground: false) == .cancel)
        for other in ["tel:1", "mailto:a@example.com", "conductor-probe://x", "capacitor://localhost/", "about:blank", "javascript:x", "data:,x"] {
            #expect(decide(nil, true, other) == .cancel, "\(other)")
        }
        #expect(decide(false, false, "about:srcdoc") == .allow)
        #expect(decide(false, false, "about:blank") == .allow)
        for other in ["https://example.com/", "capacitor://localhost/shell", "about:srcdoc#x", "data:text/html,x", "javascript:x", "blob:x"] {
            #expect(decide(false, true, other) == .cancel, "\(other)")
        }
    }

    @Test func theGateCarriesOutItsDecision() {
        final class Runtime: NSObject, WKNavigationDelegate {
            var asked = 0
            func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {}
        }
        let runtime = Runtime()
        let gate = NavigationGate(wrapping: runtime, app: app)
        var handed: [URL] = []
        var refused: [URL] = []
        var forwarded = 0
        var decided: [WKNavigationActionPolicy] = []
        gate.foreground = { true }
        gate.handOff = { handed.append($0) }
        gate.refused = { refused.append($0) }
        gate.take(frame: true, linkActivated: false, url: app, forward: { forwarded += 1 }, decided: { decided.append($0) })
        gate.take(frame: true, linkActivated: true, url: URL(string: "https://example.com/")!, forward: { forwarded += 1 }, decided: { decided.append($0) })
        gate.take(frame: true, linkActivated: false, url: URL(string: "conductor-probe://x")!, forward: { forwarded += 1 }, decided: { decided.append($0) })
        gate.take(frame: false, linkActivated: false, url: URL(string: "about:srcdoc")!, forward: { forwarded += 1 }, decided: { decided.append($0) })
        gate.take(frame: nil, linkActivated: false, url: nil, forward: { forwarded += 1 }, decided: { decided.append($0) })
        #expect(forwarded == 1)
        #expect(handed == [URL(string: "https://example.com/")!])
        #expect(refused == [URL(string: "conductor-probe://x")!])
        #expect(decided == [.cancel, .cancel, .allow, .cancel])
        // Its other calls go to the runtime's delegate; it answers the policy itself.
        #expect(gate.responds(to: #selector(WKNavigationDelegate.webView(_:didFinish:))))
        #expect(gate.forwardingTarget(for: #selector(WKNavigationDelegate.webView(_:didFinish:))) as? Runtime === runtime)
        let policy = NSSelectorFromString("webView:decidePolicyForNavigationAction:decisionHandler:")
        #expect(gate.responds(to: policy) && gate.forwardingTarget(for: policy) == nil)
    }
}
