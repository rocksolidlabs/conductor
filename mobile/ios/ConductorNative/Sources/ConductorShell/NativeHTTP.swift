import Foundation

// The native HTTP call (the bridge's `http.request`, Capacitor's `CapacitorHttp` by name): a request is sent only when its address is
// https on an origin the packaging named at build (`make mobile-sync MOBILE_NATIVE_ORIGINS=...`, written into the app's bundle and read
// once at start), and anything else is refused before anything is opened. It keeps no cookie, no cache and no credential, and follows
// no redirect. This repository's build names none: a shell with no provider makes no native HTTP call.

/** A call refused before it was made, or a reply that could not be handed over: a code and its words. */
public struct CallRefusal: Error, Equatable, Sendable {
    public let code: String
    public let words: String
    public init(code: String, words: String) {
        self.code = code
        self.words = words
    }
}

/** The origins a packaging names for the native HTTP call: none unless the bundle's file names them, each exactly. */
public struct NativeHTTPOrigins: Equatable, Sendable {
    struct Origin: Hashable, Sendable {
        let host: String
        let port: Int
    }

    let origins: Set<Origin>

    public static let none = NativeHTTPOrigins(origins: [])
    public var isEmpty: Bool { origins.isEmpty }

    /** Where `make mobile-sync` writes the list in the bundle (`mobile/www/shell/native-http.json`, copied into the app's `public`). */
    public static let bundlePath = "public/shell/native-http.json"

    /** The list the app's bundle holds, read once: none when absent, unreadable or not exactly what the sync writes. */
    public static func bundled() -> NativeHTTPOrigins {
        load(Bundle.main.bundleURL.appendingPathComponent(bundlePath))
    }

    public static func load(_ url: URL?) -> NativeHTTPOrigins {
        guard let url, let data = try? Data(contentsOf: url) else { return .none }
        return parse(data)
    }

    /** `{"origins": ["https://host[:port]", ...]}`: any other shape, or any entry that is not exactly an https origin, is none. */
    public static func parse(_ data: Data) -> NativeHTTPOrigins {
        guard let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any], object.count == 1,
              let list = object["origins"] as? [Any]
        else { return .none }
        var out = Set<Origin>()
        for entry in list {
            guard let s = entry as? String, let o = exactly(s) else { return .none }
            out.insert(o)
        }
        return NativeHTTPOrigins(origins: out)
    }

    /**
     * A host as the shell names one: ASCII labels of a-z, 0-9 and '-', none empty (so no trailing dot), and a name, never an address
     * (no host whose every label reads as a number, as an IPv4 address in any of its spellings does: `127.0.0.1`, `127.1`,
     * `2130706433`, `0x7f.0.0.1`), so the name judged is the name the session asks the system to resolve.
     */
    static func plainHost(_ h: String) -> Bool {
        let labels = h.split(separator: ".", omittingEmptySubsequences: false)
        guard !labels.isEmpty, labels.allSatisfy({ l in !l.isEmpty && l.utf8.allSatisfy { ($0 >= 0x61 && $0 <= 0x7A) || ($0 >= 0x30 && $0 <= 0x39) || $0 == 0x2D } })
        else { return false }
        let numeric = labels.allSatisfy { l in l.utf8.allSatisfy { $0 >= 0x30 && $0 <= 0x39 } || (l.hasPrefix("0x") && l.dropFirst(2).utf8.allSatisfy { ($0 >= 0x30 && $0 <= 0x39) || ($0 >= 0x61 && $0 <= 0x66) }) }
        return !numeric
    }

    /** s exactly an https origin as the sync writes one: `https://`, a plain lower-case host, and a port only when not 443. */
    static func exactly(_ s: String) -> Origin? {
        guard s.hasPrefix("https://") else { return nil }
        let rest = s.dropFirst("https://".count)
        let parts = rest.split(separator: ":", omittingEmptySubsequences: false)
        guard parts.count <= 2, plainHost(String(parts[0])) else { return nil }
        var port = 443
        if parts.count == 2 {
            let digits = String(parts[1])
            guard let p = Int(digits), String(p) == digits, p > 0, p <= 65535, p != 443 else { return nil }
            port = p
        }
        return Origin(host: String(parts[0]), port: port)
    }

    /**
     * The address a request to s is sent to: s's own path and query on its origin, when s is https on a named origin. A user part, a
     * host that is not plain (percent-encoded, not ASCII, a trailing dot), another scheme, another port or another host: refused.
     */
    public func admit(_ s: String) -> Result<URL, CallRefusal> {
        let refused = CallRefusal(code: "origin_refused", words: "Only an https address on an origin this shell's packaging names is sent: nothing was sent.")
        guard !isEmpty else {
            return .failure(CallRefusal(code: "origin_refused", words: "This shell's packaging names no origin for its native HTTP call: nothing was sent."))
        }
        guard let c = URLComponents(string: s), c.scheme?.lowercased() == "https", c.user == nil, c.password == nil,
              let raw = c.percentEncodedHost
        else { return .failure(refused) }
        let host = raw.lowercased()
        let port = c.port ?? 443
        guard NativeHTTPOrigins.plainHost(host), origins.contains(Origin(host: host, port: port)) else { return .failure(refused) }
        var out = URLComponents()
        out.scheme = "https"
        out.percentEncodedHost = host
        out.port = port == 443 ? nil : port
        out.percentEncodedPath = c.percentEncodedPath.isEmpty ? "/" : c.percentEncodedPath
        out.percentEncodedQuery = c.percentEncodedQuery
        guard let url = out.url else { return .failure(refused) }
        return .success(url)
    }
}

/** What sends an admitted request: the phone's own session, or a test's fake. */
public protocol HTTPTransport: Sendable {
    func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse)
}

/** A redirect is never followed: the reply that asks for it is the answer. */
final class NoRedirects: NSObject, URLSessionTaskDelegate {
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}

/**
 * The phone's session for the native HTTP call: ephemeral, with no cookie jar, no cache and no credential store (nothing of one
 * sign-in is sent under the next), following no redirect, and reading a reply's body only up to its bound.
 */
public final class SessionTransport: HTTPTransport, @unchecked Sendable {
    let session: URLSession
    let maxBody: Int

    public init(maxBody: Int = NativeHTTPCalls.maxBody, protocolClasses: [AnyClass]? = nil) {
        let c = URLSessionConfiguration.ephemeral
        c.httpCookieStorage = nil
        c.httpShouldSetCookies = false
        c.httpCookieAcceptPolicy = .never
        c.urlCache = nil
        c.requestCachePolicy = .reloadIgnoringLocalCacheData
        c.urlCredentialStorage = nil
        // The plugin's calls run one at a time: a reply that never ends cannot hold the queue past this.
        c.timeoutIntervalForResource = 120
        if let protocolClasses { c.protocolClasses = protocolClasses }
        session = URLSession(configuration: c, delegate: NoRedirects(), delegateQueue: nil)
        self.maxBody = maxBody
    }

    public func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        let (bytes, response) = try await session.bytes(for: request)
        guard let http = response as? HTTPURLResponse else { throw CallRefusal(code: "network", words: "The reply was not an HTTP reply.") }
        var data = Data()
        for try await b in bytes {
            data.append(b)
            if data.count > maxBody {
                bytes.task.cancel()
                throw CallRefusal(code: "too_large", words: "The reply is longer than \(maxBody) bytes.")
            }
        }
        return (data, http)
    }
}

/**
 * The native HTTP plugin's calls, by the names of Capacitor's own (`request`, and `get` to `delete` with their method): the address
 * whole (`url`), `method`, `headers` (strings) and `data` (a string); answered `{status, headers, url, data}`, the headers' names in
 * lower case and the body as its text (null when it is not UTF-8). `params` is refused: the address is taken whole. A header that
 * names where the request goes or how it is framed (`framing`, any `Proxy-` one), a name that is not an HTTP token and a value with a
 * control character are refused, so that what the gate judged (the address) is what the request is sent to.
 */
public struct NativeHTTPCalls: Sendable {
    public static let maxBody = 1024 * 1024
    public static let methods = ["request", "get", "post", "put", "patch", "delete"]
    static let verbs: Set<String> = ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"]
    /** The headers, by their names in lower case, that say where a request goes or how it is framed: the session's alone to set. */
    static let framing: Set<String> = ["host", "connection", "keep-alive", "content-length", "transfer-encoding", "te", "upgrade", "trailer"]

    /** Whether the page may set this header: its name an HTTP token and none of the framing ones, its value with no control character. */
    static func headerAllowed(_ name: String, _ value: String) -> Bool {
        let tokenSymbols = Set("!#$%&'*+-.^_`|~".utf8)
        guard !name.isEmpty, name.utf8.allSatisfy({ ($0 >= 0x30 && $0 <= 0x39) || ($0 | 0x20 >= 0x61 && $0 | 0x20 <= 0x7a) || tokenSymbols.contains($0) })
        else { return false }
        let lower = name.lowercased()
        if framing.contains(lower) || lower.hasPrefix("proxy-") { return false }
        return value.unicodeScalars.allSatisfy { $0.value >= 0x20 && $0.value != 0x7f }
    }

    public let origins: NativeHTTPOrigins
    let transport: HTTPTransport

    public init(origins: NativeHTTPOrigins, transport: HTTPTransport) {
        self.origins = origins
        self.transport = transport
    }

    /** The phone's: the origins the bundle names, the phone's session. */
    public static func device() -> NativeHTTPCalls {
        NativeHTTPCalls(origins: .bundled(), transport: SessionTransport())
    }

    public func call(_ method: String, _ options: [String: Any]) async -> CallAnswer {
        let verb: String
        switch method {
        case "request": verb = ((options["method"] as? String) ?? "GET").uppercased()
        case "get", "post", "put", "patch", "delete": verb = method.uppercased()
        default: return .reject(code: "unknown_call", words: "The native HTTP call has no call \(method).")
        }
        guard NativeHTTPCalls.verbs.contains(verb) else { return invalid("A request's method is one of GET, HEAD, POST, PUT, PATCH and DELETE.") }
        guard let address = options["url"] as? String else { return invalid("A request takes url, a string.") }
        if let params = options["params"], !(params is NSNull), (params as? [String: Any])?.isEmpty != true {
            return invalid("A request takes its address whole, with no params.")
        }
        var headers: [String: String] = [:]
        if let h = options["headers"], !(h is NSNull) {
            guard let dict = h as? [String: Any] else { return invalid("A request's headers are an object of strings.") }
            for (k, v) in dict {
                guard let s = v as? String else { return invalid("A request's headers are an object of strings.") }
                guard NativeHTTPCalls.headerAllowed(k, s) else {
                    return invalid("A request's header is not the page's to set, or is not a header: where a request goes and how it is framed are the session's.")
                }
                headers[k] = s
            }
        }
        var body: Data?
        if let d = options["data"], !(d is NSNull) {
            guard let s = d as? String else { return invalid("A request's data is a string.") }
            body = Data(s.utf8)
        }
        let url: URL
        switch origins.admit(address) {
        case .failure(let r): return .reject(code: r.code, words: r.words)
        case .success(let u): url = u
        }
        var request = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: 60)
        request.httpMethod = verb
        request.httpShouldHandleCookies = false
        for (k, v) in headers { request.setValue(v, forHTTPHeaderField: k) }
        request.httpBody = body
        do {
            let (data, response) = try await transport.send(request)
            var out: [String: CallValue] = [:]
            for (k, v) in response.allHeaderFields {
                out[String(describing: k).lowercased()] = .string(String(describing: v))
            }
            return .resolve([
                "status": .int(response.statusCode),
                "headers": .object(out),
                "url": .string(response.url?.absoluteString ?? url.absoluteString),
                "data": String(data: data, encoding: .utf8).map(CallValue.string) ?? .null,
            ])
        } catch let r as CallRefusal {
            return .reject(code: r.code, words: r.words)
        } catch {
            return .reject(code: "network", words: "The request could not be made: \(error.localizedDescription)")
        }
    }
}
