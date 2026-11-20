import ConductorKey

// The packaging's place for its native sources (docs/plans/phone-shell.md, "A packaging's native sources"): a packaging puts its
// Swift files, its signing policy above all, in the folder `Packaging/` beside this file, which this repository ships empty (the
// project check says so). The Swift Package Manager compiles every file there into this module, which depends on ConductorKey (a
// file there can `import ConductorKey` for `KeySigningPolicy` and `SigningPlan`); the app links the module and names it at start,
// and links with -ObjC, so a class nothing names by symbol is still in the executable. No project file is edited.

/** Named by the app at start, so that the module is linked whatever it holds. */
public enum ConductorPackaging {
    public static let place = "Sources/ConductorPackaging/Packaging"
    public static func linked() -> Bool { true }
}
