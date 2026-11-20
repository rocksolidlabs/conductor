import Foundation
import Testing
import WebKit

@testable import ConductorShell

// The gates at each way from the page into native code: the runtime's message handler forwards only a message to a plugin the shell
// means the page to reach, by its page name; the UI delegate answers every text-input prompt nil and hands none on.

@Suite struct GateTests {
    func message(_ pluginId: String, type: String = "message") -> [String: Any] {
        ["type": type, "pluginId": pluginId, "methodName": "request", "options": [:], "callbackId": "1"]
    }

    @Test func theBridgeGateAdmitsOnlyThePageNamesOfThePluginsMeant() {
        for name in ["ConductorKey", "ConductorStore", "CapacitorHttp", "App", "Browser", "PushNotifications"] {
            #expect(BridgeGate.admits(message(name)), "\(name)")
        }
        // Class names (the runtime would make and register a fresh plugin), names not meant, unknown ones, near misses.
        for name in ["CAPHttpPlugin", "CAPWebViewPlugin", "CAPCookiesPlugin", "CAPConsolePlugin", "CAPSystemBarsPlugin", "ConductorKeyPlugin",
                     "ConductorStorePlugin", "ShellHttpPlugin", "RefusedWebViewPlugin", "WebView", "CapacitorCookies", "Console", "SystemBars",
                     "conductorkey", "ConductorKey ", "", "Unknown"] {
            #expect(!BridgeGate.admits(message(name)), "\(name)")
        }
        // Other kinds, and bodies that are not a message.
        #expect(!BridgeGate.admits(message("ConductorKey", type: "cordova")))
        #expect(!BridgeGate.admits(["type": "cordova", "service": "ConductorKey", "action": "state", "actionArgs": [], "callbackId": "1"]))
        #expect(!BridgeGate.admits(["type": "js.error", "error": ["message": "x"]]))
        #expect(!BridgeGate.admits(["pluginId": "ConductorKey", "methodName": "state"]))
        #expect(!BridgeGate.admits(["type": "message", "pluginId": "ConductorKey"]))
        #expect(!BridgeGate.admits("ConductorKey"))
        #expect(!BridgeGate.admits(["type": "message", "pluginId": 7, "methodName": "state"]))
    }

    @Test func theGateForwardsWhatItAdmitsAndTellsWhatItDrops() {
        final class Runtime: NSObject, WKScriptMessageHandler {
            func userContentController(_ c: WKUserContentController, didReceive m: WKScriptMessage) {}
        }
        let runtime = Runtime()
        let gate = BridgeGateHandler(forwardingTo: runtime)
        var forwarded = 0
        var dropped: [Any] = []
        gate.dropped = { dropped.append($0) }
        gate.take(message("ConductorStore")) { forwarded += 1 }
        gate.take(message("CAPHttpPlugin")) { forwarded += 1 }
        gate.take(["type": "cordova", "service": "Device"]) { forwarded += 1 }
        #expect(forwarded == 1 && dropped.count == 2)
        #expect(gate.inner === runtime)
    }

}
