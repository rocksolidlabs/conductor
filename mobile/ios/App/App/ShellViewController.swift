import Capacitor
import ConductorShell
import UIKit
import WebKit

// The shell's bridge view controller: the key and store plugins registered with the native runtime, and the web view's UI
// delegate wrapped so that every media capture request (a microphone, a camera) is denied (ConductorShell's MediaCaptureRefused).
class ShellViewController: CAPBridgeViewController {
    /** The wrapping delegate: the web view holds its UI delegate weakly, so the controller keeps it. */
    private var refused: MediaCaptureRefused?

    override func capacitorDidLoad() {
        bridge?.registerPluginInstance(ConductorKeyPlugin())
        bridge?.registerPluginInstance(ConductorStorePlugin())
        let d = MediaCaptureRefused(wrapping: webView?.uiDelegate as? (NSObject & WKUIDelegate))
        refused = d
        webView?.uiDelegate = d
    }
}
