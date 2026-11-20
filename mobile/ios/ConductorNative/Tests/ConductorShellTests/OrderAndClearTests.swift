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

/** A transport that holds each request until the test lets it go, recording what reached it. */
final class HeldTransport: HTTPTransport, @unchecked Sendable {
    let lock = NSLock()
    var sent: [String] = []
    var waiting: [CheckedContinuation<Void, Never>] = []
    func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        await withCheckedContinuation { (c: CheckedContinuation<Void, Never>) in
            lock.withLock {
                sent.append(request.url!.path)
                waiting.append(c)
            }
        }
        return (Data(), HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: [:])!)
    }
    var count: Int { lock.withLock { sent.count } }
    func release() { lock.withLock { () -> [CheckedContinuation<Void, Never>] in defer { waiting = [] }; return waiting }.forEach { $0.resume() } }
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

    @Test(.timeLimit(.minutes(1))) func theNativeHTTPCallsRunOneAtATimeInTheOrderTheyArrive() async {
        let transport = HeldTransport()
        let calls = NativeHTTPCalls(origins: origins(["https://relay.shell.test"]), transport: transport)
        let runner = CallRunner { NativeHTTPRunner(calls) }
        let got = Answers()
        runner.submit("get", ["url": "https://relay.shell.test/first"], answer: got.add("first"))
        runner.submit("get", ["url": "https://relay.shell.test/second"], answer: got.add("second"))
        // The first is held at the transport: the second waits its turn and has not been sent.
        await until { transport.count == 1 }
        #expect(runner.queued == 1)
        #expect(transport.lock.withLock { transport.sent } == ["/first"])
        // Let every held request go, each turn, until both are answered (an order broken holds nothing back for long).
        await until {
            transport.release()
            return got.count == 2
        }
        #expect(got.names == ["first", "second"])
        #expect(transport.lock.withLock { transport.sent } == ["/first", "/second"])
    }
}
