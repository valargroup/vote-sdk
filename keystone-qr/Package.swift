// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "keystone-qr",
    platforms: [.macOS(.v14)],
    dependencies: [
        .package(url: "https://github.com/apple/swift-argument-parser", from: "1.3.0"),
        .package(url: "https://github.com/BlockchainCommons/URKit", from: "15.0.0"),
    ],
    targets: [
        .executableTarget(
            name: "keystone-qr",
            dependencies: [
                .product(name: "ArgumentParser", package: "swift-argument-parser"),
                .product(name: "URKit", package: "URKit"),
            ]
        ),
    ]
)
