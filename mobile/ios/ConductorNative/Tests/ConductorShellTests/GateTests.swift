import Foundation
import Testing
import WebKit

@testable import ConductorShell

// The gates at each way from the page into native code: the runtime's message handler forwards only a message to a plugin the shell
// means the page to reach, by its page name; the UI delegate answers every text-input prompt nil and hands none on.

@Suite struct GateTests {
    func message(_ pluginId: String, _ method: String = "state", type: String = "message", callback: Any = "1") -> [String: Any] {
        ["type": type, "pluginId": pluginId, "methodName": method, "options": [:], "callbackId": callback]
    }

    static let app = URL(string: "capacitor://localhost")!
    static let shell = BridgeGate.Sender(mainFrame: true, scheme: "capacitor", host: "localhost", port: 0, ownView: true)

    @Test func theBridgeGateAdmitsOnlyTheTablesPairsInTheirClosedForms() {
        for (plugin, methods) in BridgeGate.calls {
            for method in methods { #expect(BridgeGate.admits(message(plugin, method)), "\(plugin).\(method)") }
        }
        #expect(BridgeGate.names == ["ConductorKey", "ConductorStore", "CapacitorHttp", "App", "Browser", "PushNotifications"])
        // Class names, names not meant, unknown ones, near misses.
        for name in ["CAPHttpPlugin", "CAPWebViewPlugin", "CAPCookiesPlugin", "CAPConsolePlugin", "CAPSystemBarsPlugin", "ConductorKeyPlugin",
                     "ConductorStorePlugin", "ShellHttpPlugin", "RefusedWebViewPlugin", "WebView", "CapacitorCookies", "Console", "SystemBars",
                     "conductorkey", "ConductorKey ", "", "Unknown"] {
            #expect(!BridgeGate.admits(message(name)), "\(name)")
        }
        // A method outside the table on a plugin the page reaches, including the runtime's own of those plugins.
        for (plugin, method) in [("PushNotifications", "getDeliveredNotifications"), ("PushNotifications", "removeAllListeners"), ("App", "exitApp"),
                                 ("App", "getInfo"), ("Browser", "close"), ("CapacitorHttp", "get"), ("ConductorKey", "export"), ("ConductorStore", "state"),
                                 ("ConductorKey", "addListener")] {
            #expect(!BridgeGate.admits(message(plugin, method)), "\(plugin).\(method)")
        }
        // The callback id: the runtime's own forms only.
        for ok in ["-1", "0", "1", "123456789", "9000000001", "1234567890123456"] { #expect(BridgeGate.admits(message("ConductorKey", callback: ok)), "\(ok)") }
        for bad: Any in ["", "-2", "1'", "1');x=1;('", "1;", "a", "1 ", " 1", "12345678901234567", "１", 7, NSNull()] {
            #expect(!BridgeGate.admits(message("ConductorKey", callback: bad)), "\(bad)")
        }
        // Options an object or absent; no other key; other kinds and malformed bodies.
        var noOptions = message("ConductorStore", "clear")
        noOptions.removeValue(forKey: "options")
        #expect(BridgeGate.admits(noOptions))
        for options: Any in ["x", [1, 2], 7, NSNull()] {
            var m = message("ConductorStore", "clear")
            m["options"] = options
            #expect(!BridgeGate.admits(m), "\(options)")
        }
        var extra = message("ConductorKey")
        extra["save"] = true
        #expect(!BridgeGate.admits(extra))
        var noCallback = message("ConductorKey")
        noCallback.removeValue(forKey: "callbackId")
        #expect(!BridgeGate.admits(noCallback))
        #expect(!BridgeGate.admits(message("ConductorKey", type: "cordova")))
        #expect(!BridgeGate.admits(["type": "cordova", "service": "ConductorKey", "action": "state", "actionArgs": [], "callbackId": "1"]))
        #expect(!BridgeGate.admits(["type": "js.error", "error": ["message": "x"]]))
        #expect(!BridgeGate.admits(["pluginId": "ConductorKey", "methodName": "state", "callbackId": "1"]))
        #expect(!BridgeGate.admits("ConductorKey"))
        #expect(!BridgeGate.admits(["type": "message", "pluginId": 7, "methodName": "state", "callbackId": "1"]))
    }

    @Test func theBridgeGateAdmitsOnlyTheShellsOwnPage() {
        let app = GateTests.app
        #expect(BridgeGate.admits(message("ConductorKey"), from: GateTests.shell, app: app))
        #expect(BridgeGate.admits(message("ConductorKey"), from: .init(mainFrame: true, scheme: "Capacitor", host: "LOCALHOST", port: 0, ownView: true), app: app))
        for sender in [BridgeGate.Sender(mainFrame: false, scheme: "capacitor", host: "localhost", port: 0, ownView: true),
                       .init(mainFrame: true, scheme: "capacitor", host: "localhost", port: 0, ownView: false),
                       .init(mainFrame: true, scheme: "https", host: "example.com", port: 0, ownView: true),
                       .init(mainFrame: true, scheme: "capacitor", host: "elsewhere", port: 0, ownView: true),
                       .init(mainFrame: true, scheme: "capacitor", host: "localhost", port: 8080, ownView: true),
                       .init(mainFrame: false, scheme: "", host: "", port: 0, ownView: true)] {
            #expect(!BridgeGate.admits(message("ConductorKey"), from: sender, app: app), "\(sender)")
        }
    }

    @Test func theGateForwardsWhatItAdmitsAndTellsWhatItDrops() {
        final class Runtime: NSObject, WKScriptMessageHandler {
            func userContentController(_ c: WKUserContentController, didReceive m: WKScriptMessage) {}
        }
        let runtime = Runtime()
        let gate = BridgeGateHandler(forwardingTo: runtime, view: nil, app: GateTests.app)
        var forwarded = 0
        var dropped: [Any] = []
        gate.dropped = { dropped.append($0) }
        gate.take(message("ConductorStore", "list"), from: GateTests.shell) { forwarded += 1 }
        gate.take(message("ConductorStore", "list"), from: .init(mainFrame: false, scheme: "capacitor", host: "localhost", port: 0, ownView: true)) { forwarded += 1 }
        gate.take(message("CAPHttpPlugin", "request"), from: GateTests.shell) { forwarded += 1 }
        gate.take(["type": "cordova", "service": "Device"], from: GateTests.shell) { forwarded += 1 }
        #expect(forwarded == 1 && dropped.count == 3)
        #expect(gate.inner === runtime)
    }

    @Test func theViewAnswersNoPromptAndHandsNoneOn() {
        final class Runtime: NSObject, WKUIDelegate {
            var prompts = 0
            func webView(_ webView: WKWebView, runJavaScriptTextInputPanelWithPrompt prompt: String, defaultText: String?, initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping (String?) -> Void) {
                prompts += 1
                completionHandler("answered")
            }
        }
        let runtime = Runtime()
        let d = MediaCaptureRefused(wrapping: runtime)
        let sel = #selector(WKUIDelegate.webView(_:runJavaScriptTextInputPanelWithPrompt:defaultText:initiatedByFrame:completionHandler:))
        #expect(d.responds(to: sel) && d.forwardingTarget(for: sel) == nil)
        let stand = (NSObject(), NSObject())
        var answers: [String?] = []
        withExtendedLifetime(stand) {
            let view: WKWebView = standIn(stand.0)
            let frame: WKFrameInfo = standIn(stand.1)
            for p in [#"{"type":"CapacitorCookies.set","domain":"https://x.example","action":"a=b; Path=/"}"#, #"{"type":"CapacitorCookies.get"}"#,
                      #"{"type":"CapacitorCookies.isEnabled"}"#, #"{"type":"CapacitorHttp"}"#, "What is your name?"] {
                d.webView(view, runJavaScriptTextInputPanelWithPrompt: p, defaultText: nil, initiatedByFrame: frame) { answers.append($0) }
            }
        }
        #expect(answers.count == 5 && answers.allSatisfy { $0 == nil })
        #expect(runtime.prompts == 0)
    }
}

@Suite struct FilePrefixTests {
    @Test func theRulesBlockBothPrefixesForEveryLoad() throws {
        let list = try #require(try JSONSerialization.jsonObject(with: Data(FilePrefix.rules.utf8)) as? [[String: [String: String]]])
        #expect(list.count == 2)
        for (rule, prefix) in zip(list, FilePrefix.prefixes) {
            #expect(rule["action"] == ["type": "block"])
            #expect(rule["trigger"]?.keys.sorted() == ["url-filter"])
            let filter = try NSRegularExpression(pattern: try #require(rule["trigger"]?["url-filter"]))
            for url in ["capacitor://localhost\(prefix)/var/mobile/x.png", "capacitor://localhost\(prefix)"] {
                #expect(filter.firstMatch(in: url, range: NSRange(url.startIndex..., in: url)) != nil, "\(url)")
            }
            let other = "capacitor://localhost/_nuxt/entry.js"
            #expect(filter.firstMatch(in: other, range: NSRange(other.startIndex..., in: other)) == nil)
        }
        #expect(FilePrefix.prefixes.contains("/_capacitor_file_"))
    }
}
