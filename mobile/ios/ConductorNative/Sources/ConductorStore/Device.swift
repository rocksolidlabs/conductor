import Foundation
import WebKit

// The store's seams as a phone gives them: the file system (a rename over the old record, atomic within a folder), each file's
// protection (complete: unreadable while the phone is locked) and the folder out of backups, and the shell's HTTP caches.

/** The file system through FileManager, the rename through rename(2): it replaces the old file in one step. */
public struct DiskFiles: StoreFiles {
    public init() {}

    public func createDirectory(_ url: URL) throws {
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
    }

    public func write(_ data: Data, to url: URL) throws {
        try data.write(to: url)
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

/** The shell's HTTP caches: the URL cache and the web view's own data (the shell's web view loads its bundle alone). */
public struct WebCaches: HTTPCaches {
    public init() {}

    public func clear() async {
        URLCache.shared.removeAllCachedResponses()
        await withCheckedContinuation { (done: CheckedContinuation<Void, Never>) in
            DispatchQueue.main.async {
                WKWebsiteDataStore.default().removeData(ofTypes: WKWebsiteDataStore.allWebsiteDataTypes(), modifiedSince: .distantPast) {
                    done.resume()
                }
            }
        }
    }
}

extension ConductorStore {
    /** The store under this app's Application Support (`conductor/`), on the phone's own seams. */
    public static func applicationSupport() throws -> ConductorStore {
        let base = try FileManager.default.url(for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil, create: true)
        return ConductorStore(root: base.appendingPathComponent("conductor", isDirectory: true), files: DiskFiles(), protection: DeviceProtection(), caches: WebCaches())
    }
}
