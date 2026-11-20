import Foundation
import LocalAuthentication
import Security

// The key's seams as a phone gives them: the keychain item (this device only, while unlocked, never synced), the person's presence
// (Face ID, Touch ID or the passcode, asked for one call), and the install's mark under Application Support, kept out of backups.

/** The key's one keychain item: a generic password of the shell's own service, readable only while the phone is unlocked. */
public struct KeychainVault: KeyVault {
    public let service: String
    public let account: String
    public init(service: String = "conductor.shell.key", account: String = "device") {
        self.service = service
        self.account = account
    }

    /**
     * The attributes the item is made with and found by: this device only, while unlocked (never synced to another device, never in
     * a backup), the data-protection keychain where the platform has more than one.
     */
    public func attributes() -> [String: Any] {
        var a: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlockedThisDeviceOnly,
            kSecAttrSynchronizable as String: kCFBooleanFalse as Any,
        ]
        #if os(macOS)
            a[kSecUseDataProtectionKeychain as String] = kCFBooleanTrue as Any
        #endif
        return a
    }

    private func query() -> [String: Any] {
        var q = attributes()
        q.removeValue(forKey: kSecAttrAccessible as String)
        return q
    }

    public func read() throws -> KeyRecord? {
        var q = query()
        q[kSecReturnData as String] = kCFBooleanTrue
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = out as? Data else { throw KeyRefusal(code: "keychain", words: "The key could not be read (\(status)).") }
        return try JSONDecoder().decode(KeyRecord.self, from: data)
    }

    public func write(_ record: KeyRecord) throws {
        let data = try JSONEncoder().encode(record)
        let status = SecItemUpdate(query() as CFDictionary, [kSecValueData as String: data] as CFDictionary)
        if status == errSecItemNotFound {
            var add = attributes()
            add[kSecValueData as String] = data
            let added = SecItemAdd(add as CFDictionary, nil)
            guard added == errSecSuccess else { throw KeyRefusal(code: "keychain", words: "The key could not be kept (\(added)).") }
            return
        }
        guard status == errSecSuccess else { throw KeyRefusal(code: "keychain", words: "The key could not be kept (\(status)).") }
    }

    public func delete() throws {
        let status = SecItemDelete(query() as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else { throw KeyRefusal(code: "keychain", words: "The key could not be removed (\(status)).") }
    }
}

/** The person's presence as the phone asks it: a fresh context each call, Face ID, Touch ID or the passcode. */
public struct DevicePresence: PresenceCheck {
    public init() {}
    public func confirm(reason: String) async -> Bool {
        let context = LAContext()
        do {
            return try await context.evaluatePolicy(.deviceOwnerAuthentication, localizedReason: reason)
        } catch {
            return false
        }
    }
}

/** The install's mark: `conductor/key.marker` under Application Support, an empty file kept out of backups. */
public struct FileMarker: KeyMarker {
    public let url: URL
    public init(directory: URL) {
        url = directory.appendingPathComponent("conductor", isDirectory: true).appendingPathComponent("key.marker")
    }

    /** The mark under this app's Application Support. */
    public static func applicationSupport() throws -> FileMarker {
        FileMarker(directory: try FileManager.default.url(for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil, create: true))
    }

    public func exists() -> Bool {
        FileManager.default.fileExists(atPath: url.path)
    }

    public func write() throws {
        try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        try Data().write(to: url, options: .atomic)
        var u = url
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        try u.setResourceValues(values)
    }

    public func remove() {
        try? FileManager.default.removeItem(at: url)
    }
}
