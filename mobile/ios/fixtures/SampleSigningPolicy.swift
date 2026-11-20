import ConductorKey
import Foundation

// A sample packaging's signing policy, kept outside the packaging's place: the mobile-ios job copies it into
// ConductorNative/Sources/ConductorPackaging/Packaging/ for one build, and reads the built executable for the class under the key's
// fixed name (scripts/ios_boot.py policy). It signs nothing.
@objc(ConductorKeySigningPolicy)
public final class SampleSigningPolicy: NSObject, KeySigningPolicy {
    public required override init() {
        super.init()
    }

    public func plan(request: [String: Any], state: String, id: String?) -> Result<SigningPlan, KeyPolicyRefusal> {
        .failure(KeyPolicyRefusal(code: "sample", words: "The sample policy signs nothing."))
    }
}
