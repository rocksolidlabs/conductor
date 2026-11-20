import ConductorStore
import Foundation
import Testing

@testable import ConductorShell

// A clear the page asks for while the store cannot start still removes what it can and answers not_cleared; the native HTTP call's
// calls run one at a time in the order they arrive.

/** The phone's files, except that the generation file's read fails while `fail` is set (a protected file while the phone is locked). */
final class LockedGeneration: StoreFiles, @unchecked Sendable {
    let disk = DiskFiles()
    let lock = NSLock()
    var fail = false
    func createDirectory(_ url: URL, complete: Bool) throws { try disk.createDirectory(url, complete: complete) }
    func write(_ data: Data, to url: URL, complete: Bool) throws { try disk.write(data, to: url, complete: complete) }
    func rename(_ from: URL, to: URL) throws { try disk.rename(from, to: to) }
    func read(_ url: URL) throws -> Data? {
        if lock.withLock({ fail }) && url.lastPathComponent == "generation" { throw CocoaError(.fileReadNoPermission) }
        return try disk.read(url)
    }
    func remove(_ url: URL) throws { try disk.remove(url) }
    func list(_ directory: URL) throws -> [URL] { try disk.list(directory) }
}

final class Clears: HTTPCaches, @unchecked Sendable {
    let lock = NSLock()
    var count = 0
    func clear() async { lock.withLock { count += 1 } }
}

@Suite struct OrderAndClearTests {
    @Test func aClearWhoseStoreCannotStartRemovesWhatItCanAndAnswersNotCleared() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("shell-locked-\(UUID().uuidString)", isDirectory: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let files = LockedGeneration()
        let first = StoreCalls(store: ConductorStore(root: root, files: files, protection: NoProtection(), caches: Clears()))
        #expect(await first.call("put", ["area": "drafts", "key": "d1", "data": "the first sign-in's draft"]) == .resolve([:]))
        #expect(await first.call("put", ["area": "cache", "key": "c1", "data": "the first sign-in's inbox"]) == .resolve([:]))
        // The app started again while the phone is locked: the generation cannot be read, and the page's first call is a clear.
        files.lock.withLock { files.fail = true }
        let caches = Clears()
        let locked = StoreCalls(store: ConductorStore(root: root, files: files, protection: NoProtection(), caches: caches))
        #expect(code(await locked.call("clear")) == "not_cleared")
        #expect(try FileManager.default.contentsOfDirectory(atPath: root.appendingPathComponent("drafts").path).isEmpty)
        #expect(try FileManager.default.contentsOfDirectory(atPath: root.appendingPathComponent("cache").path).isEmpty)
        #expect(!FileManager.default.fileExists(atPath: root.appendingPathComponent("generation").path))
        #expect(caches.lock.withLock { caches.count } == 1)
        // Unlocked and started again: nothing of the first sign-in is read.
        files.lock.withLock { files.fail = false }
        let next = StoreCalls(store: ConductorStore(root: root, files: files, protection: NoProtection(), caches: Clears()))
        #expect(await next.call("list", ["area": "drafts"]) == .resolve(["keys": .list([])]))
        #expect(await next.call("get", ["area": "cache", "key": "c1"]) == .resolve(["data": .null]))
    }

}
