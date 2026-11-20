import Foundation
import WebKit

// The store's seams as a phone gives them: the file system (a rename over the old record, atomic within a folder), each file's
// protection (complete: unreadable while the phone is locked) and the folder out of backups, and the shell's HTTP caches.

/** The file system through FileManager, the rename through rename(2): it replaces the old file in one step. */
public struct DiskFiles: StoreFiles {
    public init() {}

    public func createDirectory(_ url: URL, complete: Bool) throws {
        var attributes: [FileAttributeKey: Any] = [:]
        #if os(iOS)
            if complete { attributes[.protectionKey] = FileProtectionType.complete }
        #endif
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true, attributes: attributes)
        #if os(iOS)
            // A folder made before (an earlier start) gets the class too: what is made in it after inherits it.
            if complete { try FileManager.default.setAttributes([.protectionKey: FileProtectionType.complete], ofItemAtPath: url.path) }
        #endif
    }

    public func write(_ data: Data, to url: URL, complete: Bool) throws {
        var options: Data.WritingOptions = []
        #if os(iOS)
            if complete { options.insert(.completeFileProtection) }
        #endif
        try data.write(to: url, options: options)
    }

    public func rename(_ from: URL, to: URL) throws {
        guard Foundation.rename(from.path, to.path) == 0 else { throw CocoaError(.fileWriteUnknown) }
    }

    public func read(_ url: URL) throws -> Data? {
        guard FileManager.default.fileExists(atPath: url.path) else { return nil }
        return try Data(contentsOf: url)
    }

    public func remove(_ url: URL) throws {
        do {
            try FileManager.default.removeItem(at: url)
        } catch CocoaError.fileNoSuchFile {
            return
        }
    }

    public func list(_ directory: URL) throws -> [URL] {
        try FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)
    }
}

/** Each record's file complete-protected (where the platform has data protection); the store's folder out of backups. */
public struct DeviceProtection: StoreProtection {
    public init() {}

    public func protect(_ file: URL) throws {
        #if os(iOS)
            try FileManager.default.setAttributes([.protectionKey: FileProtectionType.complete], ofItemAtPath: file.path)
        #endif
    }

    public func excludeFromBackup(_ directory: URL) throws {
        var u = directory
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        try u.setResourceValues(values)
    }
}

/**
 * The shell's HTTP caches and what an HTTP call keeps: the URL cache, the app's cookie jar and its stored credentials, then the web
 * view's own data (the shell's web view loads its bundle alone). The jar goes before the web view's store, so nothing of it is
 * copied back into the store just emptied.
 */
public struct WebCaches: HTTPCaches {
    let jar: HTTPCookieStorage
    let credentials: URLCredentialStorage
    let webData: @Sendable () async -> Void

    /** The phone's own: the shared jar and credential store, and the web view's data store. */
    public init() {
        self.init(jar: .shared, credentials: .shared, webData: WebCaches.removeWebViewData)
    }

    /** jar and credentials as given; webData empties the web view's store (a test gives its own). */
    public init(jar: HTTPCookieStorage, credentials: URLCredentialStorage, webData: @escaping @Sendable () async -> Void) {
        self.jar = jar
        self.credentials = credentials
        self.webData = webData
    }

    /** The web view's whole data store emptied. */
    @Sendable public static func removeWebViewData() async {
        await withCheckedContinuation { (done: CheckedContinuation<Void, Never>) in
            DispatchQueue.main.async {
                WKWebsiteDataStore.default().removeData(ofTypes: WKWebsiteDataStore.allWebsiteDataTypes(), modifiedSince: .distantPast) {
                    done.resume()
                }
            }
        }
    }

    private func emptyJar() {
        jar.removeCookies(since: .distantPast)
        for cookie in jar.cookies ?? [] { jar.deleteCookie(cookie) }
    }

    /**
     * The jar emptied before and again after the web view's store: the runtime copies the web view's cookies into the jar as they
     * change, so one the web view had while its store was being emptied is not left in the jar.
     */
    public func clear() async {
        URLCache.shared.removeAllCachedResponses()
        emptyJar()
        for (space, byUser) in credentials.allCredentials {
            for credential in byUser.values { credentials.remove(credential, for: space) }
        }
        await webData()
        emptyJar()
    }
}

extension ConductorStore {
    /** The store under this app's Application Support (`conductor/`), on the phone's own seams. */
    public static func applicationSupport() throws -> ConductorStore {
        let base = try FileManager.default.url(for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil, create: true)
        return ConductorStore(root: base.appendingPathComponent("conductor", isDirectory: true), files: DiskFiles(), protection: DeviceProtection(), caches: WebCaches())
    }
}
