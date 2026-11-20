import Foundation

/**
 * The native runtime's scheme handler serves, under `/_capacitor_file_`, any absolute path the app can read, and under
 * `/_capacitor_http_interceptor_` a proxy the shell keeps off (the runtime refuses it with its native HTTP switched off). The
 * shell's web view carries content rules that block both prefixes for every kind of load, added before its first load: the page
 * loads only once they are compiled and in place.
 */
public enum FilePrefix {
    public static let identifier = "conductor.shell.runtime-prefixes"
    public static let prefixes = ["/_capacitor_file_", "/_capacitor_http_interceptor_"]

    /** The content rules: one block a prefix, every resource type, any scheme and host. */
    public static var rules: String {
        let list = prefixes.map { ["trigger": ["url-filter": NSRegularExpression.escapedPattern(for: $0)], "action": ["type": "block"]] }
        let data = (try? JSONSerialization.data(withJSONObject: list, options: [.sortedKeys])) ?? Data("[]".utf8)
        return String(decoding: data, as: UTF8.self)
    }
}
