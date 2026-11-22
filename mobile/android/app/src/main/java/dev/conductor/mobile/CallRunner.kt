package dev.conductor.mobile

import org.json.JSONObject
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicInteger

/**
 * One plugin's calls run one at a time, in the order they arrive: each is made and answered before the next one starts, so two puts of
 * one key land in order, a put made before a clear is removed by it, and one made while a clear waits runs after it has answered.
 * `submit` is made from the native runtime's own thread for plugin calls, which hands the page's calls over in the order the page made
 * them. What the calls are made of is opened once, on the runner's thread, at the first call; one that cannot be opened answers every
 * call `native`. A call waiting for the person (the key's presence check) holds the calls after it, as on the iPhone.
 */
class CallRunner(private val opener: () -> ShellCalls) {
    private val executor: ExecutorService = Executors.newSingleThreadExecutor { r -> Thread(r, "conductor-calls").apply { isDaemon = true } }
    private val waiting = AtomicInteger()
    private var opened: Result<ShellCalls>? = null

    /** Calls submitted and not yet started: what waits its turn. */
    val queued: Int get() = waiting.get()

    /** Queues one call; `answer` is called with its answer once every call before it has been answered. */
    fun submit(method: String, options: JSONObject, answer: (CallAnswer) -> Unit) {
        waiting.incrementAndGet()
        executor.execute {
            waiting.decrementAndGet()
            val calls = opened ?: runCatching { opener() }.also { opened = it }
            val a = calls.fold(
                onSuccess = { c ->
                    try {
                        c.call(method, options)
                    } catch (e: Exception) {
                        refusalOf(e)
                    }
                },
                onFailure = { e -> unopened(e) },
            )
            answer(a)
        }
    }
}
