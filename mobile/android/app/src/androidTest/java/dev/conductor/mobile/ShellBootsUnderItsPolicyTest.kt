package dev.conductor.mobile

import android.content.ComponentName
import android.content.Intent
import android.content.pm.ApplicationInfo
import android.net.Uri
import android.content.pm.PackageManager
import android.os.Build
import android.util.Log
import android.webkit.WebView
import androidx.test.core.app.ActivityScenario
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.webkit.WebViewCompat
import org.json.JSONTokener
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference

/**
 * The plan's boot check on Android (docs/plans/phone-shell.md, ShellBootsUnderItsPolicyTests; OSS-3): the built app, started on an
 * emulator with no provider, passes its own policy (ShellPolicy: it would otherwise load nothing), draws Open a server under the
 * bundle's own page policy with no violation, with the native layer's plugins in the page (the bridge's platform `android`, which it
 * reports only when every plugin it needs is there), and every known way around the shell's gates refused, while the calls the shell
 * means still answer: a message to a plugin or a method outside the table, of the Cordova or the log kind, with a callback id that is
 * no callback id, from a frame; a location change away from the bundle with no tap; a request under the local server's prefixes, in
 * another spelling or climbing out of the bundle; a text-input prompt; a window a script opens; the runtime's JavaScript interfaces.
 * The gates' own counts are read from the app, and the page's own loads and calls before the probes are none of them. Each report is
 * logged as it is read, so the run's log says how far the check got.
 */
@RunWith(AndroidJUnit4::class)
class ShellBootsUnderItsPolicyTest {
    companion object {
        const val TAG = "ConductorBootCheck"
        const val PREFIX = "90000000"
        /** A provider's id outside the path's alphabet, and the segment the shell's screens carry it in (decisionPath, web/app/utils/shell.ts). */
        const val DECISION_ID = "org/repo#12 a:b~é"
        const val DECISION_SEGMENT = "b3JnL3JlcG8jMTIgYTpifsOp"

        /** Added before the page's own scripts: every policy violation, every uncaught error and every console error or warning kept. */
        val VIOLATIONS = """
            window.__bootViolations = [];
            window.__bootConsole = [];
            document.addEventListener('securitypolicyviolation', (e) => window.__bootViolations.push(e.violatedDirective + ' ' + e.blockedURI));
            window.addEventListener('error', (e) => window.__bootConsole.push('error: ' + e.message + ' at ' + e.filename + ':' + e.lineno));
            window.addEventListener('unhandledrejection', (e) => window.__bootConsole.push('rejection: ' + String(e.reason && (e.reason.message || e.reason.code) || e.reason)));
            for (const level of ['error', 'warn']) {
              const was = console[level];
              console[level] = function (...a) { try { window.__bootConsole.push(level + ': ' + a.map(String).join(' ').slice(0, 300)) } catch (e) {} return was.apply(this, a) };
            }
        """.trimIndent()

        val PROBES = """
            (() => {
              window.__bootProbes = '';
              const failed = [];
              const check = (name, ok, seen) => { if (!ok) failed.push(seen === undefined ? name : name + ':' + seen) };
              const within = (p, ms) => Promise.race([p, new Promise((_, no) => setTimeout(() => no({ code: 'no_answer' }), ms || 20000))]);
              const code = async (f, ms) => { try { await within(f(), ms); return 'answered' } catch (e) { return (e && e.code) || 'rejected' } };
              const value = async (f) => { try { return await within(f()) } catch (e) { return null } };
              const image = (src) => within(new Promise((r) => { const i = new Image(); i.onload = () => r('loaded'); i.onerror = () => r('refused'); i.src = src })).catch(() => 'no_answer');
              const P = (window.Capacitor && window.Capacitor.Plugins) || {};
              const post = (o) => window.androidBridge.postMessage(JSON.stringify(o));
              const id = (n) => '$PREFIX' + String(n).padStart(2, '0');
              (async () => {
                // Each step is marked in the page and said on its console, which a debug build keeps in the device's log with its time.
                const at = (step) => { window.__bootStep = step; console.info('boot probe step: ' + step) };
                at('interfaces');
                const before = location.href;
                check('interfaces', typeof window.CapacitorCookiesAndroidInterface === 'undefined' && typeof window.CapacitorHttpAndroidInterface === 'undefined');
                post({ pluginId: 'WebView', methodName: 'getServerBasePath', options: {}, callbackId: id(1) });
                post({ pluginId: 'CapacitorCookies', methodName: 'getCookies', options: {}, callbackId: id(2) });
                post({ pluginId: 'SystemBars', methodName: 'hide', options: {}, callbackId: id(3) });
                post({ type: 'cordova', service: 'Device', action: 'getDeviceInfo', actionArgs: '[]', callbackId: id(4) });
                post({ pluginId: 'PushNotifications', methodName: 'getDeliveredNotifications', options: {}, callbackId: id(5) });
                post({ pluginId: 'ConductorKey', methodName: 'state', options: {}, callbackId: id(6) + "'}); window.__bootInjected = 1; ({a:'" });
                post({ pluginId: 'App', methodName: 'addListener', options: {}, callbackId: id(7) });
                post({ pluginId: 'Browser', methodName: 'open', options: { url: 'conductor-probe://boot-probe-browser' }, callbackId: id(8) });
                post({ type: 'js.error', error: { message: 'boot probe' } });
                at('srcdoc frame');
                // A frame of the page's own (a srcdoc frame, as the diagram's is): it draws, and its call, if the channel is in it at all, is dropped.
                const frame = document.createElement('iframe');
                frame.srcdoc = '<p id="drawn">drawn</p>';
                const drawn = within(new Promise((r) => { frame.onload = () => r(true) })).catch(() => false);
                document.body.appendChild(frame);
                await drawn;
                check('srcdoc', !!(frame.contentDocument && frame.contentDocument.getElementById('drawn')));
                window.__bootFramePosted = false;
                try {
                  const w = frame.contentWindow;
                  check('frame-interfaces', typeof w.CapacitorCookiesAndroidInterface === 'undefined' && typeof w.CapacitorHttpAndroidInterface === 'undefined');
                  if (w.androidBridge) {
                    w.androidBridge.postMessage(JSON.stringify({ pluginId: 'ConductorKey', methodName: 'state', options: {}, callbackId: id(10) }));
                    window.__bootFramePosted = true;
                  }
                } catch (e) { check('frame-post', false, String(e)) }
                // The local server: its prefixes, plain and escaped, a climb out of the bundle, and a decision's address in its own spelling,
                // none served; the same decision's address as the screens carry it goes on (not refused: the native count says so).
                at('local server');
                check('file', (await image('/_capacitor_file_/system/etc/hosts')) === 'refused');
                check('content', (await image('/_capacitor_content_/media/external/images/media/1')) === 'refused');
                check('file-escaped', (await image('/%5Fcapacitor_file_/system/etc/hosts')) === 'refused');
                check('climb', (await image('/%2E%2E%2Fcapacitor.config.json')) === 'refused');
                await image('/shell/decisions/$DECISION_SEGMENT');
                await image('/shell/decisions/' + encodeURIComponent(${org.json.JSONObject.quote(DECISION_ID)}));
                // No prompt answers, and no window opens on a script's own.
                at('prompt');
                check('prompt', prompt('boot probe') === null);
                at('window');
                check('window', window.open('https://example.com/?boot-probe-window') === null);
                // The calls the shell means still answer, the native HTTP call is still the shell's own, and the runtime's WebView is not reached.
                at('calls');
                check('http', (await code(() => P.CapacitorHttp.request({ url: 'https://unnamed.example/' }))) === 'origin_refused');
                const key = await code(() => P.ConductorKey.state());
                check('key', key === 'answered', key);
                check('store', Array.isArray(((await value(() => P.ConductorStore.list({ area: 'drafts' }))) || {}).keys));
                at('runtime webview');
                const web = await code(() => P.WebView.getServerBasePath(), 10000);
                check('webview', web === 'no_answer', web);
                check('injected', window.__bootInjected === undefined);
                check('location', location.href === before);
                check('platform', !!window.conductorShell && window.conductorShell.platform === 'android', window.conductorShell && window.conductorShell.platform);
                at('done');
                window.__bootProbes = failed.length ? 'failed:' + failed.join(',') : 'ok';
              })().catch((e) => { window.__bootProbes = 'threw:' + e });
              return 1;
            })()
        """.trimIndent()

        /** The main-frame loads the test makes with no tap: each cancelled by the load gate, the page staying where it was. */
        val AWAY = listOf("conductor-probe://boot-probe-location", "https://example.com/?boot-probe-away", "intent://boot-probe#Intent;scheme=probe;end")
    }

    private fun step(what: String) {
        Log.i(TAG, what)
    }

    private fun webView(s: ActivityScenario<MainActivity>): WebView {
        val out = AtomicReference<WebView>()
        s.onActivity { out.set(it.getBridge().webView) }
        return out.get()
    }

    private fun <T> read(s: ActivityScenario<MainActivity>, f: (MainActivity) -> T): T {
        val out = AtomicReference<T>()
        s.onActivity { out.set(f(it)) }
        return out.get()
    }

    /** The page's answer to a script, as the web view hands it back (JSON); each with a bound of its own. */
    private fun eval(s: ActivityScenario<MainActivity>, js: String): String {
        val done = CountDownLatch(1)
        val out = AtomicReference("null")
        s.onActivity { a -> a.getBridge().webView.evaluateJavascript(js) { v -> out.set(v ?: "null"); done.countDown() } }
        assertTrue("the page did not answer: $js", done.await(30, TimeUnit.SECONDS))
        return out.get()
    }

    private fun text(json: String): String = (JSONTokener(json).nextValue() as? String) ?: ""

    /** Polls until the condition holds or the bound passes; each poll's own wait is bounded too. A failure says what `diagnose` reads. */
    private fun until(seconds: Long, what: String, diagnose: () -> String = { "" }, condition: () -> Boolean) {
        val deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(seconds)
        while (System.nanoTime() < deadline) {
            if (condition()) return
            Thread.sleep(250)
        }
        throw AssertionError("not within $seconds s: $what ${diagnose()}")
    }

    /** Where the page is and what it said: the web view's address, the probes' last step, its console, the gates' counts. Logged too. */
    private fun diagnosis(s: ActivityScenario<MainActivity>, gates: ShellGates): String {
        val url = read(s) { it.getBridge().webView.url }
        val page = try {
            eval(s, "JSON.stringify({href: location.href, ready: document.readyState, step: window.__bootStep, probes: window.__bootProbes, " +
                "console: window.__bootConsole, violations: window.__bootViolations})")
        } catch (e: AssertionError) {
            "no answer from the page"
        }
        val said = "(the web view at $url; the page: $page; dropped ${gates.dropped.get()}, refused ${gates.refused.get()}, " +
            "cancelled ${gates.cancelled.get()}, handed ${gates.handedOff.get()})"
        step("diagnosis: $said")
        return said
    }

    @Test
    fun theShellStartsOnOpenAServerUnderItsPolicyWithItsNativeLayer() {
        ActivityScenario.launch(MainActivity::class.java).use { s ->
            val stopped = read(s) { it.stopped }
            step("the shell's policy: ${stopped ?: "held"}")
            assertNull("the shell stopped: $stopped", stopped)
            until(30, "the gates in place") { read(s) { it.gates != null } }
            val drawn = { eval(s, "!!document.querySelector('[data-shell-open-server]')") == "true" }
            until(180, "Open a server drawn") { drawn() }
            step("Open a server drawn")
            // The page again, with a listener for policy violations added before its own scripts.
            s.onActivity { a ->
                val w = a.getBridge().webView
                WebViewCompat.addDocumentStartJavaScript(w, VIOLATIONS, setOf(APP))
                w.reload()
            }
            until(30, "the reload begun") { eval(s, "typeof window.__bootViolations") == "\"object\"" }
            until(180, "Open a server drawn again") { drawn() }
            val gates = read(s) { it.gates!! }
            val ownDropped = gates.dropped.get()
            val ownRefused = gates.refused.get()
            step("the page's own calls dropped: $ownDropped, its own requests refused: $ownRefused")
            // The load gate: each main-frame load away from the bundle with no tap, waited on until the gate has cancelled it.
            for (to in AWAY) {
                val n = gates.cancelled.get()
                eval(s, "location.href = ${org.json.JSONObject.quote(to)}; 1")
                until(30, "the load to $to cancelled") { gates.cancelled.get() > n }
                step("cancelled: $to")
            }
            eval(s, PROBES)
            // The probes' own waits bound each one (20 s for each of eleven, 10 s for one): the report comes within their sum, and this
            // bound only bounds a hang. A page that is no longer the one the probes run in (its globals gone) fails at once, saying where it is.
            until(300, "the probes' report", { diagnosis(s, gates) }) {
                val now = eval(s, "typeof window.__bootProbes === 'string' ? window.__bootProbes : null")
                if (now == "null") throw AssertionError("the page the probes ran in is gone: ${diagnosis(s, gates)}")
                text(now).isNotEmpty()
            }
            val probes = text(eval(s, "window.__bootProbes"))
            val framePosted = eval(s, "window.__bootFramePosted") == "true"
            val violations = eval(s, "window.__bootViolations.length")
            val platform = text(eval(s, "window.conductorShell ? window.conductorShell.platform : 'none'"))
            val page = text(eval(s, "location.href"))
            step("probes: $probes; the frame had the channel: $framePosted; violations: ${eval(s, "JSON.stringify(window.__bootViolations)")}")
            // 9 messages posted, the srcdoc frame's when it had the channel, and the call to the runtime's WebView through its page script.
            val expectedDrops = ownDropped + 10 + (if (framePosted) 1 else 0)
            until(30, "the gate's drops counted") { gates.dropped.get() >= expectedDrops }
            val report = "ready platform=$platform probes=$probes own-dropped=$ownDropped own-refused=$ownRefused " +
                "dropped=${gates.dropped.get() - ownDropped} refused=${gates.refused.get() - ownRefused} cancelled=${gates.cancelled.get()} " +
                "handed=${gates.handedOff.get()} violations=$violations page-own=${LoadGate.own(page)}"
            step("the boot check's report: $report")
            assertEquals(
                "ready platform=android probes=ok own-dropped=0 own-refused=0 dropped=${10 + (if (framePosted) 1 else 0)} refused=5 " +
                    "cancelled=${AWAY.size} handed=0 violations=0 page-own=true",
                report,
            )
            assertNull("the open-source build holds a signing policy", ConductorKey.packagedPolicy())
        }
    }

    @Test
    fun anIntentKeepsItsIdentityAndLosesWhatAnotherAppCouldCarryIn() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val component = ComponentName(context, MainActivity::class.java)
        // The launcher's intent (a scenario's, the system's) is the same intent after the shell has cleaned it.
        val launcher = Intent.makeMainActivity(component)
        assertTrue("the launcher's intent changed identity", launcher.filterEquals(sanitized(launcher, component)))
        // What another app could carry in: a scheme of its own, a preference for the runtime, a grant: gone; a tap's handle kept.
        val sent = Intent(Intent.ACTION_VIEW, Uri.parse("conductor-probe://x"))
            .setComponent(component)
            .putExtra("DisableDeploy", true)
            .putExtra(TapData.MESSAGE_ID, "0:1")
            .putExtra(TapData.HANDLE, "h-1")
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        val clean = sanitized(sent, component)
        assertNull(clean.data)
        assertEquals(setOf(TapData.MESSAGE_ID, TapData.HANDLE), clean.extras?.keySet()?.toSet())
        assertEquals("h-1", clean.getStringExtra(TapData.HANDLE))
        assertEquals(0, clean.flags and Intent.FLAG_GRANT_READ_URI_PERMISSION)
        assertEquals("https://push.example/d/1", sanitized(Intent(Intent.ACTION_VIEW, Uri.parse("https://push.example/d/1")), component).dataString)
    }

    @Test
    fun theBuiltAppExportsOnlyWhatATapNeedsAndIsBackedUpNowhere() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val pm = context.packageManager
        val flags = PackageManager.GET_ACTIVITIES or PackageManager.GET_RECEIVERS or PackageManager.GET_SERVICES or PackageManager.GET_PROVIDERS
        val info = if (Build.VERSION.SDK_INT >= 33) {
            pm.getPackageInfo(context.packageName, PackageManager.PackageInfoFlags.of(flags.toLong()))
        } else {
            @Suppress("DEPRECATION")
            pm.getPackageInfo(context.packageName, flags)
        }
        val components = mutableListOf<Exported.Component>()
        info.activities?.forEach { components.add(Exported.Component(it.name, it.exported, it.permission)) }
        info.receivers?.forEach { components.add(Exported.Component(it.name, it.exported, it.permission)) }
        info.services?.forEach { components.add(Exported.Component(it.name, it.exported, it.permission)) }
        info.providers?.forEach { components.add(Exported.Component(it.name, it.exported, it.readPermission)) }
        step("components: " + components.joinToString { "${it.name}${if (it.exported) " (exported, ${it.permission})" else ""}" })
        assertEquals(emptyList<String>(), Exported.findings(components))
        val app = context.applicationInfo
        assertEquals("backups", 0, app.flags and ApplicationInfo.FLAG_ALLOW_BACKUP)
        assertEquals("cleartext", 0, app.flags and ApplicationInfo.FLAG_USES_CLEARTEXT_TRAFFIC)
        val permissions = pm.getPackageInfo(context.packageName, PackageManager.GET_PERMISSIONS).requestedPermissions?.toList() ?: emptyList()
        step("permissions: $permissions")
        for (p in permissions) {
            assertTrue("$p asked for", !p.contains("CAMERA") && !p.contains("RECORD_AUDIO") && !p.contains("LOCATION") && !p.contains("STORAGE"))
        }
    }
}
