import ConductorStore
import Foundation
import Testing

@testable import ConductorShell

// A plugin's calls one at a time, in the order they arrive: a clear held at a gate in the HTTP caches while later calls queue behind it.

/** HTTP caches whose clear waits at a gate the test opens, and says when it has started waiting. */
final class GatedCaches: HTTPCaches, @unchecked Sendable {
    let lock = NSLock()
    var entered = false
    var open = false
    var gate: CheckedContinuation<Void, Never>?
    func clear() async {
        await withCheckedContinuation { (c: CheckedContinuation<Void, Never>) in
            let now = lock.withLock { () -> Bool in
                entered = true
                if open { return true }
                gate = c
                return false
            }
            if now { c.resume() }
        }
    }
    var waiting: Bool { lock.withLock { entered } }
    func release() {
        let g = lock.withLock { () -> CheckedContinuation<Void, Never>? in
            open = true
            defer { gate = nil }
            return gate
        }
        g?.resume()
    }
}

/** The answers a runner gave, in the order it gave them. */
final class Answers: @unchecked Sendable {
    let lock = NSLock()
    var got: [(name: String, answer: CallAnswer)] = []
    func add(_ name: String) -> (CallAnswer) -> Void { { a in self.lock.withLock { self.got.append((name, a)) } } }
    var count: Int { lock.withLock { got.count } }
    var names: [String] { lock.withLock { got.map(\.name) } }
}

/** Waits for a state the code reaches, giving every other task its turn between looks. */
func until(_ ready: () -> Bool) async {
    while !ready() { await Task.yield() }
}

@Suite struct RunnerTests {
    @Test(.timeLimit(.minutes(1))) func callsRunOneAtATimeInTheOrderTheyArrive() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("shell-runner-\(UUID().uuidString)", isDirectory: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let caches = GatedCaches()
        let store = ConductorStore(root: root, files: DiskFiles(), protection: NoProtection(), caches: caches)
        let runner = CallRunner { StoreCalls(store: store) }
        let got = Answers()
        runner.submit("put", ["area": "drafts", "key": "a", "data": "1"], answer: got.add("put a 1"))
        runner.submit("put", ["area": "drafts", "key": "a", "data": "2"], answer: got.add("put a 2"))
        runner.submit("clear", [:], answer: got.add("clear"))
        runner.submit("put", ["area": "drafts", "key": "b", "data": "x"], answer: got.add("put b x"))
        runner.submit("put", ["area": "drafts", "key": "b", "data": "y"], answer: got.add("put b y"))
        // The clear waits at the gate: the two puts after it wait their turn, and nothing of them is written.
        await until { caches.waiting }
        #expect(runner.queued == 2)
        #expect(got.names == ["put a 1", "put a 2"])
        #expect(!FileManager.default.fileExists(atPath: root.appendingPathComponent("drafts/b").path))
        caches.release()
        await until { got.count == 5 }
        #expect(got.names == ["put a 1", "put a 2", "clear", "put b x", "put b y"])
        #expect(got.got.allSatisfy { $0.answer == .resolve([:]) })
        // The puts before the clear were removed by it; the two after it landed in order.
        #expect(try await store.get(area: "drafts", key: "a") == nil)
        #expect(try await store.get(area: "drafts", key: "b")?.data == "y")
    }

    @Test(.timeLimit(.minutes(1))) func callsWhoseCallsCannotBeOpenedAreEachAnsweredNative() async {
        struct Unopenable: Error {}
        let runner = CallRunner { throw Unopenable() }
        let got = Answers()
        runner.submit("state", [:], answer: got.add("state"))
        runner.submit("create", [:], answer: got.add("create"))
        await until { got.count == 2 }
        #expect(got.names == ["state", "create"])
        #expect(got.got.allSatisfy { if case .reject(let code, _) = $0.answer { return code == "native" } else { return false } })
    }

    @Test func theRefusedPluginsAnswerEveryCallRefused() {
        for (plugin, methods) in [("WebView", RefusedPlugins.webView), ("CapacitorCookies", RefusedPlugins.cookies)] {
            #expect(!methods.isEmpty)
            for m in methods {
                guard case .reject(let code, let words) = RefusedPlugins.answer(plugin, m) else {
                    Issue.record("\(plugin).\(m) was not refused")
                    continue
                }
                #expect(code == "not_in_shell" && words.contains("\(plugin).\(m)"))
            }
        }
        #expect(RefusedPlugins.webView.contains("setServerBasePath") && RefusedPlugins.webView.contains("persistServerBasePath"))
    }
}
