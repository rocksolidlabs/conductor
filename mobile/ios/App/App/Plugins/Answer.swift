import Capacitor
import ConductorShell

extension CAPPluginCall {
    /** A call's answer handed back: resolved with its value, or rejected with its words, its code and its words as a field. */
    func answer(_ a: CallAnswer) {
        if let r = a.rejection {
            reject(r.message, r.code, nil, r.data)
        } else {
            resolve(a.resolved ?? [:])
        }
    }
}
