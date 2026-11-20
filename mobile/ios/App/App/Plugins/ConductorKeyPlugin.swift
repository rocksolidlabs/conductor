import Capacitor
import ConductorShell
import Foundation

// The key plugin, `ConductorKey` to the page: each call handed, in the order it arrives, to ConductorShell's KeyCalls, which decodes it, makes it of the key
// and answers it; nothing else is here.
@objc(ConductorKeyPlugin)
public class ConductorKeyPlugin: CAPPlugin, CAPBridgedPlugin {
    public let identifier = "ConductorKeyPlugin"
    public let jsName = "ConductorKey"
    public let pluginMethods: [CAPPluginMethod] = [
        CAPPluginMethod(name: "create", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "publicKey", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "state", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "bind", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "sign", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "ending", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "remove", returnType: CAPPluginReturnPromise),
    ]

    /** The calls one at a time, in the order the page made them; the KeyCalls opened at the first. */
    private let runner = CallRunner { try KeyCalls.device() }

    @objc func create(_ call: CAPPluginCall) { make(call, "create") }
    @objc func publicKey(_ call: CAPPluginCall) { make(call, "publicKey") }
    @objc func state(_ call: CAPPluginCall) { make(call, "state") }
    @objc func bind(_ call: CAPPluginCall) { make(call, "bind") }
    @objc func sign(_ call: CAPPluginCall) { make(call, "sign") }
    @objc func ending(_ call: CAPPluginCall) { make(call, "ending") }
    @objc func remove(_ call: CAPPluginCall) { make(call, "remove") }

    private func make(_ call: CAPPluginCall, _ method: String) {
        runner.submit(method, callOptions(call.options)) { call.answer($0) }
    }
}
