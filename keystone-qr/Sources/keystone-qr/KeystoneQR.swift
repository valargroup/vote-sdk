import ArgumentParser

@main
struct KeystoneQR: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "keystone-qr",
        abstract: "Keystone dynamic QR codes in your terminal",
        subcommands: [Display.self, Scan.self],
        defaultSubcommand: Display.self
    )
}
