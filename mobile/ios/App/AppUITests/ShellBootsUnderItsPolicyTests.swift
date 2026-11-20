import XCTest

// The plan's boot check (docs/plans/phone-shell.md, ShellBootsUnderItsPolicyTests): the built app started on a simulator with no
// provider draws Open a server, under the bundle's own policy with no violation reported, with the native layer's plugins in the
// page (the bridge's platform `ios`, which it reports only when every plugin it needs is there). The app shows what its boot check
// heard in one label (ConductorShell's BootCheck), added only for this launch's argument.
final class ShellBootsUnderItsPolicyTests: XCTestCase {
    func testTheShellStartsOnOpenAServerUnderItsPolicyWithItsNativeLayer() {
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["--conductor-boot-check"]
        app.launch()
        let report = app.staticTexts["conductor-boot-check"]
        XCTAssertTrue(report.waitForExistence(timeout: 120), "the boot check's report was not shown:\n\(app.debugDescription)")
        // The page reports ready once it has drawn Open a server: the first start of a fresh simulator can take minutes.
        let ready = expectation(for: NSPredicate(format: "label BEGINSWITH 'ready'"), evaluatedWith: report)
        wait(for: [ready], timeout: 180)
        XCTAssertEqual(report.label, "ready platform=ios plugins=ok violations=0")
        // Open a server's first control, as a person meets it.
        let address = app.descendants(matching: .textField)["Server address"]
        XCTAssertTrue(address.waitForExistence(timeout: 30), "Open a server's address field was not drawn:\n\(app.debugDescription)")
    }
}
