package dev.conductor.mobile

import org.json.JSONObject

/**
 * The key's calls as the bridge makes them (mobile/src/bridge.ts, `NativeKey`): create({until}), publicKey(), state(), bind({id}),
 * sign({request}), ending(), remove(). The key's start (a keystore key with no record, an unbound one past its time, both removed) runs
 * before the first call, and again before the next until it has once succeeded.
 */
class KeyCalls(val key: ConductorKey) : ShellCalls {
    private var started = false

    override fun call(method: String, options: JSONObject): CallAnswer = try {
        if (!started) {
            key.start()
            started = true
        }
        when (method) {
            "create" -> {
                val until = (options.opt("until") as? String)?.let { IsoTime.parse(it) }
                if (until == null) invalid("create takes until, an ISO 8601 time.") else CallAnswer.Resolve(JSONObject().put("publicKey", key.create(until)))
            }
            "publicKey" -> CallAnswer.Resolve(JSONObject().put("publicKey", key.publicKey() ?: JSONObject.NULL))
            "state" -> {
                val (state, id, until) = key.state()
                val out = JSONObject().put("state", state.word)
                if (id != null) out.put("id", id)
                if (until != null) out.put("until", IsoTime.format(until))
                CallAnswer.Resolve(out)
            }
            "bind" -> {
                val id = options.opt("id") as? String
                if (id == null) {
                    invalid("bind takes id, a string.")
                } else {
                    key.bind(id)
                    CallAnswer.Resolve(JSONObject())
                }
            }
            "sign" -> {
                val request = options.opt("request") as? JSONObject
                if (request == null) invalid("sign takes request, an object.") else CallAnswer.Resolve(JSONObject().put("signature", key.sign(request)))
            }
            "ending" -> {
                key.ending()
                CallAnswer.Resolve(JSONObject())
            }
            "remove" -> {
                key.remove()
                CallAnswer.Resolve(JSONObject())
            }
            else -> CallAnswer.Reject("unknown_call", "The key has no call $method.")
        }
    } catch (e: Exception) {
        refusalOf(e)
    }

    companion object {
        /** The calls the key plugin offers the page, by the names the bridge calls them: the plugin declares exactly these. */
        val METHODS = listOf("create", "publicKey", "state", "bind", "sign", "ending", "remove")
    }
}
