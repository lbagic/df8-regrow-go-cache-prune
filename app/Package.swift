// swift-tools-version: 6.0
import PackageDescription

// Builds with the Command Line Tools alone: no SwiftPM resources, no
// asset catalog, tests on swift-testing.
let package = Package(
    name: "Regrow",
    platforms: [.macOS(.v14)],
    targets: [
        .target(name: "RegrowKit"),
        .executableTarget(name: "Regrow", dependencies: ["RegrowKit"]),
        .testTarget(name: "RegrowKitTests", dependencies: ["RegrowKit"]),
    ],
    swiftLanguageModes: [.v5]
)
