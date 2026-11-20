import Testing

@testable import ConductorShell

// The boot check's report, as the UI test reads it: ready once, the probes' drops counted (only theirs), every violation counted and
// the first shown, and asked only by its launch argument.

@Suite struct BootCheckTests {
    @Test func theReportSaysReadyOnceAndCountsTheProbesDropsAndEveryViolation() {
        var r = BootReport()
        #expect(r.label == "waiting unstarted dropped=0 violations=0")
        r.take("started")
        #expect(r.label == "waiting started dropped=0 violations=0")
        for id in ["webview", "http", "cookies", "cordova", "http"] {
            r.take(BootCheck.drop(["type": "message", "callbackId": BootCheck.probePrefix + id])!)
        }
        #expect(BootCheck.drop(["type": "message", "pluginId": "Console", "callbackId": "12345"]) == nil)
        #expect(BootCheck.drop("not an object") == nil)
        r.take("ready platform=ios plugins=ok probes=ok")
        r.take("ready platform=web plugins=missing probes=ok")
        #expect(r.label == "ready platform=ios plugins=ok probes=ok dropped=4 violations=0")
        #expect(BootCheck.probeMessages == 4)
        r.take("violation script-src https://elsewhere.example/x.js")
        r.take("violation connect-src https://elsewhere.example/")
        r.take("something else")
        #expect(r.label == "ready platform=ios plugins=ok probes=ok dropped=4 violations=2 first: script-src https://elsewhere.example/x.js")
        for _ in 0..<40 { r.take("violation img-src data") }
        #expect(r.violations.count == 20)
    }

    @Test func onlyTheBootChecksLaunchAsksForIt() {
        #expect(!BootCheck.asked(["/App"]))
        #expect(BootCheck.asked(["/App", "--conductor-boot-check"]))
        #expect(BootCheck.script.contains("securitypolicyviolation") && BootCheck.script.contains("[data-shell-open-server]"))
        #expect(BootCheck.script.contains("window.webkit.messageHandlers.conductorBootCheck"))
        #expect(BootCheck.fileScript("/a/b \"c\".png") == "window.__conductorBootFile = [\"\\/a\\/b \\\"c\\\".png\"][0];")
    }

    @Test func theProbesTryEveryWayAroundAGate() {
        for way in ["pluginId: 'CAPWebViewPlugin'", "pluginId: 'CAPHttpPlugin'", "pluginId: 'CAPCookiesPlugin'", "type: 'cordova'",
                    "type: 'CapacitorCookies.set'", "'/_capacitor_file_'"] {
            #expect(BootCheck.script.contains(way), "\(way)")
        }
    }
}
