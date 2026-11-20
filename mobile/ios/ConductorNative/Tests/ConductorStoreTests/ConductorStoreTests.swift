import Foundation
import Testing

@testable import ConductorStore

// The store's rules (docs/plans/phone-shell.md, Tests per surface, `ConductorStoreTests` and the store's bounds), over a file system
// held in memory with seams that stop a write between its `.new` and its rename and that refuse a removal, a protection that records
// what it was asked, HTTP caches that count their clears, and a clock the test moves; and once over the real file system in a
// temporary folder.

/** A file system in memory: `stopRename` makes every rename throw (a write cut there), `keep` makes those removals fail. */
final class MemoryFiles: StoreFiles, @unchecked Sendable {
    let lock = NSLock()
    var files: [String: Data] = [:]
    var dirs: Set<String> = []
    var stopRename = false
    var keep: Set<String> = []
    /** Each write's and each folder's name with whether it was asked to be made under complete protection. */
    var made: [(name: String, complete: Bool)] = []
    /** Names whose write fails (a full disk for that file), each as many times as it is listed. */
    var failWrites: [String] = []
    func createDirectory(_ url: URL, complete: Bool) throws {
        lock.withLock {
            dirs.insert(url.path)
            made.append((url.lastPathComponent, complete))
        }
    }
    func write(_ data: Data, to url: URL, complete: Bool) throws {
        try lock.withLock {
            if let i = failWrites.firstIndex(of: url.lastPathComponent) {
                failWrites.remove(at: i)
                throw CocoaError(.fileWriteOutOfSpace)
            }
            files[url.path] = data
            made.append((url.lastPathComponent, complete))
        }
    }
    func rename(_ from: URL, to: URL) throws {
        try lock.withLock {
            if stopRename { throw CocoaError(.fileWriteUnknown) }
            files[to.path] = files.removeValue(forKey: from.path)
        }
    }
    func read(_ url: URL) throws -> Data? { lock.withLock { files[url.path] } }
    func remove(_ url: URL) throws {
        try lock.withLock {
            if keep.contains(url.lastPathComponent) { throw CocoaError(.fileWriteNoPermission) }
            files.removeValue(forKey: url.path)
        }
    }
    func list(_ directory: URL) throws -> [URL] {
        lock.withLock { files.keys.filter { URL(fileURLWithPath: $0).deletingLastPathComponent().path == directory.path }.map { URL(fileURLWithPath: $0) } }
    }
    func names(_ area: String) -> [String] {
        lock.withLock { files.keys.filter { $0.contains("/\(area)/") }.map { URL(fileURLWithPath: $0).lastPathComponent }.sorted() }
    }
}

final class RecordedProtection: StoreProtection, @unchecked Sendable {
    let lock = NSLock()
    var protected: [String] = []
    var excluded: [String] = []
    var fail = false
    func protect(_ file: URL) throws {
        try lock.withLock {
            if fail { throw CocoaError(.fileWriteNoPermission) }
            protected.append(file.lastPathComponent)
        }
    }
    func excludeFromBackup(_ directory: URL) throws { lock.withLock { excluded.append(directory.path) } }
}

final class CountedCaches: HTTPCaches, @unchecked Sendable {
    let lock = NSLock()
    var clears = 0
    func clear() async { lock.withLock { clears += 1 } }
}

final class MovingClock: @unchecked Sendable {
    let lock = NSLock()
    var t = Date(timeIntervalSince1970: 1_800_000_000)
    func now() -> Date { lock.withLock { t } }
    func tick() { lock.withLock { t = t.addingTimeInterval(1) } }
}

struct StoreFixture {
    let files = MemoryFiles()
    let protection = RecordedProtection()
    let caches = CountedCaches()
    let clock = MovingClock()
    let root = URL(fileURLWithPath: "/phone/conductor", isDirectory: true)
    let store: ConductorStore
    init() {
        let c = clock
        store = ConductorStore(root: root, files: files, protection: protection, caches: caches, now: { c.now() })
    }
    /** A store over the same files: the app started again. */
    func restarted() -> ConductorStore {
        let c = clock
        return ConductorStore(root: root, files: files, protection: protection, caches: caches, now: { c.now() })
    }
}

func code(_ f: () async throws -> Void) async -> String? {
    do {
        try await f()
        return nil
    } catch let r as StoreRefusal {
        return r.code
    } catch {
        return "other: \(error)"
    }
}

@Suite struct ConductorStoreTests {
    @Test func keepsAndReadsARecordWithItsTime() async throws {
        let f = StoreFixture()
        try await f.store.put(area: "drafts", key: "i1", data: #"{"choice":1}"#)
        let r = try #require(try await f.store.get(area: "drafts", key: "i1"))
        #expect(r.data == #"{"choice":1}"#)
        #expect(r.savedAt == f.clock.now())
        #expect(try await f.store.list(area: "drafts").map(\.key) == ["i1"])
        try await f.store.remove(area: "drafts", key: "i1")
        #expect(try await f.store.get(area: "drafts", key: "i1") == nil)
    }

    @Test func refusesADraftPastItsBoundAndA33rdNeverShowingItKept() async throws {
        let f = StoreFixture()
        #expect(await code { try await f.store.put(area: "drafts", key: "big", data: String(repeating: "x", count: StoreBounds.draftBytes + 1)) } == "too_large")
        #expect(try await f.store.get(area: "drafts", key: "big") == nil)
        try await f.store.put(area: "drafts", key: "exact", data: String(repeating: "é", count: StoreBounds.draftBytes / 2))
        for i in 1..<StoreBounds.drafts { try await f.store.put(area: "drafts", key: "d\(i)", data: "{}") }
        #expect(await code { try await f.store.put(area: "drafts", key: "one-more", data: "{}") } == "full")
        #expect(try await f.store.get(area: "drafts", key: "one-more") == nil)
        // A draft replaced is not one more.
        try await f.store.put(area: "drafts", key: "d1", data: #"{"choice":0}"#)
        #expect(try await f.store.list(area: "drafts").count == StoreBounds.drafts)
    }

    @Test func evictsTheCachesOldestToMakeRoomAndNeverADraft() async throws {
        let f = StoreFixture()
        try await f.store.put(area: "drafts", key: "kept", data: "draft")
        let third = String(repeating: "c", count: StoreBounds.cacheBytes / 3)
        for k in ["a", "b", "c"] {
            try await f.store.put(area: "cache", key: k, data: third)
            f.clock.tick()
        }
        try await f.store.put(area: "cache", key: "d", data: third)
        #expect(try await f.store.list(area: "cache").map(\.key) == ["b", "c", "d"])
        #expect(try await f.store.get(area: "drafts", key: "kept")?.data == "draft")
        #expect(await code { try await f.store.put(area: "cache", key: "huge", data: String(repeating: "x", count: StoreBounds.cacheBytes + 1)) } == "too_large")
        #expect(try await f.store.list(area: "cache").map(\.key) == ["b", "c", "d"])
    }

    @Test func aWriteCutBeforeItsRenameLeavesTheOldRecordWhole() async throws {
        let f = StoreFixture()
        try await f.store.put(area: "drafts", key: "i1", data: "old")
        f.files.stopRename = true
        #expect(await code { try await f.store.put(area: "drafts", key: "i1", data: "new") } == "not_saved")
        #expect(try await f.store.get(area: "drafts", key: "i1")?.data == "old")
        // A rename that failed removed the new file; one the app was stopped before (a crash) leaves it.
        #expect(f.files.names("drafts") == ["i1"])
        f.files.lock.withLock { f.files.files[f.root.appendingPathComponent("drafts/i1.new").path] = Data("cut".utf8) }
        #expect(f.files.names("drafts") == ["i1", "i1.new"])
        #expect(try await f.store.list(area: "drafts").map(\.key) == ["i1"])
        // The app started again: what the cut write left is removed, the old record stays.
        f.files.stopRename = false
        let again = f.restarted()
        try await again.start()
        #expect(f.files.names("drafts") == ["i1"])
        #expect(try await again.get(area: "drafts", key: "i1")?.data == "old")
    }

    @Test func clearEmptiesTheDraftsTheCacheAndTheHTTPCachesAndLeavesTheServers() async throws {
        let f = StoreFixture()
        try await f.store.put(area: "drafts", key: "i1", data: "draft")
        try await f.store.put(area: "cache", key: "inbox", data: "list")
        try await f.store.put(area: "servers", key: "recent", data: #"["https://desk.example.test/"]"#)
        try await f.store.clear()
        #expect(f.caches.clears == 1)
        #expect(f.files.names("drafts").isEmpty)
        #expect(f.files.names("cache").isEmpty)
        #expect(try await f.store.list(area: "drafts").isEmpty)
        #expect(try await f.store.get(area: "servers", key: "recent")?.data == #"["https://desk.example.test/"]"#)
    }

    @Test func aRecordAClearCouldNotRemoveIsNeverReadUnderTheNextSignIn() async throws {
        let f = StoreFixture()
        try await f.store.put(area: "drafts", key: "i1", data: "the first sign-in's draft")
        try await f.store.put(area: "cache", key: "inbox", data: "the first sign-in's inbox")
        f.files.keep = ["i1", "inbox"]
        #expect(await code { try await f.store.clear() } == "not_cleared")
        #expect(f.caches.clears == 1)
        #expect(f.files.names("drafts") == ["i1"])
        #expect(try await f.store.get(area: "drafts", key: "i1") == nil)
        #expect(try await f.store.list(area: "cache").isEmpty)
        // Started again over the same files: the generation is read back, and the record is still not read.
        f.files.keep = []
        let again = f.restarted()
        #expect(try await again.get(area: "drafts", key: "i1") == nil)
        try await again.put(area: "drafts", key: "i2", data: "the next sign-in's draft")
        #expect(try await again.list(area: "drafts").map(\.key) == ["i2"])
    }

    @Test func aClearWhoseGenerationCannotBeWrittenStillRemovesAndLeavesNothingToRead() async throws {
        let f = StoreFixture()
        try await f.store.put(area: "drafts", key: "i1", data: "the first sign-in's draft")
        try await f.store.put(area: "drafts", key: "i2", data: "another")
        try await f.store.put(area: "cache", key: "inbox", data: "the first sign-in's inbox")
        // The generation cannot be written, before the removal or after it, and one draft cannot be removed.
        f.files.lock.withLock { f.files.failWrites = ["generation.new", "generation.new"] }
        f.files.keep = ["i1"]
        #expect(await code { try await f.store.clear() } == "not_cleared")
        #expect(f.caches.clears == 1)
        #expect(f.files.names("drafts") == ["i1"])
        #expect(f.files.names("cache").isEmpty)
        #expect(try f.files.read(f.root.appendingPathComponent("generation")) == nil)
        #expect(try await f.store.get(area: "drafts", key: "i1") == nil)
        // Started again over the same files, with no generation on disk: nothing of the first sign-in is read.
        f.files.keep = []
        let again = f.restarted()
        #expect(try await again.get(area: "drafts", key: "i1") == nil)
        #expect(try await again.list(area: "drafts").isEmpty)
        try await again.put(area: "drafts", key: "i3", data: "the next sign-in's draft")
        #expect(try await f.restarted().list(area: "drafts").map(\.key) == ["i3"])
    }

    @Test func aGenerationWriteThatFailsOnceIsWrittenAgainAfterTheRemoval() async throws {
        let f = StoreFixture()
        try await f.store.put(area: "drafts", key: "i1", data: "draft")
        f.files.lock.withLock { f.files.failWrites = ["generation.new"] }
        try await f.store.clear()
        #expect(f.files.names("drafts").isEmpty)
        #expect(try f.files.read(f.root.appendingPathComponent("generation")) == Data("1".utf8))
        #expect(f.caches.clears == 1)
    }

    @Test func refusesAKeyOutsideItsAlphabetAndAnAreaItDoesNotHave() async throws {
        let f = StoreFixture()
        for bad in ["", "UPPER", "a/b", "..", ".", "a b", String(repeating: "a", count: 65), "x.new", "é"] {
            #expect(await code { try await f.store.put(area: "drafts", key: bad, data: "{}") } == "invalid_key")
        }
        try await f.store.put(area: "drafts", key: "a-z_0.9", data: "{}")
        #expect(await code { try await f.store.put(area: "keys", key: "a", data: "{}") } == "invalid_area")
    }

    @Test func holdsTheServersAreaToItsOwnBounds() async throws {
        let f = StoreFixture()
        for i in 1...StoreBounds.servers { try await f.store.put(area: "servers", key: "s\(i)", data: "x") }
        #expect(await code { try await f.store.put(area: "servers", key: "s9", data: "x") } == "full")
        #expect(await code { try await f.store.put(area: "servers", key: "s1", data: String(repeating: "x", count: StoreBounds.serverBytes + 1)) } == "too_large")
        // The cache's eviction never takes a server's record.
        try await f.store.put(area: "cache", key: "big", data: String(repeating: "c", count: StoreBounds.cacheBytes))
        #expect(try await f.store.list(area: "servers").count == StoreBounds.servers)
    }

    @Test func protectsEveryRecordAndKeepsTheFolderOutOfBackups() async throws {
        let f = StoreFixture()
        try await f.store.put(area: "drafts", key: "i1", data: "{}")
        try await f.store.put(area: "cache", key: "c1", data: "{}")
        #expect(f.protection.protected == ["generation.new", "i1.new", "c1.new"])
        #expect(f.protection.excluded == [f.root.path])
    }

    @Test func makesEveryFileAndFolderUnderCompleteProtectionAndLeavesNoFileAProtectFailedOn() async throws {
        let f = StoreFixture()
        try await f.store.put(area: "drafts", key: "i1", data: "the draft's text")
        let made = f.files.lock.withLock { f.files.made }
        #expect(!made.isEmpty && made.allSatisfy(\.complete))
        #expect(made.map(\.name).contains("i1.new") && made.map(\.name).contains("drafts"))
        f.protection.lock.withLock { f.protection.fail = true }
        #expect(await code { try await f.store.put(area: "drafts", key: "i2", data: "another draft's text") } == "not_saved")
        #expect(f.files.names("drafts") == ["i1"])
        #expect(try await f.store.get(area: "drafts", key: "i2") == nil)
    }

    @Test func writesThroughTheRealFileSystem() async throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("conductor-store-\(UUID().uuidString)", isDirectory: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = ConductorStore(root: dir, files: DiskFiles(), protection: DeviceProtection(), caches: CountedCaches())
        try await store.put(area: "drafts", key: "i1", data: "one")
        try await store.put(area: "drafts", key: "i1", data: "two")
        #expect(try await store.get(area: "drafts", key: "i1")?.data == "two")
        let names = try FileManager.default.contentsOfDirectory(atPath: dir.appendingPathComponent("drafts").path)
        #expect(names == ["i1"])
        #expect(try dir.resourceValues(forKeys: [.isExcludedFromBackupKey]).isExcludedFromBackup == true)
        try await store.clear()
        #expect(try await store.get(area: "drafts", key: "i1") == nil)
    }
}
