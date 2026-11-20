import Foundation
import Testing

@testable import ConductorShell

// The boot check's report, as the UI test reads it: ready once, the probes' drops and refused loads counted (only theirs), every
// violation counted and the first shown, and asked only by its launch argument.

@Suite struct BootCheckTests {
    @Test func theReportSaysReadyOnceAndCountsTheProbesDropsRefusalsAndEveryViolation() {
        var r = BootReport()
        #expect(r.label == "waiting unstarted dropped=0 refused=0 violations=0")
        r.take("started")
        #expect(r.label == "waiting started dropped=0 refused=0 violations=0")
        for n in ["01", "02", "03", "04", "05", "06'});x=1;({a:'", "07", "02"] {
            r.take(BootCheck.drop(["type": "message", "callbackId": BootCheck.probePrefix + n])!)
        }
        #expect(BootCheck.drop(["type": "message", "pluginId": "Console", "callbackId": "12345"]) == nil)
        #expect(BootCheck.drop(["type": "message", "callbackId": "-1"]) == nil)
        #expect(BootCheck.drop("not an object") == nil)
        for u in ["https://example.com/?boot-probe-frame", "capacitor://localhost/shell?boot-probe-frame", "conductor-probe://boot-probe-location",
                  "conductor-probe://boot-probe-location"] {
            r.take(BootCheck.refusal(URL(string: u)!)!)
        }
        #expect(BootCheck.refusal(URL(string: "https://example.com/")!) == nil)
        r.take("ready platform=ios plugins=ok probes=ok")
        r.take("ready platform=web plugins=missing probes=ok")
        #expect(r.label == "ready platform=ios plugins=ok probes=ok dropped=7 refused=3 violations=0")
        #expect(BootCheck.probeMessages == 7 && BootCheck.probeLoads == 3)
        r.take("violation script-src https://elsewhere.example/x.js")
        r.take("violation connect-src https://elsewhere.example/")
        r.take("something else")
        #expect(r.label == "ready platform=ios plugins=ok probes=ok dropped=7 refused=3 violations=2 first: script-src https://elsewhere.example/x.js")
        for _ in 0..<40 { r.take("violation img-src data") }
        #expect(r.violations.count == 20)
    }

    @Test func onlyTheBootChecksLaunchAsksForIt() {
        #expect(!BootCheck.asked(["/App"]))
        #expect(BootCheck.asked(["/App", "--conductor-boot-check"]))
        #expect(BootCheck.script.contains("securitypolicyviolation") && BootCheck.script.contains("[data-shell-open-server]"))
        #expect(BootCheck.script.contains("window).webkit.messageHandlers.bridge") && BootCheck.script.contains("messageHandlers.conductorBootCheck"))
        #expect(BootCheck.fileScript("/a/b \"c\".png", climb: "/..%2Fx") == "window.__conductorBootFile = [\"\\/a\\/b \\\"c\\\".png\"][0]; window.__conductorBootClimb = [\"\\/..%2Fx\"][0];")
        #expect(BootCheck.climb(from: "/var/app/App.app/public", to: "/private/tmp/x.png") == "/..%2F..%2F..%2F..%2Fprivate%2Ftmp%2Fx.png")
    }

    @Test func theProbesTryEveryWayAroundAGate() {
        for way in ["pluginId: 'CAPWebViewPlugin'", "pluginId: 'CAPHttpPlugin'", "pluginId: 'CAPCookiesPlugin'", "type: 'cordova'",
                    "methodName: 'getDeliveredNotifications'", "window.__conductorBootInjected = 1", "frame.contentWindow", "frame.srcdoc",
                    "https://example.com/?boot-probe-frame", "/shell?boot-probe-frame", "location.href = 'conductor-probe://", "window.open('conductor-probe://",
                    "type: 'CapacitorCookies.set'", "'/_capacitor_file_'", "'/%5Fcapacitor_file_'", "__conductorBootClimb"] {
            #expect(BootCheck.script.contains(way), "\(way)")
        }
    }
}
