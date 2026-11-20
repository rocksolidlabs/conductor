import ConductorKey
import ConductorStore
import Foundation

// The two plugins' calls as the bridge makes them (mobile/src/bridge.ts, `NativeKey` and `NativeStore`): their arguments decoded,
// their answers encoded, every refusal mapped to a code and its words. The app's plugins (ios/App/App/Plugins) are a few lines of
// glue around these: what a call does is here, tested without the app.

/** What a call answers the page: a value to resolve with, or a refusal's code and words to reject with. */
public enum CallAnswer: Equatable, Sendable {
    case resolve([String: CallValue])
    case reject(code: String, words: String)

    /** The value as the plugin resolves it: a plain dictionary. */
    public var resolved: [String: Any]? {
        if case .resolve(let v) = self { return v.mapValues(\.plain) }
        return nil
    }

    /**
     * The refusal as the plugin rejects with it: the words as the message, the code, and the words again as a field of their own,
     * so that the page's rejected error carries `code` and `words` (a refusal, as the bridge reads one).
     */
    public var rejection: (message: String, code: String, data: [String: Any])? {
        if case .reject(let code, let words) = self { return (words, code, ["words": words]) }
        return nil
    }
}

/** A value a call answers with: what JSON can carry. */
public indirect enum CallValue: Equatable, Sendable {
    case string(String)
    case int(Int)
    case null
    case list([CallValue])
    case object([String: CallValue])

    var plain: Any {
        switch self {
        case .string(let s): return s
        case .int(let n): return n
        case .null: return NSNull()
        case .list(let l): return l.map(\.plain)
        case .object(let o): return o.mapValues(\.plain)
        }
    }
}

/** A call's options as the page sent them, keyed by string; anything else is no options. */
public func callOptions(_ raw: [AnyHashable: Any]?) -> [String: Any] {
    var out: [String: Any] = [:]
    for (k, v) in raw ?? [:] {
        if let k = k as? String { out[k] = v }
    }
    return out
}

/** A date as the bridge reads one: ISO 8601 with milliseconds, in UTC. */
public func isoTime(_ d: Date) -> String {
    let f = ISO8601DateFormatter()
    f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    return f.string(from: d)
}

/** An ISO 8601 time the page sends, with or without fractional seconds. */
public func parseISOTime(_ s: String) -> Date? {
    let f = ISO8601DateFormatter()
    f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    if let d = f.date(from: s) { return d }
    f.formatOptions = [.withInternetDateTime]
    return f.date(from: s)
}

/** The refusal of a call whose arguments are not what it takes. */
func invalid(_ words: String) -> CallAnswer { .reject(code: "invalid_request", words: words) }

/** Any error as a refusal: a key's or a store's with its own code, anything else `native`. */
func refusal(_ error: Error) -> CallAnswer {
    if let r = error as? KeyRefusal { return .reject(code: r.code, words: r.words) }
    if let r = error as? StoreRefusal { return .reject(code: r.code, words: r.words) }
    if let r = error as? CallRefusal { return .reject(code: r.code, words: r.words) }
    return .reject(code: "native", words: "The phone could not do it: \(error.localizedDescription)")
}

/** The answer of a call the plugin could not make at all: its key or its store could not be opened on this phone. */
public func unopened(_ error: Error) -> CallAnswer {
    .reject(code: "native", words: "The phone could not open what it keeps: \(error.localizedDescription)")
}

/**
 * The key's calls: create({until}), publicKey(), state(), bind({id}), sign({request}), ending(), remove(). The key's start (a key
 * whose install is gone, an unbound one past its time, both removed) runs before the first call, and again before the next call
 * until it has once succeeded.
 */
public actor KeyCalls {
    public let key: ConductorKey
    private var started = false

    public init(key: ConductorKey) { self.key = key }

    /** The key on this phone: its Keychain item, the phone's own presence check, the install's mark, the packaged policy. */
    public static func device() throws -> KeyCalls {
        KeyCalls(key: ConductorKey(vault: KeychainVault(), presence: DevicePresence(), marker: try FileMarker.applicationSupport(), policy: ConductorKey.packagedPolicy()))
    }

    public func call(_ method: String, _ options: [String: Any] = [:]) async -> CallAnswer {
        do {
            if !started {
                try await key.start()
                started = true
            }
            switch method {
            case "create":
                guard let s = options["until"] as? String, let until = parseISOTime(s) else { return invalid("create takes until, an ISO 8601 time.") }
                return .resolve(["publicKey": .string(try await key.create(until: until))])
            case "publicKey":
                return .resolve(["publicKey": try await key.publicKey().map(CallValue.string) ?? .null])
            case "state":
                let s = try await key.state()
                var out: [String: CallValue] = ["state": .string(s.state.rawValue)]
                if let id = s.id { out["id"] = .string(id) }
                if let until = s.until { out["until"] = .string(isoTime(until)) }
                return .resolve(out)
            case "bind":
                guard let id = options["id"] as? String else { return invalid("bind takes id, a string.") }
                try await key.bind(id: id)
                return .resolve([:])
            case "sign":
                guard let request = options["request"] as? [String: Any] else { return invalid("sign takes request, an object.") }
                return .resolve(["signature": .string(try await key.sign(request: request))])
            case "ending":
                try await key.ending()
                return .resolve([:])
            case "remove":
                try await key.remove()
                return .resolve([:])
            default:
                return .reject(code: "unknown_call", words: "The key has no call \(method).")
            }
        } catch {
            return refusal(error)
        }
    }
}

/**
 * The store's calls: put({area, key, data}), get({area, key}), list({area}), remove({area, key}), clear(). The store's start (its
 * folders made and kept out of backups, a half-written record removed) runs before the first call, as the key's does.
 */
public actor StoreCalls {
    public let store: ConductorStore
    private var started = false

    public init(store: ConductorStore) { self.store = store }

    /** The store on this phone: under the app's Application Support, its files protected while the phone is locked. */
    public static func device() throws -> StoreCalls {
        StoreCalls(store: try ConductorStore.applicationSupport())
    }

    public func call(_ method: String, _ options: [String: Any] = [:]) async -> CallAnswer {
        let area = options["area"] as? String
        let key = options["key"] as? String
        do {
            if !started {
                do {
                    try await store.start()
                    started = true
                } catch {
                    // A clear is let past a start that fails: it removes what it can itself, and answers not_cleared.
                    if method != "clear" { throw error }
                }
            }
            switch method {
            case "put":
                guard let area, let key, let data = options["data"] as? String else { return invalid("put takes area, key and data, strings.") }
                try await store.put(area: area, key: key, data: data)
                return .resolve([:])
            case "get":
                guard let area, let key else { return invalid("get takes area and key, strings.") }
                guard let r = try await store.get(area: area, key: key) else { return .resolve(["data": .null]) }
                return .resolve(["data": .string(r.data), "savedAt": .string(isoTime(r.savedAt))])
            case "list":
                guard let area else { return invalid("list takes area, a string.") }
                let keys = try await store.list(area: area).map { CallValue.object(["key": .string($0.key), "savedAt": .string(isoTime($0.savedAt))]) }
                return .resolve(["keys": .list(keys)])
            case "remove":
                guard let area, let key else { return invalid("remove takes area and key, strings.") }
                try await store.remove(area: area, key: key)
                return .resolve([:])
            case "clear":
                try await store.clear()
                return .resolve([:])
            default:
                return .reject(code: "unknown_call", words: "The store has no call \(method).")
            }
        } catch {
            return refusal(error)
        }
    }
}

/** The calls each plugin offers the page, by the names the bridge calls them: the plugins declare exactly these. */
public enum PluginMethods {
    public static let key = ["create", "publicKey", "state", "bind", "sign", "ending", "remove"]
    public static let store = ["put", "get", "list", "remove", "clear"]
}
