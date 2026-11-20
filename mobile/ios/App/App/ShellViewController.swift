import Capacitor
import ConductorPackaging
import ConductorShell
import UIKit
import WebKit

// The shell's bridge view controller: the key and store plugins registered with the native runtime; the runtime's own WebView,
// CapacitorCookies and CapacitorHttp replaced by their names (RefusedPlugins.swift, ShellHttpPlugin.swift); a server base path a
// page once asked to keep never loaded at start; and the web view's UI delegate wrapped so that every media capture request (a
// microphone, a camera) is denied (ConductorShell's MediaCaptureRefused).
class ShellViewController: CAPBridgeViewController {
    /** The wrapping delegate: the web view holds its UI delegate weakly, so the controller keeps it. */
    private var refused: MediaCaptureRefused?

    /** The bundle the app ships, always: the runtime's default reads a base path a page asked to keep; the shell's never does. */
    override func instanceDescriptor() -> InstanceDescriptor {
        InstanceDescriptor()
    }

    override func capacitorDidLoad() {
        // A packaging's native sources (its signing policy) are linked whatever they hold: the module is named here.
        _ = ConductorPackaging.linked()
        bridge?.registerPluginInstance(ConductorKeyPlugin())
        bridge?.registerPluginInstance(ConductorStorePlugin())
        bridge?.registerPluginInstance(RefusedWebViewPlugin())
        bridge?.registerPluginInstance(RefusedCookiesPlugin())
        bridge?.registerPluginInstance(ShellHttpPlugin())
        let d = MediaCaptureRefused(wrapping: webView?.uiDelegate as? (NSObject & WKUIDelegate))
        refused = d
        webView?.uiDelegate = d
    }
}
