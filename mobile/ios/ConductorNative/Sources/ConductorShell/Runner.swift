import Foundation

/** What a plugin hands its calls to: KeyCalls and StoreCalls. */
public protocol ShellCalls: Actor {
    func call(_ method: String, _ options: [String: Any]) async -> CallAnswer
}

extension KeyCalls: ShellCalls {}
extension StoreCalls: ShellCalls {}

/**
 * One plugin's calls run one at a time, in the order they arrive: each is made and answered before the next one starts, so two puts
 * of one key land in order, a put made before a clear is removed by it, and one made while a clear waits runs after it has answered.
 * `submit` is synchronous and is made from the native runtime's own queue for the plugin, which hands the page's calls over in the
 * order the page made them. What the plugin's calls are made of is opened once, at the first call; one that cannot be opened answers
 * every call `native`.
 */
public final class CallRunner: @unchecked Sendable {
    private struct Job {
        let method: String
        let options: [String: Any]
        let answer: (CallAnswer) -> Void
    }

    /** The count of calls submitted and not yet started, kept apart so that the runner's one task does not hold the runner. */
    private final class Count: @unchecked Sendable {
        let lock = NSLock()
        var n = 0
        func add(_ d: Int) { lock.withLock { n += d } }
        var value: Int { lock.withLock { n } }
    }

    private let jobs: AsyncStream<Job>.Continuation
    private let waiting = Count()

    /** Calls submitted and not yet started: what waits its turn. */
    public var queued: Int { waiting.value }

    public init(opening open: @escaping @Sendable () throws -> ShellCalls) {
        var continuation: AsyncStream<Job>.Continuation!
        let stream = AsyncStream<Job>(bufferingPolicy: .unbounded) { continuation = $0 }
        jobs = continuation
        Task { [waiting] in
            let calls = Result { try open() }
            for await job in stream {
                waiting.add(-1)
                switch calls {
                case .success(let c): job.answer(await c.call(job.method, job.options))
                case .failure(let e): job.answer(unopened(e))
                }
            }
        }
    }

    deinit { jobs.finish() }

    /** Queues one call; answer is called with its answer once every call before it has been answered. */
    public func submit(_ method: String, _ options: [String: Any], answer: @escaping (CallAnswer) -> Void) {
        waiting.add(1)
        jobs.yield(Job(method: method, options: options, answer: answer))
    }
}
