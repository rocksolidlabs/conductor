// swift-tools-version:5.9
// The phone shell's native layer (docs/plans/phone-shell.md, OSS-1a): the key, the store and the plugins' calls over them, as plain
// Swift with no package of anyone else's, built and tested on its own (`swift test`, the tests in Swift Testing) and used by the iOS app's plugins.
import PackageDescription

let package = Package(
    name: "ConductorNative",
    platforms: [.iOS(.v16), .macOS(.v14)],
    products: [
        .library(name: "ConductorKey", targets: ["ConductorKey"]),
        .library(name: "ConductorStore", targets: ["ConductorStore"]),
        .library(name: "ConductorShell", targets: ["ConductorShell"]),
        .library(name: "ConductorPackaging", targets: ["ConductorPackaging"]),
    ],
    targets: [
        .target(name: "ConductorKey"),
        .testTarget(name: "ConductorKeyTests", dependencies: ["ConductorKey"]),
        .target(name: "ConductorStore"),
        .testTarget(name: "ConductorStoreTests", dependencies: ["ConductorStore"]),
        .target(name: "ConductorShell", dependencies: ["ConductorKey", "ConductorStore"]),
        .testTarget(name: "ConductorShellTests", dependencies: ["ConductorShell"]),
        // A packaging's native sources: what it puts in Sources/ConductorPackaging/Packaging/ (empty here) is compiled into the app.
        .target(name: "ConductorPackaging", dependencies: ["ConductorKey"]),
    ]
)
