import CryptoKit
import Foundation

// The phone shell's key (docs/plans/phone-shell.md, OSS-1a, "ConductorKey.swift"): one Ed25519 key kept raw as one item of this
// device's keychain, never synced, never in a backup, never leaving this code. It knows no service, no contract and no route: it
// signs only what a native signing policy builds (a class the packaging adds under the fixed name `ConductorKeySigningPolicy`; the
// open-source build has none, so it signs nothing), and when a plan asks for the person's presence it makes that check itself, for
// that one call, before it signs. Where the key is kept, how presence is asked and what time it is are seams (`KeyVault`,
// `PresenceCheck`, `KeyMarker`, the clock), so its rules are tested with fakes; the device's own are in `Device.swift`.

/** The key's state: none; made, not yet bound to an id (`unbound`); bound to an id; kept only to end what it was bound to. */
public enum KeyState: String, Codable, Sendable {
    case none, unbound, bound, ending
}

/** A refusal of the key's: a code and its words, passed to the page as they are. */
public struct KeyRefusal: Error, Equatable, Sendable {
    public let code: String
    public let words: String
    public init(code: String, words: String) {
        self.code = code
        self.words = words
    }
}

/** A policy's refusal of a request: its code and words, passed back as they are. */
public struct KeyPolicyRefusal: Error, Sendable {
    public let code: String
    public let words: String
    public init(code: String, words: String) {
        self.code = code
        self.words = words
    }
}

/**
 * What a policy builds for a request: the bytes to sign, whether the person confirms first, whether an ending key may sign it, and
 * what the signature (64 bytes) becomes as the string answered.
 */
public struct SigningPlan {
    public let message: Data
    public let presence: Bool
    public let whileEnding: Bool
    public let finish: (Data) -> String
    public init(message: Data, presence: Bool, whileEnding: Bool, finish: @escaping (Data) -> String) {
        self.message = message
        self.presence = presence
        self.whileEnding = whileEnding
        self.finish = finish
    }
}

/**
 * A packaging's signing policy: it builds, it never signs. Exposed to the Objective-C runtime as `ConductorKeySigningPolicy` and found
 * once at start (`ConductorKey.packagedPolicy`).
 */
public protocol KeySigningPolicy: AnyObject {
    init()
    func plan(request: [String: Any], state: String, id: String?) -> Result<SigningPlan, KeyPolicyRefusal>
}

/** What the keychain item holds: the private key's raw bytes and the record kept beside it. */
public struct KeyRecord: Codable, Equatable, Sendable {
    public var privateKey: Data
    public var state: KeyState
    public var id: String?
    public var until: Date?
    public init(privateKey: Data, state: KeyState, id: String? = nil, until: Date? = nil) {
        self.privateKey = privateKey
        self.state = state
        self.id = id
        self.until = until
    }
}

/** Where the key is kept: one item, read, written whole, or deleted. A read of an item that is not a record throws this. */
public struct UnreadableKeyRecord: Error, Sendable {
    public init() {}
}

/** Where the key is kept: one item, read, written whole, or deleted. */
public protocol KeyVault: Sendable {
    func read() throws -> KeyRecord?
    func write(_ record: KeyRecord) throws
    func delete() throws
}

/** The person's presence, asked for one call: true when confirmed; false when cancelled, failed or not possible. */
public protocol PresenceCheck: Sendable {
    func confirm(reason: String) async -> Bool
}

/**
 * The install's mark, written with the key: a key found with no mark is an earlier install's (the keychain keeps an item after the
 * app is deleted, and a new install starts with no files), and is removed at start.
 */
public protocol KeyMarker: Sendable {
    func exists() -> Bool
    func write() throws
    func remove()
}

/** The words the person is asked to confirm an answer with. */
public let presenceReason = "Confirm your answer in Conductor"
/** A request handed to `sign` is a JSON object of at most this many bytes. */
public let maxSignRequest = 1024
/** A bound id is 1 to this many bytes of printable ASCII. */
public let maxKeyID = 128

/** Base64url with no padding (RFC 4648 §5). */
public func base64url(_ data: Data) -> String {
    data.base64EncodedString().replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "")
}

/** Whether s is a bound id: 1 to 128 bytes of printable ASCII, no space. */
func validKeyID(_ s: String) -> Bool {
    let bytes = Array(s.utf8)
    return !bytes.isEmpty && bytes.count <= maxKeyID && bytes.allSatisfy { $0 > 0x20 && $0 < 0x7F }
}

/**
 * The key. An actor: its calls run one at a time, and a sign that waits for the person's presence reads the key again after the
 * wait, so a key removed, ended or replaced meanwhile signs nothing.
 */
public actor ConductorKey {
    private let vault: KeyVault
    private let presence: PresenceCheck
    private let marker: KeyMarker
    private let policy: KeySigningPolicy?
    private let now: @Sendable () -> Date

    public init(vault: KeyVault, presence: PresenceCheck, marker: KeyMarker, policy: KeySigningPolicy?, now: @escaping @Sendable () -> Date = { Date() }) {
        self.vault = vault
        self.presence = presence
        self.marker = marker
        self.policy = policy
        self.now = now
    }

    /** The packaging's policy, found by its fixed name in the Objective-C runtime; none in the open-source build. */
    public static func packagedPolicy() -> KeySigningPolicy? {
        guard let type = NSClassFromString("ConductorKeySigningPolicy") as? KeySigningPolicy.Type else { return nil }
        return type.init()
    }

    private func refuse(_ code: String, _ words: String) -> KeyRefusal {
        KeyRefusal(code: code, words: words)
    }

    /**
     * The record kept, or nil. One that cannot be read as a key (an item of a form this build does not know, or a private key that is
     * not one) is removed, as if absent: no form of the item can leave the phone with a key it can neither use nor clear.
     */
    private func stored() throws -> KeyRecord? {
        let r: KeyRecord?
        do {
            r = try vault.read()
        } catch is UnreadableKeyRecord {
            try removeAll()
            return nil
        }
        guard let r else { return nil }
        guard (try? Curve25519.Signing.PrivateKey(rawRepresentation: r.privateKey)) != nil else {
            try removeAll()
            return nil
        }
        return r
    }

    /** The record, unbound past its time removed as if absent. */
    private func current() throws -> KeyRecord? {
        guard let r = try stored() else { return nil }
        if r.state == .unbound, let until = r.until, until <= now() {
            try removeAll()
            return nil
        }
        return r
    }

    private func removeAll() throws {
        try vault.delete()
        marker.remove()
    }

    /**
     * Run before any page script: a key whose install's mark is gone is an earlier install's and is removed; so is an unbound key
     * past its time.
     */
    public func start() throws {
        guard let r = try stored() else { return }
        if !marker.exists() {
            try vault.delete()
            return
        }
        if r.state == .unbound, let until = r.until, until <= now() {
            try removeAll()
        }
    }

    /**
     * Makes the key, unbound until `until`, and answers its public key (32 bytes, base64url). Only from none: a key unbound or bound
     * is refused `key_exists`, one ending `key_ending`, so a new sign-in never replaces a key whose end is not confirmed.
     */
    public func create(until: Date) throws -> String {
        if let r = try current() {
            if r.state == .ending { throw refuse("key_ending", "This phone's key is still ending what it was bound to.") }
            throw refuse("key_exists", "This phone has a key already.")
        }
        let key = Curve25519.Signing.PrivateKey()
        try marker.write()
        try vault.write(KeyRecord(privateKey: key.rawRepresentation, state: .unbound, until: until))
        return base64url(key.publicKey.rawRepresentation)
    }

    /** The public key (base64url), or nil with none. */
    public func publicKey() throws -> String? {
        guard let r = try current(), let key = try? Curve25519.Signing.PrivateKey(rawRepresentation: r.privateKey) else { return nil }
        return base64url(key.publicKey.rawRepresentation)
    }

    /** The state, the id once bound, and an unbound key's time. */
    public func state() throws -> (state: KeyState, id: String?, until: Date?) {
        guard let r = try current() else { return (.none, nil, nil) }
        return (r.state, r.id, r.until)
    }

    /** Binds an unbound key to id (1 to 128 bytes of printable ASCII). */
    public func bind(id: String) throws {
        guard validKeyID(id) else { throw refuse("invalid_id", "An id is 1 to \(maxKeyID) printable characters.") }
        guard var r = try current() else { throw refuse("no_key", "This phone has no key.") }
        guard r.state == .unbound else { throw refuse("not_unbound", "This phone's key is bound already.") }
        r.state = .bound
        r.id = id
        r.until = nil
        try vault.write(r)
    }

    /** A bound key, kept only to end what it was bound to. */
    public func ending() throws {
        guard var r = try current() else { throw refuse("no_key", "This phone has no key.") }
        guard r.state == .bound else { throw refuse("not_bound", "This phone's key is not bound.") }
        r.state = .ending
        try vault.write(r)
    }

    /** The key and its id gone: state none. */
    public func remove() throws {
        try removeAll()
    }

    /**
     * Signs what the policy builds for request (a JSON object of at most 1 KiB) and answers what the plan makes of the signature.
     * Nothing without a policy (`no_policy`), from none (`no_key`), past an unbound key's time (`key_expired`), with an ending key a
     * plan not marked for ending (`key_ending`); a policy's refusal is passed back as it is; when the plan asks for presence and the
     * person does not confirm, `presence_refused` and nothing signed. Never bytes handed in: only the plan's.
     */
    public func sign(request: [String: Any]) async throws -> String {
        guard JSONSerialization.isValidJSONObject(request), let body = try? JSONSerialization.data(withJSONObject: request) else {
            throw refuse("invalid_request", "A request is a JSON object.")
        }
        guard body.count <= maxSignRequest else { throw refuse("too_large", "A request is at most \(maxSignRequest) bytes.") }
        guard let found = try stored() else { throw refuse("no_key", "This phone has no key.") }
        if found.state == .unbound, let until = found.until, until <= now() {
            try removeAll()
            throw refuse("key_expired", "This phone's key was not bound in time.")
        }
        guard let policy else { throw refuse("no_policy", "This build signs nothing.") }
        let plan: SigningPlan
        switch policy.plan(request: request, state: found.state.rawValue, id: found.id) {
        case .failure(let r): throw refuse(r.code, r.words)
        case .success(let p): plan = p
        }
        if found.state == .ending && !plan.whileEnding { throw refuse("key_ending", "This phone's key only ends what it was bound to.") }
        if plan.presence {
            guard await presence.confirm(reason: presenceReason) else {
                throw refuse("presence_refused", "Not confirmed: nothing was signed.")
            }
        }
        // The wait above let other calls run: the key signs only if it is still the one the plan was built for, in the same state.
        guard let again = try stored(), again.privateKey == found.privateKey, again.state == found.state, again.id == found.id else {
            throw refuse("key_changed", "This phone's key changed while it was confirmed: nothing was signed.")
        }
        // Nor if an unbound key's time ran out while the person was asked: the same rule as before the wait.
        if again.state == .unbound, let until = again.until, until <= now() {
            try removeAll()
            throw refuse("key_expired", "This phone's key was not bound in time.")
        }
        let key = try Curve25519.Signing.PrivateKey(rawRepresentation: again.privateKey)
        return plan.finish(try key.signature(for: plan.message))
    }
}
