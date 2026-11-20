import Foundation
import Testing

@testable import ConductorShell

// The native HTTP call: what the packaging names, read exactly; the admit rule over every kind of address that is not a named origin;
// the call's shape over a fake transport; and the phone's own session over a stub protocol (no redirect followed, no cookie sent or
// kept, the body read only to its bound).

/** A transport that records what reaches it and answers 200 with a body. */
final class FakeTransport: HTTPTransport, @unchecked Sendable {
    let lock = NSLock()
    var sent: [URLRequest] = []
    func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        lock.withLock { sent.append(request) }
        let r = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "text/plain", "X-Relay": "one"])!
        return (Data("the reply".utf8), r)
    }
    var count: Int { lock.withLock { sent.count } }
}

func origins(_ list: [String]) -> NativeHTTPOrigins {
    NativeHTTPOrigins.parse(try! JSONSerialization.data(withJSONObject: ["origins": list]))
}

func refusedCode(_ r: Result<URL, CallRefusal>) -> String? {
    if case .failure(let e) = r { return e.code }
    return nil
}

/**
 * The stub server of the session test: `relay.shell.test` answers `/redirect` by sending the request to `elsewhere.shell.test`,
 * `/big` with a body one byte past the bound, `/cookie` with a cookie, and anything else 200; every request it is handed is recorded.
 */
final class StubServer: URLProtocol, @unchecked Sendable {
    static let lock = NSLock()
    static var seen: [URLRequest] = []
    static var requests: [URLRequest] { lock.withLock { seen } }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        StubServer.lock.withLock { StubServer.seen.append(request) }
        let url = request.url!
        func reply(_ status: Int, _ headers: [String: String], _ body: Data) {
            client?.urlProtocol(self, didReceive: HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1", headerFields: headers)!, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: body)
            client?.urlProtocolDidFinishLoading(self)
        }
        switch url.path {
        case "/redirect":
            let to = URL(string: "https://elsewhere.shell.test/took")!
            let response = HTTPURLResponse(url: url, statusCode: 302, httpVersion: "HTTP/1.1", headerFields: ["Location": to.absoluteString])!
            client?.urlProtocol(self, wasRedirectedTo: URLRequest(url: to), redirectResponse: response)
            reply(302, ["Location": to.absoluteString], Data())
        case "/big":
            reply(200, ["Content-Type": "text/plain"], Data(repeating: 0x61, count: 64 + 1))
        case "/cookie":
            reply(200, ["Set-Cookie": "kept=yes; Path=/; Secure"], Data("ok".utf8))
        default:
            reply(200, ["Content-Type": "text/plain"], Data("ok".utf8))
        }
    }

    override func stopLoading() {}
}

@Suite struct NativeHTTPTests {
    @Test func readsOnlyExactlyTheOriginsTheSyncWrites() throws {
        #expect(!origins(["https://relay.shell.test", "https://relay.shell.test:8443"]).isEmpty)
        for bad in [["http://relay.shell.test"], ["https://relay.shell.test/"], ["https://relay.shell.test."], ["https://Relay.shell.test"],
                    ["https://user@relay.shell.test"], ["https://relay.shell.test:443"], ["https://relay.shell.test:0"], ["https://*.shell.test"],
                    ["https://relay.shell.test", 7] as [Any], ["https://relay.shell.test", "https://relay.shell.test/x"]] {
            #expect(NativeHTTPOrigins.parse(try JSONSerialization.data(withJSONObject: ["origins": bad])).isEmpty, "\(bad)")
        }
        #expect(NativeHTTPOrigins.parse(Data("not json".utf8)).isEmpty)
        #expect(NativeHTTPOrigins.parse(try JSONSerialization.data(withJSONObject: ["origins": ["https://relay.shell.test"], "more": 1])).isEmpty)
        #expect(NativeHTTPOrigins.load(nil).isEmpty)
        #expect(NativeHTTPOrigins.load(URL(fileURLWithPath: "/nonexistent/native-http.json")).isEmpty)
        // The app's own bundle holds no list in a test run: none.
        #expect(NativeHTTPOrigins.bundled().isEmpty)
    }

    @Test func admitsOnlyHttpsOnANamedOrigin() throws {
        let o = origins(["https://relay.shell.test", "https://other.shell.test:8443"])
        #expect(try o.admit("https://relay.shell.test/v1/inbox?page=2").get().absoluteString == "https://relay.shell.test/v1/inbox?page=2")
        #expect(try o.admit("https://relay.shell.test").get().absoluteString == "https://relay.shell.test/")
        #expect(try o.admit("https://relay.shell.test:443/x").get().absoluteString == "https://relay.shell.test/x")
        #expect(try o.admit("https://other.shell.test:8443/x").get().absoluteString == "https://other.shell.test:8443/x")
        // Upper case is the same origin, sent in lower case.
        #expect(try o.admit("HTTPS://RELAY.SHELL.TEST/x").get().absoluteString == "https://relay.shell.test/x")
        for bad in ["https://elsewhere.shell.test/", "http://relay.shell.test/", "file:///etc/hosts", "https://user@relay.shell.test/",
                    "https://user:pw@relay.shell.test/", "https://relay.shell.test:8443/", "https://other.shell.test/", "https://relay.shell.test./",
                    "https://relay.shell.test.elsewhere.test/", "https://relay.shell.test@elsewhere.test/", "https://%72elay.shell.test/",
                    "https://r\u{0435}lay.shell.test/", "https://relay.shell.test\\@elsewhere.test/", "wss://relay.shell.test/", "relay.shell.test/x", ""] {
            #expect(refusedCode(o.admit(bad)) == "origin_refused", "\(bad)")
        }
        #expect(refusedCode(NativeHTTPOrigins.none.admit("https://relay.shell.test/")) == "origin_refused")
        // An IP literal, in any of its spellings, is never an origin: not named, not admitted.
        for ip in ["127.0.0.1", "127.1", "2130706433", "0x7f.0.0.1", "10.0.0.1"] {
            #expect(origins(["https://\(ip)"]).isEmpty, "\(ip)")
            #expect(refusedCode(o.admit("https://\(ip)/")) == "origin_refused", "\(ip)")
        }
        for ip in ["https://[::1]/", "https://[::ffff:127.0.0.1]/", "https://[fe80::1%25en0]/"] {
            #expect(refusedCode(o.admit(ip)) == "origin_refused", "\(ip)")
        }
        // A name with a numeric label is still a name.
        #expect(!origins(["https://api2.example.test", "https://1password.example.test"]).isEmpty)
    }

    @Test func aCallIsSentOnlyWhenAdmittedAndAnsweredInTheBridgesShape() async throws {
        let t = FakeTransport()
        let calls = NativeHTTPCalls(origins: origins(["https://relay.shell.test"]), transport: t)
        let a = await calls.call("request", ["url": "https://relay.shell.test/v1/answer", "method": "post", "headers": ["Content-Type": "application/json"], "data": "{\"a\":1}", "responseType": "text"])
        guard case .resolve(let v) = a else { Issue.record("not sent: \(a)"); return }
        #expect(v["status"] == .int(200))
        #expect(v["data"] == .string("the reply"))
        #expect(v["headers"] == .object(["content-type": .string("text/plain"), "x-relay": .string("one")]))
        let sent = try #require(t.sent.first)
        #expect(sent.url?.absoluteString == "https://relay.shell.test/v1/answer")
        #expect(sent.httpMethod == "POST")
        #expect(sent.value(forHTTPHeaderField: "Content-Type") == "application/json")
        #expect(sent.httpBody == Data("{\"a\":1}".utf8))
        #expect(!sent.httpShouldHandleCookies)
        // Refused before anything reaches the transport: another origin, none named, arguments of the wrong kind.
        for (m, o) in [("request", ["url": "https://elsewhere.shell.test/"]), ("get", ["url": "file:///etc/hosts"]),
                       ("request", ["url": "https://relay.shell.test/", "method": "TRACE"]), ("request", ["url": 7]),
                       ("post", ["url": "https://relay.shell.test/", "data": ["a": 1]]), ("get", ["url": "https://relay.shell.test/", "headers": ["n": 1]]),
                       ("get", ["url": "https://relay.shell.test/", "params": ["q": "1"]]), ("fetch", ["url": "https://relay.shell.test/"])] as [(String, [String: Any])] {
            if case .resolve = await calls.call(m, o) { Issue.record("\(m) \(o) was sent") }
        }
        if case .resolve = await NativeHTTPCalls(origins: .none, transport: t).call("get", ["url": "https://relay.shell.test/"]) { Issue.record("sent with none named") }
        #expect(t.count == 1)
    }

    @Test func aHeaderThatNamesWhereARequestGoesOrHowItIsFramedIsRefusedAndNothingSent() async throws {
        let t = FakeTransport()
        let calls = NativeHTTPCalls(origins: origins(["https://relay.shell.test"]), transport: t)
        let refused: [(String, String)] = [
            ("Host", "other.example"), ("host", "other.example"), ("HOST", "other.example"), ("Connection", "close"), ("Keep-Alive", "timeout=5"),
            ("Proxy-Authorization", "Basic eA=="), ("proxy-connection", "keep-alive"), ("Content-Length", "0"), ("Transfer-Encoding", "chunked"),
            ("TE", "trailers"), ("te", "trailers"), ("Upgrade", "websocket"), ("Trailer", "X"),
            ("", "x"), ("X Bad", "x"), ("X:Bad", "x"), ("Ä", "x"), ("X(", "x"), ("X-Ok", "a\r\nHost: other.example"), ("X-Ok", "a\u{0}"),
            ("X-Ok", "a\u{7f}"), ("X-Ok", "a\tb"),
        ]
        for (k, v) in refused {
            let a = await calls.call("request", ["url": "https://relay.shell.test/v1", "headers": [k: v]])
            guard case .reject(let code, _) = a else { Issue.record("\(k): \(v) was sent"); continue }
            #expect(code == "invalid_request", "\(k)")
        }
        #expect(t.count == 0)
        // What a provider sends is untouched.
        let a = await calls.call("request", ["url": "https://relay.shell.test/v1", "method": "POST",
                                             "headers": ["Authorization": "Bearer t0k.en", "Content-Type": "application/json", "X-Request-Id": "r-1~"]])
        guard case .resolve = a else { Issue.record("not sent: \(a)"); return }
        let sent = try #require(t.sent.first)
        #expect(sent.value(forHTTPHeaderField: "Authorization") == "Bearer t0k.en")
        #expect(sent.value(forHTTPHeaderField: "Content-Type") == "application/json")
        #expect(sent.value(forHTTPHeaderField: "X-Request-Id") == "r-1~")
    }

    @Test(.timeLimit(.minutes(1))) func thePhonesSessionFollowsNoRedirectKeepsNoCookieAndBoundsTheBody() async throws {
        let calls = NativeHTTPCalls(origins: origins(["https://relay.shell.test"]), transport: SessionTransport(maxBody: 64, protocolClasses: [StubServer.self]))
        // A redirect off the list is not followed: the reply asking for it is the answer, and the other origin is never asked.
        let redirected = await calls.call("get", ["url": "https://relay.shell.test/redirect"])
        #expect(!StubServer.requests.contains { $0.url?.host == "elsewhere.shell.test" })
        if case .resolve(let v) = redirected { #expect(v["status"] == .int(302)) }
        // A cookie the jar holds for the origin is not sent, and one the reply sets is not kept.
        let jar = HTTPCookieStorage.shared
        jar.setCookie(HTTPCookie(properties: [.domain: "relay.shell.test", .path: "/", .name: "old", .value: "first sign-in", .secure: "TRUE"])!)
        _ = await calls.call("get", ["url": "https://relay.shell.test/cookie"])
        let asked = try #require(StubServer.requests.last { $0.url?.path == "/cookie" })
        #expect(asked.value(forHTTPHeaderField: "Cookie") == nil)
        #expect(!(jar.cookies(for: URL(string: "https://relay.shell.test/")!) ?? []).contains { $0.name == "kept" })
        let session = (calls.transport as? SessionTransport)?.session.configuration
        #expect(session?.httpCookieStorage == nil && session?.urlCache == nil && session?.urlCredentialStorage == nil)
        // A body past the bound is refused, not handed over.
        guard case .reject(let code, _) = await calls.call("get", ["url": "https://relay.shell.test/big"]) else {
            Issue.record("a body past its bound was handed over")
            return
        }
        #expect(code == "too_large")
    }
}
