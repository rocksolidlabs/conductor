import Foundation

// The phone shell's store (docs/plans/phone-shell.md, OSS-1a, "ConductorStore.swift"): the only place on the phone the shell keeps
// anything of a decision or a draft. One file per record under Application Support, `conductor/<area>/<key>`, each written to
// `<key>.new` and renamed over the old one, so a write cut before its rename leaves the old record whole; every file under the
// strongest protection the platform gives (unreadable while the phone is locked), the folder kept out of backups. Three areas with
// bounds of their own: drafts (32 records of 12 KiB, never evicted), the cache (1 MiB in all, its oldest evicted first), and the
// shell's own servers (Open a server's last addresses). `clear()`, at a sign-out, empties the drafts and the cache and the HTTP
// caches before it answers, and leaves the servers. Each record carries the store's generation, which `clear()` moves first: a
// record a clear could not remove is from before it and is never read again. The file system, the clock, the files' protection and
// the HTTP caches are seams (`StoreFiles`, `StoreProtection`, `HTTPCaches`), so the rules are tested with fakes.

/** The store's areas: what the bridge names. */
public enum StoreArea: String, CaseIterable, Sendable {
    case drafts, cache, servers
}

/** A refusal of the store's: a code and its words, passed to the page as they are. */
public struct StoreRefusal: Error, Equatable, Sendable {
    public let code: String
    public let words: String
    public init(code: String, words: String) {
        self.code = code
        self.words = words
    }
}

/** The store's bounds. */
public enum StoreBounds {
    /** Drafts: at most this many records, never evicted. */
    public static let drafts = 32
    /** A draft's text, in bytes. */
    public static let draftBytes = 12 * 1024
    /** The cache's texts in all, in bytes; a record past it is refused. */
    public static let cacheBytes = 1024 * 1024
    /** The servers area: at most this many records, each of at most `serverBytes`. */
    public static let servers = 5
    public static let serverBytes = 12 * 1024
    /** A key: 1 to 64 of a-z, 0-9, '-', '_', '.'. */
    public static let keyLength = 64
}

/** The file system the store writes through: what a write, a rename and a listing need. */
public protocol StoreFiles: Sendable {
    func createDirectory(_ url: URL) throws
    func write(_ data: Data, to url: URL) throws
    func rename(_ from: URL, to: URL) throws
    func read(_ url: URL) throws -> Data?
    func remove(_ url: URL) throws
    func list(_ directory: URL) throws -> [URL]
}

/** The files' protection: each record unreadable while locked, the store's folder out of backups. */
public protocol StoreProtection: Sendable {
    func protect(_ file: URL) throws
    func excludeFromBackup(_ directory: URL) throws
}

/** The shell's HTTP caches: emptied at a sign-out. */
public protocol HTTPCaches: Sendable {
    func clear() async
}

/** A record read: its text and when it was kept. */
public struct StoredRecord: Equatable, Sendable {
    public let data: String
    public let savedAt: Date
}

/** A record as its file holds it. */
struct RecordFile: Codable {
    var g: Int
    var savedAt: Date
    var data: String
}

/** Whether key is a store's key: 1 to 64 of a-z, 0-9, '-', '_', '.', and no name a file system gives meaning to. */
func validStoreKey(_ key: String) -> Bool {
    let bytes = Array(key.utf8)
    guard !bytes.isEmpty, bytes.count <= StoreBounds.keyLength, key != ".", key != "..", !key.hasSuffix(".new") else { return false }
    return bytes.allSatisfy { ($0 >= 0x61 && $0 <= 0x7A) || ($0 >= 0x30 && $0 <= 0x39) || $0 == 0x2D || $0 == 0x5F || $0 == 0x2E }
}

public actor ConductorStore {
    private let root: URL
    private let files: StoreFiles
    private let protection: StoreProtection
    private let caches: HTTPCaches
    private let now: @Sendable () -> Date
    private var generation = 0
    private var started = false

    /** A store under root (`<Application Support>/conductor` on a phone). */
    public init(root: URL, files: StoreFiles, protection: StoreProtection, caches: HTTPCaches, now: @escaping @Sendable () -> Date = { Date() }) {
        self.root = root
        self.files = files
        self.protection = protection
        self.caches = caches
        self.now = now
    }

    private func refuse(_ code: String, _ words: String) -> StoreRefusal {
        StoreRefusal(code: code, words: words)
    }

    private func dir(_ area: StoreArea) -> URL { root.appendingPathComponent(area.rawValue, isDirectory: true) }
    private func file(_ area: StoreArea, _ key: String) -> URL { dir(area).appendingPathComponent(key) }
    private var generationFile: URL { root.appendingPathComponent("generation") }

    /** Writes data to url through `<url>.new` and a rename: a write cut before the rename leaves the old file whole. */
    private func replace(_ url: URL, with data: Data) throws {
        let next = url.appendingPathExtension("new")
        try files.write(data, to: next)
        try protection.protect(next)
        try files.rename(next, to: url)
    }

    /** Makes the folders, out of backups; reads the generation; removes what a write cut before its rename left (`.new`). */
    private func ready() throws {
        if started { return }
        try files.createDirectory(root)
        try protection.excludeFromBackup(root)
        for area in StoreArea.allCases { try files.createDirectory(dir(area)) }
        if let data = try files.read(generationFile), let g = Int(String(decoding: data, as: UTF8.self)) { generation = g }
        for area in StoreArea.allCases {
            for url in try files.list(dir(area)) where url.pathExtension == "new" { try? files.remove(url) }
        }
        started = true
    }

    private func area(_ name: String) throws -> StoreArea {
        guard let a = StoreArea(rawValue: name) else { throw refuse("invalid_area", "No area \(name) in the store.") }
        return a
    }

    /** A record's file as read: nil when there is none, it cannot be read, or it is from before the last clear (drafts, cache). */
    private func load(_ area: StoreArea, _ key: String) -> RecordFile? {
        guard let data = try? files.read(file(area, key)), let r = try? JSONDecoder.store.decode(RecordFile.self, from: data) else { return nil }
        if area != .servers && r.g != generation {
            try? files.remove(file(area, key))
            return nil
        }
        return r
    }

    /** The keys of an area's records that are read as present, with their records. */
    private func records(_ area: StoreArea) throws -> [(key: String, record: RecordFile)] {
        try files.list(dir(area)).filter { $0.pathExtension != "new" }.compactMap { url in
            let key = url.lastPathComponent
            guard validStoreKey(key), let r = load(area, key) else { return nil }
            return (key, r)
        }.sorted { $0.key < $1.key }
    }

    /** Must be run before anything else: the folders made and what a cut write left removed. */
    public func start() throws {
        try ready()
    }

    /**
     * Keeps data under key in area. A draft past 12 KiB is refused `too_large`, a 33rd draft `full` (a draft replaced is not one more);
     * a cache record past 1 MiB `too_large`, and the cache's oldest records evicted to make room, never a draft; the servers area holds
     * 5 records of at most 12 KiB. A write that fails is `not_saved`: never reported kept.
     */
    public func put(area name: String, key: String, data: String) throws {
        try ready()
        let area = try area(name)
        guard validStoreKey(key) else { throw refuse("invalid_key", "A key is 1 to \(StoreBounds.keyLength) of a-z, 0-9, '-', '_' and '.'.") }
        let size = data.utf8.count
        let present = try records(area)
        let replacing = present.contains { $0.key == key }
        switch area {
        case .drafts:
            guard size <= StoreBounds.draftBytes else { throw refuse("too_large", "A draft is at most \(StoreBounds.draftBytes) bytes.") }
            guard replacing || present.count < StoreBounds.drafts else { throw refuse("full", "This phone keeps \(StoreBounds.drafts) drafts already.") }
        case .servers:
            guard size <= StoreBounds.serverBytes else { throw refuse("too_large", "A server's record is at most \(StoreBounds.serverBytes) bytes.") }
            guard replacing || present.count < StoreBounds.servers else { throw refuse("full", "The servers area holds \(StoreBounds.servers) records.") }
        case .cache:
            guard size <= StoreBounds.cacheBytes else { throw refuse("too_large", "A cached record is at most \(StoreBounds.cacheBytes) bytes.") }
            var others = present.filter { $0.key != key }.sorted { ($0.record.savedAt, $0.key) < ($1.record.savedAt, $1.key) }
            var total = others.reduce(0) { $0 + $1.record.data.utf8.count }
            while total + size > StoreBounds.cacheBytes, let oldest = others.first {
                try files.remove(file(.cache, oldest.key))
                total -= oldest.record.data.utf8.count
                others.removeFirst()
            }
        }
        do {
            try replace(file(area, key), with: try JSONEncoder.store.encode(RecordFile(g: generation, savedAt: now(), data: data)))
        } catch {
            throw refuse("not_saved", "The record could not be kept on this phone.")
        }
    }

    /** The record under key, or nil. */
    public func get(area name: String, key: String) throws -> StoredRecord? {
        try ready()
        let area = try area(name)
        guard validStoreKey(key), let r = load(area, key) else { return nil }
        return StoredRecord(data: r.data, savedAt: r.savedAt)
    }

    /** The area's keys with when each was kept, by key. */
    public func list(area name: String) throws -> [(key: String, savedAt: Date)] {
        try ready()
        return try records(try area(name)).map { ($0.key, $0.record.savedAt) }
    }

    /** Removes the record under key, if any. */
    public func remove(area name: String, key: String) throws {
        try ready()
        let area = try area(name)
        guard validStoreKey(key) else { return }
        try files.remove(file(area, key))
    }

    /**
     * A sign-out: the generation moved first (so a record a removal misses is never read again), then every record of the drafts and
     * the cache removed and the HTTP caches emptied, before it answers. The servers area is the person's own and stays.
     */
    public func clear() async throws {
        try ready()
        generation += 1
        try replace(generationFile, with: Data(String(generation).utf8))
        for area in [StoreArea.drafts, .cache] {
            for url in (try? files.list(dir(area))) ?? [] { try? files.remove(url) }
        }
        await caches.clear()
    }
}

extension JSONEncoder {
    static var store: JSONEncoder {
        let e = JSONEncoder()
        e.dateEncodingStrategy = .millisecondsSince1970
        e.outputFormatting = .sortedKeys
        return e
    }
}

extension JSONDecoder {
    static var store: JSONDecoder {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .millisecondsSince1970
        return d
    }
}
