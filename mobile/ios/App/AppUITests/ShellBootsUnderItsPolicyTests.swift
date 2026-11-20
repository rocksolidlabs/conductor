import XCTest

// The plan's boot check (docs/plans/phone-shell.md, ShellBootsUnderItsPolicyTests): the built app started on a simulator with no
// provider draws Open a server, under the bundle's own policy with no violation reported, with the native layer's plugins in the
// page (the bridge's platform `ios`, which it reports only when every plugin it needs is there), and every known way around the
// shell's gates refused (a plugin named by its native class, a Cordova message, a prompt of the runtime's cookies kind, a file under
// the runtime's file prefix) while the calls the shell means still answer. The app shows what its boot check heard in one label
// (ConductorShell's BootCheck), added only for this launch's argument.
final class ShellBootsUnderItsPolicyTests: XCTestCase {
    func testTheShellStartsOnOpenAServerUnderItsPolicyWithItsNativeLayer() {
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["--conductor-boot-check"]
        app.launch()
        let report = app.staticTexts["conductor-boot-check"]
        XCTAssertTrue(report.waitForExistence(timeout: 120), "the boot check's report was not shown:\n\(app.debugDescription)")
        // The page reports ready once it has drawn Open a server: the first start of a fresh simulator can take minutes.
        let ready = XCTNSPredicateExpectation(predicate: NSPredicate(format: "label BEGINSWITH 'ready'"), object: report)
        let waited = XCTWaiter().wait(for: [ready], timeout: 180)
        XCTAssertEqual(waited, .completed, "the boot check never became ready; its report: \(report.label)")
        XCTAssertEqual(report.label, "ready platform=ios plugins=ok probes=ok dropped=7 refused=4 violations=0")
        // Open a server's first control, as a person meets it.
        let address = app.descendants(matching: .textField)["Server address"]
        XCTAssertTrue(address.waitForExistence(timeout: 30), "Open a server's address field was not drawn:\n\(app.debugDescription)")
    }
}
