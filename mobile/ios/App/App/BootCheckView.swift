import ConductorShell
import UIKit
import WebKit

// The boot check's side in the app (ConductorShell's BootCheck): added only when the app is launched with the boot check's argument.
// It adds the check's script before the page's own, hands the page the path of one of the bundle's images and a climb out of the
// bundle to a copy of it (for the file probes), counts the probe messages the bridge gate drops and the probe loads the load gate
// refuses (telling the page of each refusal, so that it waits for one before its next), and shows all of it in one label the UI test
// reads.
final class BootCheckView: NSObject, WKScriptMessageHandler {
    private var report = BootReport()
    private let label = UILabel()

    init(webView: WKWebView, gate: BridgeGateHandler, loads: NavigationGate) {
        super.init()
        let controller = webView.configuration.userContentController
        if let image = BootCheckView.bundledImage() {
            // A copy of it outside the bundle (the app's temporary folder), which a climb from the bundle's folder would reach.
            let outside = URL(fileURLWithPath: NSTemporaryDirectory()).appendingPathComponent("conductor-boot-climb.png")
            try? FileManager.default.removeItem(at: outside)
            try? FileManager.default.copyItem(at: URL(fileURLWithPath: image), to: outside)
            let base = Bundle.main.bundleURL.appendingPathComponent("public", isDirectory: true).path
            let climb = BootCheck.climb(from: base, to: outside.resolvingSymlinksInPath().path)
            controller.addUserScript(WKUserScript(source: BootCheck.fileScript(image, climb: climb), injectionTime: .atDocumentStart, forMainFrameOnly: true))
        }
        controller.addUserScript(WKUserScript(source: BootCheck.script, injectionTime: .atDocumentStart, forMainFrameOnly: true))
        gate.dropped = { [weak self] body in
            guard let self, let message = BootCheck.drop(body) else { return }
            self.report.take(message)
            self.show()
        }
        loads.refused = { [weak self, weak webView] url in
            guard let self, let message = BootCheck.refusal(url) else { return }
            self.report.take(message)
            self.show()
            webView?.evaluateJavaScript(BootCheck.refusedScript, completionHandler: nil)
        }
        controller.add(self, name: BootCheck.handler)
        label.accessibilityIdentifier = BootCheck.identifier
        label.numberOfLines = 0
        label.font = .systemFont(ofSize: 11)
        label.backgroundColor = .white
        label.textColor = .black
        show()
        label.translatesAutoresizingMaskIntoConstraints = false
        webView.addSubview(label)
        NSLayoutConstraint.activate([
            label.leadingAnchor.constraint(equalTo: webView.safeAreaLayoutGuide.leadingAnchor, constant: 8),
            label.trailingAnchor.constraint(equalTo: webView.safeAreaLayoutGuide.trailingAnchor, constant: -8),
            label.bottomAnchor.constraint(equalTo: webView.safeAreaLayoutGuide.bottomAnchor, constant: -8),
        ])
    }

    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        guard let text = message.body as? String else { return }
        report.take(text)
        show()
    }

    /** The absolute path of an image the app's bundle holds (under public/), which the file-prefix probe asks for. */
    static func bundledImage() -> String? {
        let base = Bundle.main.bundleURL.appendingPathComponent("public", isDirectory: true)
        let walk = FileManager.default.enumerator(at: base, includingPropertiesForKeys: nil)
        while let url = walk?.nextObject() as? URL {
            if url.pathExtension == "png" { return url.path }
        }
        return nil
    }

    private func show() {
        label.text = report.label
        label.accessibilityLabel = report.label
        label.accessibilityValue = (report.dropped.sorted() + report.refused.sorted()).joined(separator: " ")
    }
}
