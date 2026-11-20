import CryptoKit
import Foundation
import Security
import Testing

@testable import ConductorKey

// The key's rules (docs/plans/phone-shell.md, Tests per surface, `ConductorKeyTests`), with fakes for where it is kept, the person's
// presence and the clock, and a test policy of the tests' own that names no service. What only a phone shows (the sheet itself, the
// keychain's own behaviour while locked) is the by-hand steps' part.

/** A keychain item held in memory. */
final class MemoryVault: KeyVault, @unchecked Sendable {
    let lock = NSLock()
    var record: KeyRecord?
    func read() throws -> KeyRecord? { lock.withLock { record } }
    func write(_ r: KeyRecord) throws { lock.withLock { record = r } }
    func delete() throws { lock.withLock { record = nil } }
}

/** The install's mark held in memory. */
final class MemoryMarker: KeyMarker, @unchecked Sendable {
    let lock = NSLock()
    var present = false
    func exists() -> Bool { lock.withLock { present } }
    func write() throws { lock.withLock { present = true } }
    func remove() { lock.withLock { present = false } }
}

/** A presence check the test answers, recording each reason; `during` runs while the person is asked. */
final class FakePresence: PresenceCheck, @unchecked Sendable {
    let lock = NSLock()
    var answer: Bool
    var reasons: [String] = []
    var during: (@Sendable () async -> Void)?
    init(answer: Bool) { self.answer = answer }
    func confirm(reason: String) async -> Bool {
        lock.withLock { reasons.append(reason) }
        await during?()
        return lock.withLock { answer }
    }
    var asked: Int { lock.withLock { reasons.count } }
}

/** A clock the test moves. */
final class FakeClock: @unchecked Sendable {
    let lock = NSLock()
    var t = Date(timeIntervalSince1970: 1_800_000_000)
    func now() -> Date { lock.withLock { t } }
    func advance(_ s: TimeInterval) { lock.withLock { t = t.addingTimeInterval(s) } }
}

/**
 * The tests' policy: it signs the bytes "test:" and the request's "m", asks presence when the request says so, lets an ending key
 * sign only a request marked "end", refuses a request with "refuse", and records each signature it is handed.
 */
final class TestPolicy: KeySigningPolicy {
    var finished: [Data] = []
    init() {}
    func plan(request: [String: Any], state: String, id: String?) -> Result<SigningPlan, KeyPolicyRefusal> {
        if request["refuse"] != nil { return .failure(KeyPolicyRefusal(code: "not_allowed", words: "Not a request this policy signs.")) }
        let m = request["m"] as? String ?? ""
        return .success(SigningPlan(message: Data("test:\(m)".utf8), presence: request["presence"] as? Bool ?? false, whileEnding: request["end"] as? Bool ?? false) { sig in
            self.finished.append(sig)
            return base64url(sig)
        })
    }
}

struct Fixture {
    let vault = MemoryVault()
    let marker = MemoryMarker()
    let presence: FakePresence
    let clock = FakeClock()
    let policy: TestPolicy?
    let key: ConductorKey

    init(presence answer: Bool = true, policy: Bool = true) {
        presence = FakePresence(answer: answer)
        let p = policy ? TestPolicy() : nil
        self.policy = p
        let c = clock
        key = ConductorKey(vault: vault, presence: presence, marker: marker, policy: p, now: { c.now() })
    }

    /** A key made and bound to `dev-1`. */
    func bound() async throws -> String {
        let pk = try await key.create(until: clock.now().addingTimeInterval(600))
        try await key.bind(id: "dev-1")
        return pk
    }
}

func refusal(_ f: () async throws -> Void) async -> String? {
    do {
        try await f()
        return nil
    } catch let r as KeyRefusal {
        return r.code
    } catch {
        return "other: \(error)"
    }
}

func publicKey(_ b64url: String) throws -> Curve25519.Signing.PublicKey {
    var s = b64url.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
    while s.count % 4 != 0 { s += "=" }
    return try Curve25519.Signing.PublicKey(rawRepresentation: Data(base64Encoded: s)!)
}

@Suite struct ConductorKeyTests {
    @Test func signsNothingWithoutAPolicy() async throws {
        let f = Fixture(policy: false)
        _ = try await f.bound()
        #expect(await refusal { _ = try await f.key.sign(request: ["m": "x", "presence": true]) } == "no_policy")
        #expect(f.presence.asked == 0)
        #expect(ConductorKey.packagedPolicy() == nil)
    }

    @Test func signsOnlyThePlansBytes() async throws {
        let f = Fixture()
        let pk = try publicKey(try await f.bound())
        #expect(try await f.key.publicKey() == base64url(pk.rawRepresentation))
        // The request names bytes of its own: they are never what is signed.
        let request: [String: Any] = ["m": "answer dr-7", "bytes": "c2lnbiBtZQ"]
        let out = try await f.key.sign(request: request)
        let sig = try #require(f.policy?.finished.first)
        #expect(out == base64url(sig))
        #expect(pk.isValidSignature(sig, for: Data("test:answer dr-7".utf8)))
        #expect(!pk.isValidSignature(sig, for: Data("c2lnbiBtZQ".utf8)))
        #expect(!pk.isValidSignature(sig, for: try JSONSerialization.data(withJSONObject: request)))
    }

    @Test func asksPresenceWheneverThePlanDoesAndSignsNothingWhenRefused() async throws {
        let f = Fixture(presence: false)
        _ = try await f.bound()
        #expect(await refusal { _ = try await f.key.sign(request: ["m": "x", "presence": true]) } == "presence_refused")
        #expect(f.presence.reasons == [presenceReason])
        #expect(f.policy?.finished.isEmpty == true)
        // No presence asked when the plan does not ask it.
        _ = try await f.key.sign(request: ["m": "y"])
        #expect(f.presence.asked == 1)
        f.presence.answer = true
        _ = try await f.key.sign(request: ["m": "z", "presence": true])
        #expect(f.presence.asked == 2)
        #expect(f.policy?.finished.count == 2)
        #expect(presenceReason == "Confirm your answer in Conductor")
    }

    @Test func passesAPolicysRefusalAsItIs() async throws {
        let f = Fixture()
        _ = try await f.bound()
        #expect(await refusal { _ = try await f.key.sign(request: ["refuse": true, "presence": true]) } == "not_allowed")
        #expect(f.presence.asked == 0)
    }

    @Test func anEndingKeySignsOnlyWhatThePlanMarksForEnding() async throws {
        let f = Fixture()
        _ = try await f.bound()
        try await f.key.ending()
        #expect(try await f.key.state().state == .ending)
        #expect(await refusal { _ = try await f.key.sign(request: ["m": "answer"]) } == "key_ending")
        _ = try await f.key.sign(request: ["m": "end it", "end": true])
        #expect(f.policy?.finished.count == 1)
    }

    @Test func refusesANewKeyWhileOneIsUnboundBoundOrEnding() async throws {
        let f = Fixture()
        let until = f.clock.now().addingTimeInterval(600)
        let first = try await f.key.create(until: until)
        #expect(first.count == 43)
        #expect(await refusal { _ = try await f.key.create(until: until) } == "key_exists")
        try await f.key.bind(id: "dev-1")
        #expect(await refusal { _ = try await f.key.create(until: until) } == "key_exists")
        try await f.key.ending()
        #expect(await refusal { _ = try await f.key.create(until: until) } == "key_ending")
        #expect(try await f.key.publicKey() == first)
        try await f.key.remove()
        #expect(try await f.key.state().state == KeyState.none)
        #expect(!f.marker.exists())
        let second = try await f.key.create(until: until)
        #expect(second != first)
    }

    @Test func bindsAnUnboundKeyToAnIdOfItsBounds() async throws {
        let f = Fixture()
        _ = try await f.key.create(until: f.clock.now().addingTimeInterval(600))
        for bad in ["", "has space", String(repeating: "a", count: 129), "é", "line\nbreak"] {
            #expect(await refusal { try await f.key.bind(id: bad) } == "invalid_id")
        }
        try await f.key.bind(id: String(repeating: "a", count: 128))
        let s = try await f.key.state()
        #expect(s.state == .bound && s.id == String(repeating: "a", count: 128) && s.until == nil)
        #expect(await refusal { try await f.key.bind(id: "dev-2") } == "not_unbound")
        #expect(await refusal { try await Fixture().key.bind(id: "dev-1") } == "no_key")
    }

    @Test func anUnboundKeyPastItsTimeIsGoneAndSignsNothing() async throws {
        let f = Fixture()
        _ = try await f.key.create(until: f.clock.now().addingTimeInterval(60))
        f.clock.advance(61)
        #expect(await refusal { _ = try await f.key.sign(request: ["m": "x"]) } == "key_expired")
        #expect(f.vault.record == nil)
        #expect(try await f.key.state().state == KeyState.none)
    }

    @Test func atStartRemovesAnEarlierInstallsKeyAndAnUnboundOnePastItsTime() async throws {
        // A key with no mark: an earlier install's.
        let f = Fixture()
        _ = try await f.bound()
        f.marker.remove()
        try await f.key.start()
        #expect(f.vault.record == nil)
        // An unbound key past its time.
        let g = Fixture()
        _ = try await g.key.create(until: g.clock.now().addingTimeInterval(60))
        g.clock.advance(120)
        try await g.key.start()
        #expect(g.vault.record == nil)
        #expect(!g.marker.exists())
        // A bound key with its mark stays.
        let h = Fixture()
        _ = try await h.bound()
        try await h.key.start()
        #expect(h.vault.record?.state == .bound)
    }

    @Test func signsNothingWithAKeyChangedWhileThePersonIsAsked() async throws {
        let f = Fixture()
        _ = try await f.bound()
        let key = f.key
        f.presence.during = { try? await key.remove() }
        #expect(await refusal { _ = try await f.key.sign(request: ["m": "x", "presence": true]) } == "key_changed")
        #expect(f.policy?.finished.isEmpty == true)
    }

    @Test func refusesARequestPastItsBound() async throws {
        let f = Fixture()
        _ = try await f.bound()
        #expect(await refusal { _ = try await f.key.sign(request: ["m": String(repeating: "x", count: maxSignRequest)]) } == "too_large")
        #expect(f.presence.asked == 0)
    }

    @Test func keepsTheKeychainItemOnThisDeviceOnlyWhileUnlocked() {
        let a = KeychainVault().attributes()
        #expect(a[kSecAttrAccessible as String] as? String == kSecAttrAccessibleWhenUnlockedThisDeviceOnly as String)
        #expect((a[kSecAttrSynchronizable as String] as? Bool) == false)
        #expect(a[kSecClass as String] as? String == kSecClassGenericPassword as String)
    }

    @Test func keepsTheInstallsMarkOutOfBackups() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("conductor-key-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: dir) }
        let m = FileMarker(directory: dir)
        #expect(!m.exists())
        try m.write()
        #expect(m.exists())
        #expect(try m.url.resourceValues(forKeys: [.isExcludedFromBackupKey]).isExcludedFromBackup == true)
        m.remove()
        #expect(!m.exists())
    }
}
