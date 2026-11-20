import XCTest

// The plan's boot check (docs/plans/phone-shell.md, ShellBootsUnderItsPolicyTests): the built app started on a simulator with no
// provider draws Open a server, under the bundle's own policy with no violation reported, with the native layer's plugins in the
// page (the bridge's platform `ios`, which it reports only when every plugin it needs is there), and every known way around the
// shell's gates refused (a message from another frame or of another form, a load the page may not make, a spelling or a path the
// scheme handler may not serve, a prompt of the runtime's cookies kind) while the calls the shell means still answer, and a decision's
// address as the screens build it let through. The app shows what its boot check heard in one label (ConductorShell's BootCheck),
// added only for this launch's argument.
final class ShellBootsUnderItsPolicyTests: XCTestCase {
    func testTheShellStartsOnOpenAServerUnderItsPolicyWithItsNativeLayer() {
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["--conductor-boot-check"]
        app.launch()
        let report = app.staticTexts["conductor-boot-check"]
        XCTAssertTrue(report.waitForExistence(timeout: 120), "the boot check's report was not shown:\n\(app.debugDescription)")
        // The page reports ready once it has drawn Open a server (the first start of a fresh simulator can take minutes), and the
        // gates' counts come from native code as each probe reaches it, some after the page's own report. The report is polled until
        // it is the whole one (each count only grows), the app has ended or the bound has passed; the last one read is kept, so that a
        // failure says how far the check got and what the gates refused, even when a probe ended the app.
        let expected = "ready platform=ios plugins=ok probes=ok dropped=8 refused=5 violations=0"
        var last = "", refused = ""
        let deadline = Date().addingTimeInterval(240)
        while Date() < deadline, app.state == .runningForeground {
            last = report.label
            refused = String(describing: report.value)
            if last == expected { break }
            RunLoop.current.run(until: Date().addingTimeInterval(1))
        }
        XCTAssertEqual(last, expected, "the app \(app.state == .runningForeground ? "is running" : "has ended"); what the gates refused: \(refused)")
        // Open a server's first control, as a person meets it.
        let address = app.descendants(matching: .textField)["Server address"]
        XCTAssertTrue(address.waitForExistence(timeout: 30), "Open a server's address field was not drawn:\n\(app.debugDescription)")
    }
}
