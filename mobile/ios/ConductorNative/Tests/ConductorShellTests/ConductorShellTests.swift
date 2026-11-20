import ConductorKey
import ConductorStore
import Foundation
import Testing
import WebKit

@testable import ConductorShell

// The plugins' calls as the page makes them (mobile/src/bridge.ts, `NativeKey` and `NativeStore`): each call's arguments decoded and
// its answer in the shape the bridge reads, each refusal with its code and its words as the page's rejected error carries them, the
// start before the first call, and the web view's media capture refused. The key is over fakes, the store over a temporary folder.

final class Vault: KeyVault, @unchecked Sendable {
    let lock = NSLock()
    var record: KeyRecord?
    func read() throws -> KeyRecord? { lock.withLock { record } }
    func write(_ r: KeyRecord) throws { lock.withLock { record = r } }
    func delete() throws { lock.withLock { record = nil } }
}

final class Marker: KeyMarker, @unchecked Sendable {
    let lock = NSLock()
    var present = false
    func exists() -> Bool { lock.withLock { present } }
    func write() throws { lock.withLock { present = true } }
    func remove() { lock.withLock { present = false } }
}

struct Present: PresenceCheck {
    func confirm(reason: String) async -> Bool { true }
}

/** A policy that signs "shell:" and the request's "m", and refuses a request with "refuse" by a code of its own. */
final class Policy: KeySigningPolicy {
    init() {}
    func plan(request: [String: Any], state: String, id: String?, publicKey: String) -> Result<SigningPlan, KeyPolicyRefusal> {
        if request["refuse"] != nil { return .failure(KeyPolicyRefusal(code: "not_allowed", words: "Not a request this policy signs.")) }
        return .success(SigningPlan(message: Data("shell:\(request["m"] as? String ?? "")".utf8), presence: false, whileEnding: false) { base64url($0) })
    }
}

struct Keys {
    let vault = Vault()
    let marker = Marker()
    let calls: KeyCalls
    init(policy: Bool = true) {
        calls = KeyCalls(key: ConductorKey(vault: vault, presence: Present(), marker: marker, policy: policy ? Policy() : nil))
    }
    func call(_ m: String, _ o: [String: Any] = [:]) async -> CallAnswer { await calls.call(m, o) }
}

struct NoProtection: StoreProtection {
    func protect(_ file: URL) throws {}
    func excludeFromBackup(_ directory: URL) throws {}
}

struct NoCaches: HTTPCaches {
    func clear() async {}
}

func temporaryStore() throws -> (StoreCalls, URL) {
    let root = FileManager.default.temporaryDirectory.appendingPathComponent("shell-\(UUID().uuidString)", isDirectory: true)
    return (StoreCalls(store: ConductorStore(root: root, files: DiskFiles(), protection: NoProtection(), caches: NoCaches())), root)
}

func code(_ a: CallAnswer) -> String? {
    if case .reject(let c, _) = a { return c }
    return nil
}

func string(_ a: CallAnswer, _ field: String) -> String? {
    if case .resolve(let v) = a, case .string(let s)? = v[field] { return s }
    return nil
}

/** An hour from now, as the page writes a time. */
let soon = isoTime(Date().addingTimeInterval(3600))

/** An object handed where a type the test cannot make is taken, by a call that never reads it. */
func standIn<T: AnyObject>(_ o: NSObject) -> T {
    Unmanaged<T>.fromOpaque(Unmanaged.passUnretained(o).toOpaque()).takeUnretainedValue()
}

@Suite struct ConductorShellTests {
    @Test func theKeysCallsAnswerAsTheBridgeReadsThem() async throws {
        let k = Keys()
        #expect(await k.call("state") == .resolve(["state": .string("none")]))
        #expect(await k.call("publicKey") == .resolve(["publicKey": .null]))
        let made = await k.call("create", ["until": soon])
        let pk = try #require(string(made, "publicKey"))
        #expect(pk.count == 43)
        #expect(await k.call("publicKey") == .resolve(["publicKey": .string(pk)]))
        #expect(await k.call("state") == .resolve(["state": .string("unbound"), "until": .string(soon)]))
        #expect(await k.call("bind", ["id": "dev-1"]) == .resolve([:]))
        #expect(await k.call("state") == .resolve(["state": .string("bound"), "id": .string("dev-1")]))
        let signed = await k.call("sign", ["request": ["m": "hello"]])
        #expect(string(signed, "signature")?.count == 86)
        #expect(await k.call("ending") == .resolve([:]))
        #expect(await k.call("state") == .resolve(["state": .string("ending"), "id": .string("dev-1")]))
        #expect(await k.call("remove") == .resolve([:]))
        #expect(await k.call("state") == .resolve(["state": .string("none")]))
    }

    @Test func theKeyRefusesArgumentsItDoesNotTake() async {
        let k = Keys()
        #expect(code(await k.call("create")) == "invalid_request")
        #expect(code(await k.call("create", ["until": "tomorrow"])) == "invalid_request")
        #expect(code(await k.call("create", ["until": 1_800_000_000])) == "invalid_request")
        #expect(code(await k.call("bind", ["id": 5])) == "invalid_request")
        #expect(code(await k.call("sign", ["request": "m"])) == "invalid_request")
        #expect(code(await k.call("sign")) == "invalid_request")
        #expect(code(await k.call("export")) == "unknown_call")
        #expect(k.vault.record == nil)
    }

    @Test func theKeysRefusalsKeepTheirCodeAndWords() async throws {
        let k = Keys()
        let none = await k.call("sign", ["request": ["m": "x"]])
        guard case .reject(let c, let words) = none else { Issue.record("signed with no key"); return }
        #expect(c == "no_key")
        let r = try #require(none.rejection)
        #expect(r.code == "no_key" && r.message == words && r.data["words"] as? String == words && !words.isEmpty)
        #expect(none.resolved == nil)
        _ = await k.call("create", ["until": soon])
        #expect(code(await k.call("create", ["until": soon])) == "key_exists")
        #expect(code(await k.call("ending")) == "not_bound")
        _ = await k.call("bind", ["id": "dev-1"])
        #expect(code(await k.call("sign", ["request": ["refuse": true]])) == "not_allowed")
        #expect(code(await Keys(policy: false).call("sign", ["request": [:]])) == "no_key")
    }

    @Test func theKeyStartsBeforeItsFirstCall() async throws {
        let k = Keys()
        _ = await k.call("create", ["until": soon])
        // The install's mark gone (the app reinstalled) under a key still kept: a new start removes the key before it answers.
        let again = Keys()
        again.vault.record = k.vault.record
        #expect(again.vault.record != nil)
        #expect(await again.call("state") == .resolve(["state": .string("none")]))
        #expect(again.vault.record == nil)
    }

    @Test func theStoresCallsAnswerAsTheBridgeReadsThem() async throws {
        let (s, root) = try temporaryStore()
        defer { try? FileManager.default.removeItem(at: root) }
        #expect(await s.call("get", ["area": "drafts", "key": "d1"]) == .resolve(["data": .null]))
        #expect(await s.call("list", ["area": "drafts"]) == .resolve(["keys": .list([])]))
        #expect(await s.call("put", ["area": "drafts", "key": "d1", "data": "{\"a\":1}"]) == .resolve([:]))
        let got = await s.call("get", ["area": "drafts", "key": "d1"])
        #expect(string(got, "data") == "{\"a\":1}")
        let saved = try #require(string(got, "savedAt"))
        #expect(parseISOTime(saved) != nil && saved.hasSuffix("Z") && saved.contains("."))
        #expect(await s.call("list", ["area": "drafts"]) == .resolve(["keys": .list([.object(["key": .string("d1"), "savedAt": .string(saved)])])]))
        let plain = try #require(await s.call("list", ["area": "drafts"]).resolved)
        #expect(((plain["keys"] as? [Any])?.first as? [String: Any])?["key"] as? String == "d1")
        #expect(await s.call("remove", ["area": "drafts", "key": "d1"]) == .resolve([:]))
        #expect(await s.call("get", ["area": "drafts", "key": "d1"]).resolved?["data"] is NSNull)
        _ = await s.call("put", ["area": "cache", "key": "c1", "data": "x"])
        #expect(await s.call("clear") == .resolve([:]))
        #expect(await s.call("get", ["area": "cache", "key": "c1"]) == .resolve(["data": .null]))
    }

    @Test func theStoreRefusesWithItsOwnCodes() async throws {
        let (s, root) = try temporaryStore()
        defer { try? FileManager.default.removeItem(at: root) }
        #expect(code(await s.call("put", ["area": "drafts", "key": "d1"])) == "invalid_request")
        #expect(code(await s.call("put", ["area": "drafts", "key": "d1", "data": 7])) == "invalid_request")
        #expect(code(await s.call("get", ["area": "drafts"])) == "invalid_request")
        #expect(code(await s.call("list")) == "invalid_request")
        #expect(code(await s.call("remove", ["key": "d1"])) == "invalid_request")
        #expect(code(await s.call("put", ["area": "keys", "key": "d1", "data": "x"])) == "invalid_area")
        #expect(code(await s.call("put", ["area": "drafts", "key": "../d1", "data": "x"])) == "invalid_key")
        #expect(code(await s.call("put", ["area": "servers", "key": "s", "data": String(repeating: "x", count: StoreBounds.serverBytes + 1)])) == "too_large")
        #expect(code(await s.call("drop", ["area": "drafts"])) == "unknown_call")
    }

    @Test func optionsKeepTheirStringKeysAlone() {
        let o = callOptions([AnyHashable("area"): "drafts", AnyHashable(3): "x", AnyHashable("key"): "d1"])
        #expect(o.count == 2 && o["area"] as? String == "drafts" && o["key"] as? String == "d1")
        #expect(callOptions(nil).isEmpty)
        #expect(parseISOTime("2026-11-19T12:10:00Z") == parseISOTime("2026-11-19T12:10:00.000Z"))
        #expect(isoTime(parseISOTime(soon)!) == soon)
        #expect(parseISOTime("2026-11-19") == nil)
    }

    @Test func everyMediaCaptureIsDeniedAndTheRestHandedOn() {
        final class Runtime: NSObject, WKUIDelegate {
            func webView(_ webView: WKWebView, runJavaScriptAlertPanelWithMessage message: String, initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping () -> Void) {
                completionHandler()
            }
        }
        let runtime = Runtime()
        let d = MediaCaptureRefused(wrapping: runtime)
        let capture = #selector(WKUIDelegate.webView(_:requestMediaCapturePermissionFor:initiatedByFrame:type:decisionHandler:))
        let alert = #selector(WKUIDelegate.webView(_:runJavaScriptAlertPanelWithMessage:initiatedByFrame:completionHandler:))
        let confirm = #selector(WKUIDelegate.webView(_:runJavaScriptConfirmPanelWithMessage:initiatedByFrame:completionHandler:))
        #expect(d.responds(to: capture) && d.forwardingTarget(for: capture) == nil)
        #expect(d.responds(to: alert) && d.forwardingTarget(for: alert) as? Runtime === runtime)
        #expect(!d.responds(to: confirm))
        #expect(MediaCaptureRefused.decision == .deny)
        // The call itself, as the web view makes it, with stand-ins for the view, the origin and the frame (none of them is read):
        // the answer is .deny for every type.
        let stand = (NSObject(), NSObject(), NSObject())
        var answers: [WKPermissionDecision] = []
        withExtendedLifetime(stand) {
            let view: WKWebView = standIn(stand.0)
            let origin: WKSecurityOrigin = standIn(stand.1)
            let frame: WKFrameInfo = standIn(stand.2)
            for type in [WKMediaCaptureType.camera, .microphone, .cameraAndMicrophone] {
                d.webView(view, requestMediaCapturePermissionFor: origin, initiatedByFrame: frame, type: type) { answers.append($0) }
            }
        }
        #expect(answers == [.deny, .deny, .deny])
    }
}
