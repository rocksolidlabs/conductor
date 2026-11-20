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
    private var loads: NavigationGate?
    /** The boot check's report, only in a launch the boot check asked for. */
    private var bootCheck: BootCheckView?
    /** The gate at the runtime's script message handler: none until it is in place, and then the page may load. */
    private var gate: BridgeGateHandler?
    private var earlier: EarlierComparison?

    /** The bundle the app ships, always: the runtime's default reads a base path a page asked to keep; the shell's never does. */
    override func instanceDescriptor() -> InstanceDescriptor {
        InstanceDescriptor()
    }

    /** The shell's router: a file only inside the bundle's folder. */
    override func router() -> Router {
        ShellRouter()
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
        let runtimeNav = webView.navigationDelegate as? (NSObject & WKNavigationDelegate)
        let n = NavigationGate(wrapping: runtimeNav, app: app)
        n.foreground = { UIApplication.shared.applicationState == .active }
        n.handOff = { UIApplication.shared.open($0, options: [:], completionHandler: nil) }
        loads = n
        webView.navigationDelegate = n
        // A deliberate break, reverted next: the load gate's earlier comparison in front of it.
        let o = EarlierComparison(gate: n, runtime: runtimeNav, app: app)
        earlier = o
        webView.navigationDelegate = o
        // A script opens no window on its own (WebKit's own refusal, without a person's tap); a window a tap opens is the load gate's.
        webView.configuration.preferences.javaScriptCanOpenWindowsAutomatically = false
        let d = MediaCaptureRefused(wrapping: webView.uiDelegate as? (NSObject & WKUIDelegate))
        d.newWindow = { [weak n] action in n?.newWindow(action) }
        refused = d
        webView.uiDelegate = d
        // The runtime's handler, `bridge`, taken over by the gate, which forwards to it only what it admits.
        if let runtime = (bridge as? CapacitorBridge)?.webViewDelegationHandler {
            // A deliberate break, reverted next: the listener calls' form not held.
            let g = BridgeGateHandler(forwardingTo: runtime, view: webView, app: app) { body, sender, app in
                guard BridgeGate.fromShell(sender, app: app), let d = body as? [String: Any], Set(d.keys).isSubset(of: BridgeGate.keys),
                      d["type"] as? String == "message", let p = d["pluginId"] as? String, let m = d["methodName"] as? String,
                      BridgeGate.calls[p]?.contains(m) == true, let c = d["callbackId"] as? String, BridgeGate.callbackForm(c) else { return false }
                if let o = d["options"], !(o is [String: Any]) { return false }
                return true
            }
            runtime.contentController.removeScriptMessageHandler(forName: BridgeGate.handler)
            runtime.contentController.add(g, name: BridgeGate.handler)
            gate = g
        }
        if BootCheck.asked(), let gate { bootCheck = BootCheckView(webView: webView, gate: gate, loads: n) }
    }

    /**
     * The page loads (the runtime's viewDidLoad) only once its gates stand: the bridge gate and the load gate in place and the content
     * rules compiled and added. Any missing, nothing loads.
     */
    override func viewDidLoad() {
        guard gate != nil, loads != nil, let app = bridge?.config.localURL,
              let controller = (bridge as? CapacitorBridge)?.webViewDelegationHandler.contentController
        else { return }
        WKContentRuleListStore.default().compileContentRuleList(forIdentifier: FilePrefix.identifier, encodedContentRuleList: FilePrefix.rules(app: app)) { list, _ in
            DispatchQueue.main.async {
                guard let list else { return }
                controller.add(list)
                super.viewDidLoad()
            }
        }
    }
}

/** The runtime's scheme handler's router, ConductorShell's BundleRoute: a file only inside the bundle's folder. */
struct ShellRouter: Router {
    var basePath: String = ""
    func route(for path: String) -> String {
        BundleRoute.route(base: basePath, path: path)
    }
}

/** A deliberate break, reverted next: the load gate's earlier comparison (scheme, host and port in any case) forwarding to the runtime. */
final class EarlierComparison: NSObject, WKNavigationDelegate {
    let gate: NavigationGate
    weak var runtime: (NSObject & WKNavigationDelegate)?
    let app: URL
    init(gate: NavigationGate, runtime: (NSObject & WKNavigationDelegate)?, app: URL) {
        self.gate = gate
        self.runtime = runtime
        self.app = app
    }

    func webView(_ webView: WKWebView, decidePolicyFor a: WKNavigationAction, decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        if a.targetFrame?.isMainFrame == true, let url = a.request.url, url.scheme?.lowercased() == app.scheme?.lowercased(),
           url.host?.lowercased() == app.host?.lowercased(), url.port == app.port, url.user == nil, let runtime {
            runtime.webView?(webView, decidePolicyFor: a, decisionHandler: decisionHandler)
            return
        }
        gate.webView(webView, decidePolicyFor: a, decisionHandler: decisionHandler)
    }

    override func responds(to selector: Selector!) -> Bool { super.responds(to: selector) || gate.responds(to: selector) }
    override func forwardingTarget(for selector: Selector!) -> Any? { super.responds(to: selector) ? nil : gate }
}
