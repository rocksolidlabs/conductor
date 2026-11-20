import Foundation

/**
 * The native runtime's own plugins the shell does not offer a page: `WebView` (it would load the shell from another folder, outside
 * its bundle and its policy, and keep that folder for every later start) and `CapacitorCookies` (the app's cookie jar). The app
 * registers a stand-in under each name, which answers every call of the runtime's own list refused.
 */
public enum RefusedPlugins {
    public static let webView = ["setServerAssetPath", "setServerBasePath", "getServerBasePath", "persistServerBasePath"]
    public static let cookies = ["getCookies", "setCookie", "deleteCookie", "clearCookies", "clearAllCookies"]

    /** The answer to every call of a refused plugin. */
    public static func answer(_ plugin: String, _ method: String) -> CallAnswer {
        .reject(code: "not_in_shell", words: "The shell does not offer \(plugin).\(method).")
    }
}
