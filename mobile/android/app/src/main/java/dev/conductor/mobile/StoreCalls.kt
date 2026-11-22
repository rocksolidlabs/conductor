package dev.conductor.mobile

import org.json.JSONArray
import org.json.JSONObject

/**
 * The store's calls as the bridge makes them (mobile/src/bridge.ts, `NativeStore`): put({area, key, data}), get({area, key}),
 * list({area}), remove({area, key}), clear(). The store's start (its folders made, a half-written record removed) runs before the
 * first call, and again before the next until it has once succeeded; a clear is let past a start that fails, removes what it can
 * itself, and answers `not_cleared`.
 */
class StoreCalls(val store: ConductorStore) : ShellCalls {
    private var started = false

    override fun call(method: String, options: JSONObject): CallAnswer {
        val area = options.opt("area") as? String
        val key = options.opt("key") as? String
        return try {
            if (!started) {
                try {
                    store.start()
                    started = true
                } catch (e: Exception) {
                    if (method != "clear") throw e
                }
            }
            when (method) {
                "put" -> {
                    val data = options.opt("data") as? String
                    if (area == null || key == null || data == null) return invalid("put takes area, key and data, strings.")
                    store.put(area, key, data)
                    CallAnswer.Resolve(JSONObject())
                }
                "get" -> {
                    if (area == null || key == null) return invalid("get takes area and key, strings.")
                    val r = store.get(area, key) ?: return CallAnswer.Resolve(JSONObject().put("data", JSONObject.NULL))
                    CallAnswer.Resolve(JSONObject().put("data", r.data).put("savedAt", IsoTime.format(r.savedAt)))
                }
                "list" -> {
                    if (area == null) return invalid("list takes area, a string.")
                    val keys = JSONArray()
                    for ((k, at) in store.list(area)) keys.put(JSONObject().put("key", k).put("savedAt", IsoTime.format(at)))
                    CallAnswer.Resolve(JSONObject().put("keys", keys))
                }
                "remove" -> {
                    if (area == null || key == null) return invalid("remove takes area and key, strings.")
                    store.remove(area, key)
                    CallAnswer.Resolve(JSONObject())
                }
                "clear" -> {
                    store.clear()
                    CallAnswer.Resolve(JSONObject())
                }
                else -> CallAnswer.Reject("unknown_call", "The store has no call $method.")
            }
        } catch (e: Exception) {
            refusalOf(e)
        }
    }

    companion object {
        /** The calls the store plugin offers the page, by the names the bridge calls them: the plugin declares exactly these. */
        val METHODS = listOf("put", "get", "list", "remove", "clear")
    }
}
