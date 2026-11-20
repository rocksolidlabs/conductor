import Foundation

/**
 * The content rules of the shell's web view, judged on the address as WebKit writes it, compiled and added before the first load.
 * The native runtime's scheme handler reads the decoded path, so the rules admit a load of the app's own scheme only when its path
 * is spelled with the characters decoding leaves unchanged and the bundle's files and the shell's routes use (letters, digits, `.`,
 * `_`, `-`, `/`; a query or a fragment after it is not read as the path): a percent sign above all is refused, for every kind of
 * load and every host. And whatever its spelling, a path under the runtime's two prefixes is refused: `/_capacitor_file_` (any
 * absolute path the app can read) and `/_capacitor_http_interceptor_` (a proxy the runtime keeps off).
 */
public enum FilePrefix {
    public static let identifier = "conductor.shell.load-rules"
    public static let prefixes = ["/_capacitor_file_", "/_capacitor_http_interceptor_"]
    /** The characters a path of the app's own scheme may be spelled with. */
    public static let pathCharacters = "/A-Za-z0-9._-"

    /** Every load of the app's scheme blocked, then let through when its path is spelled with `pathCharacters` alone. */
    public static func spellingRules(app: URL) -> [[String: Any]] {
        let scheme = NSRegularExpression.escapedPattern(for: app.scheme ?? "capacitor")
        let host = NSRegularExpression.escapedPattern(for: app.host ?? "localhost")
        let letThrough = ["^\(scheme)://\(host)$", "^\(scheme)://\(host)[?#]", "^\(scheme)://\(host)/[\(pathCharacters)]*$",
                           "^\(scheme)://\(host)/[\(pathCharacters)]*[?#]"]
        return [["trigger": ["url-filter": "^\(scheme):"], "action": ["type": "block"]]]
            + letThrough.map { ["trigger": ["url-filter": $0], "action": ["type": "ignore-previous-rules"]] }
    }

    /** The runtime's two prefixes blocked, after the spelling's rules, so that no spelling lets them through. */
    public static var prefixRules: [[String: Any]] {
        prefixes.map { ["trigger": ["url-filter": NSRegularExpression.escapedPattern(for: $0)], "action": ["type": "block"]] }
    }

    /** The rules as the content-rule compiler takes them. */
    public static func rules(app: URL) -> String {
        encode(spellingRules(app: app) + prefixRules)
    }

    public static func encode(_ list: [[String: Any]]) -> String {
        let data = (try? JSONSerialization.data(withJSONObject: list, options: [.sortedKeys])) ?? Data("[]".utf8)
        return String(decoding: data, as: UTF8.self)
    }
}

/**
 * The shell's router for the runtime's scheme handler: a path with no extension answers the bundle's `index.html`, as the runtime's
 * own router does (a route of the single-page app); any other path answers its file only when the path, its `.` and `..` resolved,
 * stays inside the bundle's folder; anything else answers a file that does not exist.
 */
public enum BundleRoute {
    public static let nowhere = "/nonexistent/conductor-shell/refused"

    public static func route(base: String, path: String) -> String {
        if URL(fileURLWithPath: path).pathExtension.isEmpty { return base + "/index.html" }
        let root = URL(fileURLWithPath: base, isDirectory: true).standardizedFileURL.path
        let inside = root.hasSuffix("/") ? root : root + "/"
        let full = URL(fileURLWithPath: base + "/" + path).standardizedFileURL.path
        guard full.hasPrefix(inside) else { return nowhere }
        return full
    }
}
