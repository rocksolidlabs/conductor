package dev.conductor.mobile

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.File
import java.io.IOException

// The Android store's rules over a fake file system and a fake clock (docs/plans/phone-shell.md, OSS-3, the store's Android side): the
// iPhone's areas and bounds, the write through `.new` and a rename, and a sign-out's clear that leaves nothing of one sign-in to be read
// under the next, whatever fails on the way.

/** Files in memory, with switches that make one kind of step fail. */
class MemoryFiles : StoreFiles {
    val files = sortedMapOf<String, ByteArray>()
    val dirs = sortedSetOf<String>()
    var failRename: (File) -> Boolean = { false }
    var failWrite: (File) -> Boolean = { false }
    var failRemove: (File) -> Boolean = { false }
    var failRead: (File) -> Boolean = { false }

    override fun createDirectory(dir: File) {
        dirs.add(dir.path)
    }

    override fun write(data: ByteArray, to: File) {
        if (failWrite(to)) throw IOException("write refused")
        files[to.path] = data.copyOf()
    }

    override fun rename(from: File, to: File) {
        if (failRename(to)) throw IOException("rename refused")
        files[to.path] = files.remove(from.path) ?: throw IOException("no such file")
    }

    override fun read(file: File): ByteArray? {
        if (failRead(file)) throw IOException("read refused")
        return files[file.path]?.copyOf()
    }

    override fun remove(file: File) {
        if (failRemove(file)) throw IOException("remove refused")
        files.remove(file.path)
    }

    override fun list(dir: File): List<File> = files.keys.map { File(it) }.filter { it.parentFile?.path == dir.path }

    fun names(area: String): List<String> = list(File("/s/$area")).map { it.name }.sorted()
}

class CountedCaches : HttpCaches {
    var clears = 0
    override fun clear() {
        clears++
    }
}

class ConductorStoreTest {
    @get:Rule
    val temp = TemporaryFolder()

    private var clock = 1_000_000L
    private val files = MemoryFiles()
    private val caches = CountedCaches()
    private fun store() = ConductorStore(File("/s"), files, caches) { clock }

    private fun refusal(code: String, f: () -> Unit) {
        try {
            f()
            fail("expected $code")
        } catch (e: StoreRefusal) {
            assertEquals(code, e.code)
        }
    }

    @Test
    fun keepsAndReadsARecordWithItsTime() {
        val s = store()
        s.put("drafts", "d1", "hello")
        assertEquals(StoredRecord("hello", clock), s.get("drafts", "d1"))
        assertEquals(listOf("d1" to clock), s.list("drafts"))
        s.remove("drafts", "d1")
        assertNull(s.get("drafts", "d1"))
    }

    @Test
    fun refusesADraftPastItsBoundAndA33rdNeverShowingItKept() {
        val s = store()
        refusal("too_large") { s.put("drafts", "big", "x".repeat(StoreBounds.DRAFT_BYTES + 1)) }
        assertNull(s.get("drafts", "big"))
        for (i in 0 until StoreBounds.DRAFTS) s.put("drafts", "d$i", "x")
        refusal("full") { s.put("drafts", "one-more", "x") }
        assertNull(s.get("drafts", "one-more"))
        // A draft replaced is not one more.
        s.put("drafts", "d0", "again")
        assertEquals("again", s.get("drafts", "d0")?.data)
    }

    @Test
    fun evictsTheCachesOldestToMakeRoomAndNeverADraft() {
        val s = store()
        s.put("drafts", "keep", "draft")
        val half = "x".repeat(StoreBounds.CACHE_BYTES / 2)
        s.put("cache", "a", half)
        clock += 1
        s.put("cache", "b", half)
        clock += 1
        s.put("cache", "c", half)
        assertNull(s.get("cache", "a"))
        assertEquals(half, s.get("cache", "b")?.data)
        assertEquals(half, s.get("cache", "c")?.data)
        assertEquals("draft", s.get("drafts", "keep")?.data)
        refusal("too_large") { s.put("cache", "huge", "x".repeat(StoreBounds.CACHE_BYTES + 1)) }
    }

    @Test
    fun aWriteCutBeforeItsRenameLeavesTheOldRecordWhole() {
        val s = store()
        s.put("drafts", "d1", "old")
        files.failRename = { it.name == "d1" }
        refusal("not_saved") { s.put("drafts", "d1", "new") }
        assertEquals("old", s.get("drafts", "d1")?.data)
        assertEquals(listOf("d1"), files.names("drafts"))
    }

    @Test
    fun aStartRemovesWhatAWriteCutBeforeItsRenameLeft() {
        store().put("drafts", "d1", "old")
        files.files["/s/drafts/d1.new"] = "half".toByteArray()
        val s = store()
        assertEquals("old", s.get("drafts", "d1")?.data)
        assertEquals(listOf("d1"), files.names("drafts"))
    }

    @Test
    fun clearEmptiesTheDraftsTheCacheAndTheHttpCachesAndLeavesTheServers() {
        val s = store()
        s.put("drafts", "d", "1")
        s.put("cache", "c", "2")
        s.put("servers", "recent", "3")
        s.clear()
        assertNull(s.get("drafts", "d"))
        assertNull(s.get("cache", "c"))
        assertEquals("3", s.get("servers", "recent")?.data)
        assertEquals(1, caches.clears)
        assertEquals(emptyList<String>(), files.names("drafts") + files.names("cache"))
    }

    @Test
    fun aRecordAClearCouldNotRemoveIsNeverReadUnderTheNextSignIn() {
        val s = store()
        s.put("drafts", "d", "secret")
        files.failRemove = { it.name == "d" }
        refusal("not_cleared") { s.clear() }
        files.failRemove = { false }
        assertNull(s.get("drafts", "d"))
        // A restart reads none of it either.
        files.files["/s/drafts/d"] = JSONObject().put("g", 0).put("savedAt", 1).put("data", "secret").toString().toByteArray()
        assertNull(store().get("drafts", "d"))
    }

    @Test
    fun aClearWhoseGenerationCannotBeWrittenStillRemovesAndLeavesNothingToRead() {
        val s = store()
        s.put("drafts", "d", "secret")
        s.put("cache", "c", "secret")
        files.failWrite = { it.name == "generation.new" }
        refusal("not_cleared") { s.clear() }
        assertEquals(emptyList<String>(), files.names("drafts") + files.names("cache"))
        assertEquals(1, caches.clears)
        assertFalse(files.files.containsKey("/s/generation"))
        files.failWrite = { false }
        assertTrue(store().list("drafts").isEmpty())
    }

    @Test
    fun aGenerationWriteThatFailsOnceIsWrittenAgainAfterTheRemoval() {
        val s = store()
        s.put("drafts", "d", "x")
        var tries = 0
        files.failWrite = { it.name == "generation.new" && tries++ == 0 }
        s.clear()
        assertEquals("1", String(files.files["/s/generation"]!!))
    }

    @Test
    fun aStartWithNoGenerationOnDiskRemovesEveryDraftAndCacheFile() {
        files.files["/s/drafts/d"] = JSONObject().put("g", 7).put("savedAt", 1).put("data", "old").toString().toByteArray()
        files.files["/s/cache/c"] = JSONObject().put("g", 7).put("savedAt", 1).put("data", "old").toString().toByteArray()
        files.files["/s/servers/recent"] = JSONObject().put("g", 7).put("savedAt", 1).put("data", "mine").toString().toByteArray()
        val s = store()
        assertTrue(s.list("drafts").isEmpty())
        assertTrue(s.list("cache").isEmpty())
        assertEquals("mine", s.get("servers", "recent")?.data)
        assertEquals(emptyList<String>(), files.names("drafts") + files.names("cache"))
    }

    @Test
    fun aRecordAStartCouldNotRemoveStillReadsAsStale() {
        files.files["/s/drafts/d"] = JSONObject().put("g", 7).put("savedAt", 1).put("data", "old").toString().toByteArray()
        files.failRemove = { it.name == "d" }
        val s = store()
        s.put("drafts", "n", "new")
        assertNull(s.get("drafts", "d"))
        assertEquals("new", s.get("drafts", "n")?.data)
    }

    @Test
    fun aClearWhoseStoreCannotStartRemovesWhatItCanAndAnswersNotCleared() {
        store().put("drafts", "d", "secret")
        files.failRead = { it.name == "generation" }
        val calls = StoreCalls(store())
        val a = calls.call("clear", JSONObject())
        assertTrue(a is CallAnswer.Reject)
        assertEquals("not_cleared", (a as CallAnswer.Reject).code)
        assertEquals(emptyList<String>(), files.names("drafts"))
        assertFalse(files.files.containsKey("/s/generation"))
        files.failRead = { false }
        assertTrue(store().list("drafts").isEmpty())
    }

    @Test
    fun aRecordThatCannotBeReadIsARefusalThatChangesNothing() {
        val s = store()
        s.put("drafts", "d", "kept")
        var once = true
        files.failRead = { it.name == "d" && once.also { once = false } }
        refusal("unreadable") { s.get("drafts", "d") }
        assertTrue(files.files.containsKey("/s/drafts/d"))
        assertEquals("kept", s.get("drafts", "d")?.data)
        // A put whose bounds need the area read refuses too, writing nothing.
        files.failRead = { it.name == "d" }
        refusal("unreadable") { s.put("drafts", "e", "x") }
        assertEquals(listOf("d"), files.names("drafts"))
    }

    @Test
    fun aStoreDoesNotStartOverAFileItCanNeitherRemoveNorReadAndClearsOnceItCanBeRemoved() {
        files.files["/s/drafts/d"] = JSONObject().put("g", 7).put("savedAt", 1).put("data", "old").toString().toByteArray()
        files.failRemove = { it.name == "d" }
        files.failRead = { it.name == "d" }
        refusal("unreadable") { store().get("drafts", "x") }
        // Still unreadable, but removable now: the store's start removes it (no generation on disk: every draft is stale), so the
        // store starts clean and the clear that follows is a true "cleared".
        files.failRemove = { false }
        val a = StoreCalls(store()).call("clear", JSONObject())
        assertTrue("the clear answered ${(a as? CallAnswer.Reject)?.code}", a is CallAnswer.Resolve)
        assertEquals(emptyList<String>(), files.names("drafts"))
        files.failRead = { false }
        val after = store()
        assertTrue(after.list("drafts").isEmpty())
        after.put("drafts", "n", "new")
        assertEquals("new", after.get("drafts", "n")?.data)
    }

    @Test
    fun aClearWhoseFileCanStillNeitherBeRemovedNorReadAnswersNotCleared() {
        files.files["/s/drafts/d"] = JSONObject().put("g", 7).put("savedAt", 1).put("data", "old").toString().toByteArray()
        files.failRemove = { it.name == "d" }
        files.failRead = { it.name == "d" }
        val a = StoreCalls(store()).call("clear", JSONObject())
        assertEquals("not_cleared", (a as CallAnswer.Reject).code)
        assertEquals(listOf("d"), files.names("drafts"))
        assertFalse(files.files.containsKey("/s/generation"))
    }

    @Test
    fun refusesAKeyOutsideItsAlphabetAndAnAreaItDoesNotHave() {
        val s = store()
        for (key in listOf("", ".", "..", "a/b", "A", "x.new", "é", "a b", "x".repeat(65))) refusal("invalid_key") { s.put("drafts", key, "x") }
        refusal("invalid_area") { s.put("keys", "k", "x") }
        assertTrue(validStoreKey("a-b_c.d9"))
    }

    @Test
    fun holdsTheServersAreaToItsOwnBounds() {
        val s = store()
        for (i in 0 until StoreBounds.SERVERS) s.put("servers", "s$i", "x")
        refusal("full") { s.put("servers", "more", "x") }
        refusal("too_large") { s.put("servers", "s0", "x".repeat(StoreBounds.SERVER_BYTES + 1)) }
        // A clear leaves them, and the cache's eviction never reaches them.
        s.clear()
        assertEquals(StoreBounds.SERVERS, s.list("servers").size)
    }

    @Test
    fun writesThroughTheRealFileSystem() {
        val root = File(temp.root, "conductor")
        val s = ConductorStore(root, DiskFiles(), caches)
        s.put("drafts", "d1", "on disk")
        assertEquals("on disk", ConductorStore(root, DiskFiles(), caches).get("drafts", "d1")?.data)
        assertFalse(File(root, "drafts/d1.new").exists())
        s.clear()
        assertNull(ConductorStore(root, DiskFiles(), caches).get("drafts", "d1"))
    }
}
