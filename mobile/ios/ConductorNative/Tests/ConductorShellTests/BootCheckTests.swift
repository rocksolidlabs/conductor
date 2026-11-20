import Testing

@testable import ConductorShell

// The boot check's report, as the UI test reads it: ready once, every violation counted and the first shown, and asked only by its
// launch argument.

@Suite struct BootCheckTests {
    @Test func theReportSaysReadyOnceAndCountsEveryViolation() {
        var r = BootReport()
        #expect(r.label == "waiting violations=0")
        r.take("ready platform=ios plugins=ok")
        r.take("ready platform=web plugins=missing")
        #expect(r.label == "ready platform=ios plugins=ok violations=0")
        r.take("violation script-src https://elsewhere.example/x.js")
        r.take("violation connect-src https://elsewhere.example/")
        r.take("something else")
        #expect(r.label == "ready platform=ios plugins=ok violations=2 first: script-src https://elsewhere.example/x.js")
        for _ in 0..<40 { r.take("violation img-src data") }
        #expect(r.violations.count == 20)
    }

    @Test func onlyTheBootChecksLaunchAsksForIt() {
        #expect(!BootCheck.asked(["/App"]))
        #expect(BootCheck.asked(["/App", "--conductor-boot-check"]))
        #expect(BootCheck.script.contains("securitypolicyviolation") && BootCheck.script.contains("[data-shell-open-server]"))
        #expect(BootCheck.script.contains("window.webkit.messageHandlers.conductorBootCheck"))
    }
}
