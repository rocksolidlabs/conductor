import Capacitor
import ConductorPackaging
import ConductorShell
import UIKit
import WebKit

// The shell's bridge view controller, with one gate at each way from the page into native code (docs/plans/phone-shell.md, "Every
// way a page reaches native code"), each deciding on who sent a thing, every field native code reads, the spelling native code uses
// and what leaves the app by it:
// - the runtime's script message handler taken over by ConductorShell's BridgeGateHandler: only the main frame of this view at the
//   app's own origin, only a call of the closed table (BridgeGate.calls) in the runtime's own forms;
// - the web view's navigation delegate wrapped in NavigationGate: the app's own page on to the runtime, a link to the web handed to
//   iOS, a frame only its own document, nothing else loaded or handed on;
// - the UI delegate wrapped in MediaCaptureRefused: every media capture denied, every text-input prompt answered nil, every new
//   window decided by the load gate and never opened in the shell;
// - content rules (FilePrefix) admitting only the bundle's spelling of the app's own scheme and never the runtime's prefixes, and
//   the shell's router (ShellRouter) answering only files inside the bundle's folder;
// - a server base path a page once asked to keep never loaded.
// Behind the gates, the key, store and HTTP plugins registered, and the runtime's own WebView and CapacitorCookies replaced by
// refusing stand-ins as a second wall. The page loads only once every gate stands.
class ShellViewController: CAPBridgeViewController {
    /** The wrapping delegates: the web view holds its delegates weakly, so the controller keeps them. */
    private var refused: MediaCaptureRefused?
    /** The boot check's report, only in a launch the boot check asked for. */
    private var bootCheck: BootCheckView?
    /** The gate at the runtime's script message handler: none until it is in place, and then the page may load. */
    private var gate: BridgeGateHandler?

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
        guard let app = bridge?.config.localURL, let webView else { return }
        let d = MediaCaptureRefused(wrapping: webView.uiDelegate as? (NSObject & WKUIDelegate))
        refused = d
        webView.uiDelegate = d
        // The runtime's handler, `bridge`, taken over by the gate, which forwards to it only what it admits.
        if let runtime = (bridge as? CapacitorBridge)?.webViewDelegationHandler {
            let g = BridgeGateHandler(forwardingTo: runtime, view: webView, app: app)
            runtime.contentController.removeScriptMessageHandler(forName: BridgeGate.handler)
            runtime.contentController.add(g, name: BridgeGate.handler)
            gate = g
        }
        if BootCheck.asked(), let gate { bootCheck = BootCheckView(webView: webView, gate: gate) }
    }

    /**
     * The page loads (the runtime's viewDidLoad) only once its gates stand: the bridge gate and the load gate in place and the content
     * rules compiled and added. Any missing, nothing loads.
     */
    override func viewDidLoad() {
        guard gate != nil, let controller = (bridge as? CapacitorBridge)?.webViewDelegationHandler.contentController else { return }
        WKContentRuleListStore.default().compileContentRuleList(forIdentifier: FilePrefix.identifier, encodedContentRuleList: FilePrefix.rules) { list, _ in
            DispatchQueue.main.async {
                guard let list else { return }
                controller.add(list)
                super.viewDidLoad()
            }
        }
    }
}

