import Capacitor
import ConductorShell
import Foundation

// The native runtime's own WebView and CapacitorCookies plugins, replaced by their names (the runtime keeps one plugin a name, the
// last registered) with stand-ins that refuse every call of the runtime's own list: no page in the shell can load it from another
// folder or keep that folder for its next start, or reach the app's cookie jar.

@objc(RefusedWebViewPlugin)
public class RefusedWebViewPlugin: CAPPlugin, CAPBridgedPlugin {
    public let identifier = "RefusedWebViewPlugin"
    public let jsName = "WebView"
    public let pluginMethods: [CAPPluginMethod] = RefusedPlugins.webView.map { CAPPluginMethod(name: $0, returnType: CAPPluginReturnPromise) }

    @objc func setServerAssetPath(_ call: CAPPluginCall) { call.answer(RefusedPlugins.answer(jsName, "setServerAssetPath")) }
    @objc func setServerBasePath(_ call: CAPPluginCall) { call.answer(RefusedPlugins.answer(jsName, "setServerBasePath")) }
    @objc func getServerBasePath(_ call: CAPPluginCall) { call.answer(RefusedPlugins.answer(jsName, "getServerBasePath")) }
    @objc func persistServerBasePath(_ call: CAPPluginCall) { call.answer(RefusedPlugins.answer(jsName, "persistServerBasePath")) }
}

@objc(RefusedCookiesPlugin)
public class RefusedCookiesPlugin: CAPPlugin, CAPBridgedPlugin {
    public let identifier = "RefusedCookiesPlugin"
    public let jsName = "CapacitorCookies"
    public let pluginMethods: [CAPPluginMethod] = RefusedPlugins.cookies.map { CAPPluginMethod(name: $0, returnType: CAPPluginReturnPromise) }

    @objc func getCookies(_ call: CAPPluginCall) { call.answer(RefusedPlugins.answer(jsName, "getCookies")) }
    @objc func setCookie(_ call: CAPPluginCall) { call.answer(RefusedPlugins.answer(jsName, "setCookie")) }
    @objc func deleteCookie(_ call: CAPPluginCall) { call.answer(RefusedPlugins.answer(jsName, "deleteCookie")) }
    @objc func clearCookies(_ call: CAPPluginCall) { call.answer(RefusedPlugins.answer(jsName, "clearCookies")) }
    @objc func clearAllCookies(_ call: CAPPluginCall) { call.answer(RefusedPlugins.answer(jsName, "clearAllCookies")) }
}
