package dev.conductor.mobile

import org.json.JSONObject
import java.io.File

// The phone shell's store on Android (docs/plans/phone-shell.md, OSS-3: "the store's Android side"), the only place on the phone the
// shell keeps anything of a decision or a draft, as the iPhone's ConductorStore keeps it: one file per record under the app's no-backup
// folder (which no backup and no transfer to another phone takes), `conductor/<area>/<key>`, each written to `<key>.new` and renamed
// over the old one, so a write cut before its rename leaves the old record whole. Three areas with bounds of their own: drafts (32
// records of 12 KiB, never evicted), the cache (1 MiB in all, its oldest evicted first), and the shell's own servers (Open a server's
// last addresses). `clear()`, at a sign-out, empties the drafts and the cache and the HTTP caches before it answers, and leaves the
// servers. Each record carries the store's generation, which `clear()` moves first: a record a clear could not remove is from before
// it and is never read again. The file system, the clock and the HTTP caches are seams (`StoreFiles`, `HttpCaches`), so the rules are
// tested with fakes. Android has no file protection class like the iPhone's complete one: the records are in the app's
// credential-encrypted storage, unreadable before the phone's first unlock after a start.

/** The store's areas: what the bridge names. */
enum class StoreArea(val word: String) {
    DRAFTS("drafts"), CACHE("cache"), SERVERS("servers");

    companion object {
        fun of(word: String): StoreArea? = values().firstOrNull { it.word == word }
    }
}

/** A refusal of the store's: a code and its words, passed to the page as they are. */
class StoreRefusal(code: String, words: String) : Refusal(code, words)

/** The store's bounds, the iPhone's. */
object StoreBounds {
    const val DRAFTS = 32
    const val DRAFT_BYTES = 12 * 1024
    const val CACHE_BYTES = 1024 * 1024
    const val SERVERS = 5
    const val SERVER_BYTES = 12 * 1024
    const val KEY_LENGTH = 64
}

/** The file system the store writes through. `read` answers null for a file that is not there and throws for one it cannot read. */
interface StoreFiles {
    fun createDirectory(dir: File)
    fun write(data: ByteArray, to: File)
    fun rename(from: File, to: File)
    fun read(file: File): ByteArray?
    fun remove(file: File)
    fun list(dir: File): List<File>
}

/** The shell's HTTP caches: emptied at a sign-out, before it answers. */
interface HttpCaches {
    fun clear()
}

/** A record read: its text and when it was kept (milliseconds since 1970). */
data class StoredRecord(val data: String, val savedAt: Long)

/** Whether key is a store's key: 1 to 64 of a-z, 0-9, '-', '_', '.', and no name a file system gives meaning to. */
fun validStoreKey(key: String): Boolean {
    if (key.isEmpty() || key.length > StoreBounds.KEY_LENGTH || key == "." || key == ".." || key.endsWith(".new")) return false
    return key.all { it in 'a'..'z' || it in '0'..'9' || it == '-' || it == '_' || it == '.' }
}

class ConductorStore(
    private val root: File,
    private val files: StoreFiles,
    private val caches: HttpCaches,
    private val now: () -> Long = { System.currentTimeMillis() },
) {
    private var generation = 0L
    /** Whether the generation in memory is the one on disk: a record is written only under a generation the disk holds. */
    private var generationKept = false
    private var started = false

    private class RecordFile(val g: Long, val savedAt: Long, val data: String)

    private fun refuse(code: String, words: String) = StoreRefusal(code, words)
    private fun dir(area: StoreArea) = File(root, area.word)
    private fun file(area: StoreArea, key: String) = File(dir(area), key)
    private val generationFile get() = File(root, "generation")

    private fun encode(r: RecordFile): ByteArray =
        JSONObject().put("g", r.g).put("savedAt", r.savedAt).put("data", r.data).toString().toByteArray(Charsets.UTF_8)

    private fun decode(bytes: ByteArray): RecordFile? = try {
        val o = JSONObject(String(bytes, Charsets.UTF_8))
        val g = o.get("g")
        val savedAt = o.get("savedAt")
        val data = o.get("data")
        if (g is Number && savedAt is Number && data is String) RecordFile(g.toLong(), savedAt.toLong(), data) else null
    } catch (e: Exception) {
        null
    }

    /** Writes data to file through `<file>.new` and a rename: a write cut before the rename leaves the old file whole. */
    private fun replace(target: File, data: ByteArray) {
        val next = File(target.parentFile, target.name + ".new")
        try {
            files.write(data, next)
            files.rename(next, target)
        } catch (e: Exception) {
            try {
                files.remove(next)
            } catch (_: Exception) {
            }
            throw e
        }
    }

    /**
     * Makes the folders; reads the generation; removes what a write cut before its rename left (`.new`). With no generation on disk (a
     * fresh store, or a clear that could neither write nor keep it), every drafts or cache record there is of a sign-in a clear ended or
     * could not finish: each is removed, and the generation is taken past any that could not be, so none of them is read.
     */
    private fun ready() {
        if (started) return
        files.createDirectory(root)
        for (area in StoreArea.values()) files.createDirectory(dir(area))
        for (area in StoreArea.values()) {
            for (f in files.list(dir(area))) if (f.name.endsWith(".new")) runCatching { files.remove(f) }
        }
        val kept = files.read(generationFile)?.let { String(it, Charsets.UTF_8).toLongOrNull() }
        if (kept != null) {
            generation = kept
            generationKept = true
        } else {
            var past = 0L
            for (area in listOf(StoreArea.DRAFTS, StoreArea.CACHE)) {
                for (f in files.list(dir(area))) {
                    if (runCatching { files.remove(f) }.isSuccess) continue
                    val r = runCatching { files.read(f) }.getOrNull()?.let { decode(it) } ?: continue
                    past = maxOf(past, r.g + 1)
                }
            }
            generation = past
            generationKept = false
        }
        started = true
    }

    private fun areaOf(name: String): StoreArea = StoreArea.of(name) ?: throw refuse("invalid_area", "No area $name in the store.")

    /** A record's file as read: null when there is none, it cannot be read, or it is from before the last clear (drafts, cache). */
    private fun load(area: StoreArea, key: String): RecordFile? {
        val r = runCatching { files.read(file(area, key)) }.getOrNull()?.let { decode(it) } ?: return null
        if (area != StoreArea.SERVERS && r.g != generation) {
            runCatching { files.remove(file(area, key)) }
            return null
        }
        return r
    }

    /** The keys of an area's records that are read as present, with their records, by key. */
    private fun records(area: StoreArea): List<Pair<String, RecordFile>> =
        files.list(dir(area)).filter { !it.name.endsWith(".new") }.mapNotNull { f ->
            val key = f.name
            if (!validStoreKey(key)) null else load(area, key)?.let { key to it }
        }.sortedBy { it.first }

    /** Must be run before anything else: the folders made and what a cut write left removed. */
    @Synchronized
    fun start() = ready()

    /**
     * Keeps data under key in area. A draft past 12 KiB is refused `too_large`, a 33rd draft `full` (a draft replaced is not one more); a
     * cache record past 1 MiB `too_large`, and the cache's oldest records evicted to make room, never a draft; the servers area holds 5
     * records of at most 12 KiB. A write that fails is `not_saved`: never reported kept.
     */
    @Synchronized
    fun put(area: String, key: String, data: String) {
        ready()
        val a = areaOf(area)
        if (!validStoreKey(key)) throw refuse("invalid_key", "A key is 1 to ${StoreBounds.KEY_LENGTH} of a-z, 0-9, '-', '_' and '.'.")
        val size = utf8Size(data)
        val present = records(a)
        val replacing = present.any { it.first == key }
        when (a) {
            StoreArea.DRAFTS -> {
                if (size > StoreBounds.DRAFT_BYTES) throw refuse("too_large", "A draft is at most ${StoreBounds.DRAFT_BYTES} bytes.")
                if (!replacing && present.size >= StoreBounds.DRAFTS) throw refuse("full", "This phone keeps ${StoreBounds.DRAFTS} drafts already.")
            }
            StoreArea.SERVERS -> {
                if (size > StoreBounds.SERVER_BYTES) throw refuse("too_large", "A server's record is at most ${StoreBounds.SERVER_BYTES} bytes.")
                if (!replacing && present.size >= StoreBounds.SERVERS) throw refuse("full", "The servers area holds ${StoreBounds.SERVERS} records.")
            }
            StoreArea.CACHE -> {
                if (size > StoreBounds.CACHE_BYTES) throw refuse("too_large", "A cached record is at most ${StoreBounds.CACHE_BYTES} bytes.")
                val others = present.filter { it.first != key }
                    .sortedWith(compareBy<Pair<String, RecordFile>>({ it.second.savedAt }, { it.first })).toMutableList()
                var total = others.sumOf { utf8Size(it.second.data) }
                while (total + size > StoreBounds.CACHE_BYTES && others.isNotEmpty()) {
                    val oldest = others.removeAt(0)
                    files.remove(file(StoreArea.CACHE, oldest.first))
                    total -= utf8Size(oldest.second.data)
                }
            }
        }
        try {
            if (!generationKept) {
                replace(generationFile, generation.toString().toByteArray(Charsets.UTF_8))
                generationKept = true
            }
            replace(file(a, key), encode(RecordFile(generation, now(), data)))
        } catch (e: Exception) {
            throw refuse("not_saved", "The record could not be kept on this phone.")
        }
    }

    /** The record under key, or null. */
    @Synchronized
    fun get(area: String, key: String): StoredRecord? {
        ready()
        val a = areaOf(area)
        if (!validStoreKey(key)) return null
        return load(a, key)?.let { StoredRecord(it.data, it.savedAt) }
    }

    /** The area's keys with when each was kept, by key. */
    @Synchronized
    fun list(area: String): List<Pair<String, Long>> {
        ready()
        return records(areaOf(area)).map { it.first to it.second.savedAt }
    }

    /** Removes the record under key, if any. */
    @Synchronized
    fun remove(area: String, key: String) {
        ready()
        val a = areaOf(area)
        if (!validStoreKey(key)) return
        files.remove(file(a, key))
    }

    /**
     * A sign-out: the generation moved first (so a record a removal misses is never read again), then every record of the drafts and the
     * cache removed and the HTTP caches emptied, whatever the generation's write did, before it answers. A generation that could not be
     * written is written again after the removal (which makes room); one that still cannot be has its file removed, so the next start
     * reads none of what is left. The servers area is the person's own and stays. Anything not removed or not written is answered
     * `not_cleared`, after all the rest is done. It runs when the store cannot start too.
     */
    @Synchronized
    fun clear() {
        var whole = true
        var known = true
        try {
            ready()
        } catch (e: Exception) {
            known = false
            whole = false
        }
        if (known) generation += 1
        val bytes = generation.toString().toByteArray(Charsets.UTF_8)
        var kept = known && runCatching { replace(generationFile, bytes) }.isSuccess
        for (a in listOf(StoreArea.DRAFTS, StoreArea.CACHE)) {
            val listed = runCatching { files.list(dir(a)) }.getOrNull()
            if (listed == null) {
                whole = false
                continue
            }
            for (f in listed) if (runCatching { files.remove(f) }.isFailure) whole = false
        }
        caches.clear()
        if (known && !kept) kept = runCatching { replace(generationFile, bytes) }.isSuccess
        if (!kept) {
            // Neither written nor known: no generation on disk, so a start reads none of the drafts or the cache left.
            if (runCatching { files.remove(generationFile) }.isFailure) whole = false
        }
        generationKept = kept
        if (!kept || !whole) throw refuse("not_cleared", "Not everything of this sign-in could be removed from this phone.")
    }
}
