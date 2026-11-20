import Foundation
import Testing
import WebKit

@testable import ConductorShell

// The gates at each way from the page into native code: the runtime's message handler forwards only a message to a plugin the shell
// means the page to reach, by its page name; the UI delegate answers every text-input prompt nil and hands none on.

@Suite struct GateTests {
    func message(_ pluginId: String, _ method: String = "state", type: String = "message", callback: Any = "1", options: Any = [String: Any]()) -> [String: Any] {
        ["type": type, "pluginId": pluginId, "methodName": method, "options": options, "callbackId": callback]
    }

    /** A pair of the table with the options the runtime's page script gives it: a listener call's event and id. */
    func call(_ plugin: String, _ method: String) -> [String: Any] {
        let event = BridgeGate.events[plugin]?.sorted().first ?? ""
        switch method {
        case "addListener": return message(plugin, method, options: ["eventName": event])
        case "removeListener": return message(plugin, method, callback: "-1", options: ["eventName": event, "callbackId": "12"])
        default: return message(plugin, method)
        }
    }

    static let app = URL(string: "capacitor://localhost")!
    static let shell = BridgeGate.Sender(mainFrame: true, scheme: "capacitor", host: "localhost", port: 0, ownView: true)

    @Test func theBridgeGateAdmitsOnlyTheTablesPairsInTheirClosedForms() {
        for (plugin, methods) in BridgeGate.calls {
            for method in methods { #expect(BridgeGate.admits(call(plugin, method)), "\(plugin).\(method)") }
        }
        // The listener calls: exactly an event the bridge listens for on that plugin, and a removal's listener id in the runtime's form.
        for (plugin, events) in BridgeGate.events {
            for event in events {
                #expect(BridgeGate.admits(message(plugin, "addListener", options: ["eventName": event])), "\(plugin) \(event)")
                #expect(BridgeGate.admits(message(plugin, "removeListener", options: ["eventName": event, "callbackId": "7"])), "\(plugin) \(event)")
            }
        }
        #expect(BridgeGate.events == ["PushNotifications": ["registration", "pushNotificationActionPerformed"], "App": ["appUrlOpen"]])
        for options: Any in [[String: Any](), ["eventName": 7], ["eventName": "appStateChange"], ["eventName": "registration"], ["eventName": "appUrlOpen", "x": 1],
                             ["eventName": "appUrlOpen", "callbackId": "7"], NSNull(), "appUrlOpen"] {
            #expect(!BridgeGate.admits(message("App", "addListener", options: options)), "\(options)")
        }
        var bare = message("App", "addListener")
        bare.removeValue(forKey: "options")
        #expect(!BridgeGate.admits(bare))
        for options: Any in [["eventName": "appUrlOpen"], ["eventName": "appUrlOpen", "callbackId": "1'"], ["eventName": "appUrlOpen", "callbackId": 7],
                             ["eventName": "registration", "callbackId": "7"], ["eventName": "appUrlOpen", "callbackId": "7", "x": 1], [String: Any]()] {
            #expect(!BridgeGate.admits(message("App", "removeListener", options: options)), "\(options)")
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

/**
 * Whether the content rules block a load of url, as WebKit applies them: in order, a block set and an ignore-previous-rules clearing
 * it; a filter case-insensitive unless its rule says otherwise.
 */
func blocked(_ url: String, by rules: String) throws -> Bool {
    let list = try #require(try JSONSerialization.jsonObject(with: Data(rules.utf8)) as? [[String: [String: Any]]])
    var block = false
    for rule in list {
        let sensitive = rule["trigger"]?["url-filter-is-case-sensitive"] as? Bool ?? false
        let filter = try NSRegularExpression(pattern: try #require(rule["trigger"]?["url-filter"] as? String), options: sensitive ? [] : [.caseInsensitive])
        guard filter.firstMatch(in: url, range: NSRange(url.startIndex..., in: url)) != nil else { continue }
        switch rule["action"]?["type"] as? String {
        case "block": block = true
        case "ignore-previous-rules": block = false
        default: Issue.record("an action the rules do not use: \(String(describing: rule["action"]))")
        }
    }
    return block
}

@Suite struct LoadRulesTests {
    let rules = FilePrefix.rules(app: URL(string: "capacitor://localhost")!)

    @Test func theRulesAdmitOnlyTheBundlesSpellingAndNeverThePrefixes() throws {
        for ok in ["capacitor://localhost", "capacitor://localhost/", "capacitor://localhost/index.html", "capacitor://localhost/_nuxt/DBSZVbF0.js",
                   "capacitor://localhost/_fonts/Inter-Regular.woff2", "capacitor://localhost/shell/decisions/dr-1", "capacitor://localhost/shell/open?h=a%2Fb",
                   "capacitor://localhost/shell/decisions/b3JnL3JlcG8jMTI", "capacitor://localhost/sponsor/rocksolidlabs-mark.png#x",
                   "https://example.com/%5Fanything"] {
            #expect(try !blocked(ok, by: rules), "\(ok)")
        }
        for bad in ["capacitor://localhost/_capacitor_file_/private/var/x.png", "capacitor://localhost/%5Fcapacitor_file_/private/var/x.png",
                    "capacitor://localhost/_capacitor%5Ffile_/x.png", "capacitor://localhost/_capacitor_http_interceptor_?u=https://x",
                    "capacitor://localhost/_nuxt/..%2F..%2Fx.png", "capacitor://localhost/a%2Fb.png", "capacitor://localhost/a b.png",
                    "capacitor://localhost/é.png", "capacitor://localhost/a~b.png", "capacitor://localhost/a@b.png", "capacitor://localhost/a;b.png",
                    "capacitor://elsewhere/index.html", "capacitor://localhost.evil/index.html", "capacitor://localhost:8080/index.html",
                    "capacitor://user@localhost/index.html", "capacitor:/index.html", "capacitor://localhost/_capacitor_file_?x",
                    "capacitor://LOCALHOST/index.html", "capacitor://Localhost/", "capacitor://local%68ost/index.html", "CAPACITOR://LOCALHOST/index.html",
                    "capacitor://localhost/shell/decisions/org%2Frepo%2312"] {
            #expect(try blocked(bad, by: rules), "\(bad)")
        }
    }

    @Test func theBundlesOwnFilesAreServedAndNothingOutsideIt() {
        let base = "/var/app/App.app/public"
        #expect(BundleRoute.route(base: base, path: "/index.html") == "/var/app/App.app/public/index.html")
        #expect(BundleRoute.route(base: base, path: "/_nuxt/DBSZVbF0.js") == "/var/app/App.app/public/_nuxt/DBSZVbF0.js")
        #expect(BundleRoute.route(base: base, path: "/sponsor/../sponsor/mark.png") == "/var/app/App.app/public/sponsor/mark.png")
        // A path with no extension answers index.html, as the runtime's own router does.
        for route in ["", "/", "/shell/open", "/..", "/../../etc/hosts"] {
            #expect(BundleRoute.route(base: base, path: route) == base + "/index.html", "\(route)")
        }
        // A climb out of the bundle, by `..` at once or after a real folder, or to the folder beside it: a file that does not exist.
        for climb in ["/../x.png", "/../../../private/var/mobile/x.png", "/sponsor/../../x.png", "/_nuxt/../../App.app/x.png",
                      "/./../public2/x.png", "/../public/../../x.png"] {
            #expect(BundleRoute.route(base: base, path: climb) == BundleRoute.nowhere, "\(climb)")
        }
        // An absolute path is read under the bundle's folder, never as the file system's.
        #expect(BundleRoute.route(base: base, path: "/private/var/mobile/x.png") == "/var/app/App.app/public/private/var/mobile/x.png")
        #expect(BundleRoute.route(base: base + "/", path: "/index.html") == "/var/app/App.app/public/index.html")
    }
}
