import ConductorShell
import UIKit
import WebKit

// The boot check's side in the app (ConductorShell's BootCheck): added only when the app is launched with the boot check's argument.
// It adds the check's script before the page's own and shows what the script reports in one label the UI test reads.
final class BootCheckView: NSObject, WKScriptMessageHandler {
    private var report = BootReport()
    private let label = UILabel()

    init(webView: WKWebView) {
        super.init()
        let controller = webView.configuration.userContentController
        controller.addUserScript(WKUserScript(source: BootCheck.script, injectionTime: .atDocumentStart, forMainFrameOnly: true))
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

    private func show() {
        label.text = report.label
        label.accessibilityLabel = report.label
    }
}
