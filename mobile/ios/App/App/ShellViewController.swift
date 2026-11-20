import Capacitor
import ConductorPackaging
import ConductorShell
import UIKit
import WebKit

// The shell's bridge view controller, with one gate at each way from the page into native code (docs/plans/phone-shell.md, "Every
// way a page reaches native code"): the runtime's script message handler taken over by ConductorShell's BridgeGateHandler, which
// forwards only a message to a plugin the shell means the page to reach, by its page name; the web view's UI delegate wrapped in
// MediaCaptureRefused (every media capture denied, every text-input prompt answered nil and handed to no one); the runtime's file
// prefixes blocked by content rules before the first load; a server base path a page once asked to keep never loaded. Behind the
// gate, the key and store plugins registered, and the runtime's own WebView, CapacitorCookies and CapacitorHttp replaced by their
// names (RefusedPlugins.swift, ShellHttpPlugin.swift) as a second wall. The page loads only once every gate stands.
class ShellViewController: CAPBridgeViewController {
    /** The wrapping delegate: the web view holds its UI delegate weakly, so the controller keeps it. */
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
        let d = MediaCaptureRefused(wrapping: webView?.uiDelegate as? (NSObject & WKUIDelegate))
        refused = d
        webView?.uiDelegate = d
        // The runtime's handler, `bridge`, taken over by the gate, which forwards to it only what it admits.
        if let runtime = (bridge as? CapacitorBridge)?.webViewDelegationHandler {
            let g = BridgeGateHandler(forwardingTo: runtime)
            runtime.contentController.removeScriptMessageHandler(forName: BridgeGate.handler)
            runtime.contentController.add(g, name: BridgeGate.handler)
            gate = g
        }
        if BootCheck.asked(), let webView { bootCheck = BootCheckView(webView: webView) }
    }

    /**
     * The page loads (the runtime's viewDidLoad) only once its gates stand: the bridge gate in place and the content rules that
     * block the runtime's file prefixes compiled and added. Either missing, nothing loads.
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
