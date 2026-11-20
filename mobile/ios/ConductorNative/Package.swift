// swift-tools-version:5.9
// The phone shell's native layer (docs/plans/phone-shell.md, OSS-1a): the key and the store, as plain Swift with no package of
// anyone else's, built and tested on its own (`swift test`, the tests in Swift Testing) and used by the iOS app's plugins.
import PackageDescription

let package = Package(
    name: "ConductorNative",
    platforms: [.iOS(.v16), .macOS(.v14)],
    products: [
        .library(name: "ConductorKey", targets: ["ConductorKey"]),
    ],
    targets: [
        .target(name: "ConductorKey"),
        .testTarget(name: "ConductorKeyTests", dependencies: ["ConductorKey"]),
    ]
)
