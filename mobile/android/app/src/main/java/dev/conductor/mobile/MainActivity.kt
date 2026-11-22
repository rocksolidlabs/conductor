package dev.conductor.mobile

import android.content.ActivityNotFoundException
import android.content.ComponentName
import android.content.Intent
import android.content.pm.ApplicationInfo
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.view.WindowManager
import android.widget.TextView
import androidx.webkit.WebViewFeature
import com.getcapacitor.BridgeActivity
import com.getcapacitor.CapConfig
import com.getcapacitor.ServerPath
import java.net.CookieHandler

/**
 * The Android shell's activity (docs/plans/phone-shell.md, OSS-3): the native runtime's own, with the shell's plugins registered
 * before its bridge is made, its configuration the shell's own (never a file the build copies), and its gates put in place before the
 * page's first load. At every start it checks its policy (ShellPolicy, the plan's boot check on the phone): the web view can tell the
 * shell's page from another, the build is not backed up and sends no cleartext, its plugins are the shell's, and the runtime started
 * as the shell configured it; any finding, and the shell loads nothing and says why. An intent from another app reaches it only as a
 * notification's tap or an https link (`sanitized`). Nothing of the page shows in the system's list of recent apps.
 */
class MainActivity : BridgeActivity() {
    /** Whether the app is in the foreground: a link is handed out of the shell only then. */
    @Volatile
    var foreground = false
        private set

    /** The gates in place, for the boot check's counts; none until they stand. */
    var gates: ShellGates? = null
        private set

    /** The words the shell stopped with, when its policy did not hold; none while it runs. */
    var stopped: String? = null
        private set

    override fun onCreate(savedInstanceState: Bundle?) {
        intent = sanitized(intent, ComponentName(this, MainActivity::class.java))
        registerPlugin(ConductorKeyPlugin::class.java)
        registerPlugin(ConductorStorePlugin::class.java)
        registerPlugin(ShellHttpPlugin::class.java)
        registerPlugin(RefusedWebViewPlugin::class.java)
        registerPlugin(RefusedCookiesPlugin::class.java)
        super.onCreate(savedInstanceState)
        if (Build.VERSION.SDK_INT >= 33) {
            setRecentsScreenshotEnabled(false)
        } else {
            window.setFlags(WindowManager.LayoutParams.FLAG_SECURE, WindowManager.LayoutParams.FLAG_SECURE)
        }
    }

    private fun debuggable(): Boolean = (applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE) != 0

    private fun asset(name: String): String? = try {
        assets.open(name).use { String(it.readBytes(), Charsets.UTF_8) }
    } catch (e: Exception) {
        null
    }

    override fun load() {
        val before = ShellPolicy.before(
            ShellPolicy.Before(
                messageListener = WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER),
                documentStartScript = WebViewFeature.isFeatureSupported(WebViewFeature.DOCUMENT_START_SCRIPT),
                allowBackup = (applicationInfo.flags and ApplicationInfo.FLAG_ALLOW_BACKUP) != 0,
                cleartext = (applicationInfo.flags and ApplicationInfo.FLAG_USES_CLEARTEXT_TRAFFIC) != 0,
                pluginsJson = asset("capacitor.plugins.json"),
            ),
        )
        if (before.isNotEmpty()) {
            stop(before)
            return
        }
        // A server path a page once asked the runtime to keep is never loaded: forgotten, and the bundle named as the one to serve.
        getSharedPreferences("CapWebViewSettings", MODE_PRIVATE).edit().remove("serverBasePath").commit()
        config = CapConfig.Builder(this)
            .setHostname("localhost")
            .setAndroidScheme("https")
            .setAllowNavigation(null)
            .setAllowMixedContent(false)
            .setUseLegacyBridge(false)
            .setLoggingEnabled(false)
            .setResolveServiceWorkerRequests(false)
            .setWebContentsDebuggingEnabled(debuggable())
            .create()
        // Named, the bundle's first load is posted to the main thread rather than made at once: it runs after the gates below.
        bridgeBuilder.setServerPath(ServerPath(ServerPath.PathType.ASSET_PATH, "public"))
        super.load()
        val b = bridge
        if (b == null) {
            stop(listOf("The shell's native runtime did not start."))
            return
        }
        gates = try {
            ShellGates.install(this, b)
        } catch (e: Exception) {
            stop(listOf("The shell's gates could not be put in place."))
            return
        }
        val c = b.config
        val after = ShellPolicy.after(
            ShellPolicy.After(
                scheme = c.androidScheme,
                host = c.hostname,
                serverUrl = c.serverUrl,
                allowNavigation = c.allowNavigation?.toList() ?: emptyList(),
                mixedContent = c.isMixedContentAllowed,
                legacyBridge = c.isUsingLegacyBridge,
                logging = c.isLoggingEnabled,
                serviceWorkers = c.isResolveServiceWorkerRequests,
                debugging = c.isWebContentsDebuggingEnabled,
                debuggable = debuggable(),
                cookieHandler = CookieHandler.getDefault() != null,
                plugins = ShellPolicy.PLUGINS.keys.associateWith { b.getPlugin(it)?.instance?.javaClass?.name },
            ),
        )
        if (after.isNotEmpty()) stop(after)
    }

    /** The shell stops: its page never loads (the web view is never shown, so the runtime's posted first load never runs), with words. */
    private fun stop(words: List<String>) {
        stopped = words.joinToString(" ")
        gates = null
        bridge?.webView?.stopLoading()
        val view = TextView(this)
        view.text = "Conductor cannot start on this phone. ${words.joinToString(" ")}"
        view.setPadding(48, 96, 48, 48)
        setContentView(view)
    }

    override fun onNewIntent(intent: Intent) {
        val clean = sanitized(intent, ComponentName(this, MainActivity::class.java))
        setIntent(clean)
        super.onNewIntent(clean)
    }

    override fun onResume() {
        super.onResume()
        foreground = true
    }

    override fun onPause() {
        foreground = false
        super.onPause()
    }

    /** A link the person activated, handed to the system as for a link tapped anywhere: the browser, or the app tied to that address. */
    fun handOff(url: String) {
        if (!foreground || !webAddress(url)) return
        val view = Intent(Intent.ACTION_VIEW, Uri.parse(url)).addCategory(Intent.CATEGORY_BROWSABLE)
        try {
            startActivity(view)
        } catch (e: ActivityNotFoundException) {
            // Nothing on the phone opens it: the shell's page stays as it was.
        }
    }
}
