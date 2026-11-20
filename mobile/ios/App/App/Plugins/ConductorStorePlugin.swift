import Capacitor
import ConductorShell
import Foundation

// The store plugin, `ConductorStore` to the page: each call handed, in the order it arrives, to ConductorShell's StoreCalls, which decodes it, makes it of
// the store and answers it; nothing else is here.
@objc(ConductorStorePlugin)
public class ConductorStorePlugin: CAPPlugin, CAPBridgedPlugin {
    public let identifier = "ConductorStorePlugin"
    public let jsName = "ConductorStore"
    public let pluginMethods: [CAPPluginMethod] = [
        CAPPluginMethod(name: "put", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "get", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "list", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "remove", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "clear", returnType: CAPPluginReturnPromise),
    ]

    /** The calls one at a time, in the order the page made them; the StoreCalls opened at the first. */
    private let runner = CallRunner { try StoreCalls.device() }

    @objc func put(_ call: CAPPluginCall) { make(call, "put") }
    @objc func get(_ call: CAPPluginCall) { make(call, "get") }
    @objc func list(_ call: CAPPluginCall) { make(call, "list") }
    @objc func remove(_ call: CAPPluginCall) { make(call, "remove") }
    @objc func clear(_ call: CAPPluginCall) { make(call, "clear") }

    private func make(_ call: CAPPluginCall, _ method: String) {
        runner.submit(method, callOptions(call.options)) { call.answer($0) }
    }
}
