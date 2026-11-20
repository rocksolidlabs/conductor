import Capacitor
import ConductorShell
import Foundation

// The store plugin, `ConductorStore` to the page: each call handed to ConductorShell's StoreCalls, which decodes it, makes it of
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

    private let calls = Result { try StoreCalls.device() }

    @objc func put(_ call: CAPPluginCall) { make(call, "put") }
    @objc func get(_ call: CAPPluginCall) { make(call, "get") }
    @objc func list(_ call: CAPPluginCall) { make(call, "list") }
    @objc func remove(_ call: CAPPluginCall) { make(call, "remove") }
    @objc func clear(_ call: CAPPluginCall) { make(call, "clear") }

    private func make(_ call: CAPPluginCall, _ method: String) {
        let options = callOptions(call.options)
        switch calls {
        case .failure(let e): call.answer(unopened(e))
        case .success(let c): Task { call.answer(await c.call(method, options)) }
        }
    }
}
