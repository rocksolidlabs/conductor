package dev.conductor.mobile

import android.content.Context
import android.os.Handler
import android.os.Looper
import android.webkit.CookieManager
import android.webkit.WebStorage
import android.webkit.WebView
import android.webkit.WebViewDatabase
import java.io.File
import java.io.FileOutputStream
import java.io.IOException
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

// The store's seams as a phone gives them: the file system (a rename over the old record, atomic within a folder) and the shell's HTTP
// caches. DiskFiles is plain Java I/O, tested on the JVM; WebCaches is the web view's.

/** The file system through java.io: each write synced to the disk before its rename, the rename rename(2), which replaces in one step. */
class DiskFiles : StoreFiles {
    override fun createDirectory(dir: File) {
        if (!dir.isDirectory && !dir.mkdirs() && !dir.isDirectory) throw IOException("the folder could not be made")
    }

    override fun write(data: ByteArray, to: File) {
        FileOutputStream(to).use { out ->
            out.write(data)
            out.fd.sync()
        }
    }

    override fun rename(from: File, to: File) {
        if (!from.renameTo(to)) throw IOException("the record could not be put in place")
    }

    override fun read(file: File): ByteArray? = if (!file.exists()) null else file.readBytes()

    override fun remove(file: File) {
        if (file.exists() && !file.delete() && file.exists()) throw IOException("the record could not be removed")
    }

    override fun list(dir: File): List<File> = dir.listFiles()?.toList() ?: throw IOException("the folder could not be read")
}

/**
 * The shell's HTTP caches and what the web view keeps: its cookies, its storage, its saved HTTP credentials and its cache (the shell's
 * web view loads its bundle alone; the native HTTP call keeps no cookie, cache or credential of its own). Each is the web view's, so
 * the work is done on the main thread and waited for, with a bound.
 */
class WebCaches(private val context: Context, private val webView: () -> WebView?) : HttpCaches {
    override fun clear() {
        val done = CountDownLatch(1)
        Handler(Looper.getMainLooper()).post {
            try {
                webView()?.clearCache(true)
                WebStorage.getInstance().deleteAllData()
                WebViewDatabase.getInstance(context).clearHttpAuthUsernamePassword()
                val cookies = CookieManager.getInstance()
                cookies.removeAllCookies { _ ->
                    cookies.flush()
                    done.countDown()
                }
            } catch (e: Exception) {
                done.countDown()
            }
        }
        done.await(30, TimeUnit.SECONDS)
    }
}

/** The store on this phone: under the app's no-backup folder (`conductor/`), its HTTP caches the web view's. */
fun deviceStore(context: Context, webView: () -> WebView?): ConductorStore =
    ConductorStore(File(context.noBackupFilesDir, "conductor"), DiskFiles(), WebCaches(context.applicationContext, webView))
