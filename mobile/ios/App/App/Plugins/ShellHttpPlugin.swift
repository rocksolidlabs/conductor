import Capacitor
import ConductorShell
import Foundation

// The native HTTP call, `CapacitorHttp` to the page as the runtime's own is (the runtime keeps one plugin a name, the last
// registered): each call handed to ConductorShell's NativeHTTPCalls, which sends it only when its address is https on an origin the
// packaging named at build, read once from the app's bundle; none in this repository's build.
@objc(ShellHttpPlugin)
public class ShellHttpPlugin: CAPPlugin, CAPBridgedPlugin {
    public let identifier = "ShellHttpPlugin"
    public let jsName = "CapacitorHttp"
    public let pluginMethods: [CAPPluginMethod] = NativeHTTPCalls.methods.map { CAPPluginMethod(name: $0, returnType: CAPPluginReturnPromise) }

    /** The calls one at a time, in the order the page made them, as the key's and the store's. */
    private let runner = CallRunner { NativeHTTPRunner(NativeHTTPCalls.device()) }

    @objc func request(_ call: CAPPluginCall) { make(call, "request") }
    @objc func get(_ call: CAPPluginCall) { make(call, "get") }
    @objc func post(_ call: CAPPluginCall) { make(call, "post") }
    @objc func put(_ call: CAPPluginCall) { make(call, "put") }
    @objc func patch(_ call: CAPPluginCall) { make(call, "patch") }
    @objc func delete(_ call: CAPPluginCall) { make(call, "delete") }

    private func make(_ call: CAPPluginCall, _ method: String) {
        runner.submit(method, callOptions(call.options)) { call.answer($0) }
    }
}
