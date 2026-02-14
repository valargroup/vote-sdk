import ArgumentParser
import Foundation
import URKit

struct Display: ParsableCommand {
    static let configuration = CommandConfiguration(
        abstract: "Display QR code(s) in the terminal"
    )

    @Option(name: .long, help: "UR type for animated fountain-coded display (e.g. 'bytes', 'zcash-pczt')")
    var urType: String?

    @Option(name: .long, help: "Max fragment length in bytes for fountain coding (default: 200)")
    var fragmentLen: Int = 200

    @Option(name: .long, help: "Frame interval in milliseconds (default: 200)")
    var interval: Int = 200

    @Argument(help: "Data to encode (reads from stdin if omitted)")
    var data: String?

    func run() throws {
        let input: String
        if let data = data {
            input = data
        } else if !isatty(STDIN_FILENO).boolValue {
            // Read all of stdin
            input = readAllStdin()
        } else {
            throw ValidationError("No input data. Pass as argument or pipe to stdin.")
        }

        guard !input.isEmpty else {
            throw ValidationError("Empty input")
        }

        if let urType = urType {
            try animatedDisplay(input: input, urType: urType)
        } else {
            // Static single QR
            let rendered = TerminalQR.render(input)
            print(rendered)
        }
    }

    // MARK: - Animated UR Display

    private func animatedDisplay(input: String, urType: String) throws {
        let bytes: [UInt8]
        if let hexBytes = hexToBytes(input.trimmingCharacters(in: .whitespacesAndNewlines)) {
            bytes = hexBytes
        } else {
            bytes = Array(input.utf8)
        }

        let cbor = CBOR(bytes)
        let ur = try UR(type: urType, cbor: cbor)
        let encoder = UREncoder(ur, maxFragmentLen: fragmentLen)

        if encoder.isSinglePart {
            let part = encoder.nextPart()
            print(TerminalQR.render(part))
            return
        }

        // Hide cursor, set up cleanup on Ctrl-C
        print("\u{001B}[?25l", terminator: "")
        fflush(stdout)

        signal(SIGINT) { _ in
            print("\u{001B}[?25h") // restore cursor
            Darwin.exit(0)
        }

        let intervalSec = Double(interval) / 1000.0
        var seqNum = 0

        while true {
            let part = encoder.nextPart()
            seqNum += 1

            // Clear screen and move to top-left
            print("\u{001B}[2J\u{001B}[H", terminator: "")
            print(TerminalQR.render(part))
            print("  Part \(seqNum) | \(bytes.count) bytes | fragment \(fragmentLen)B | \(interval)ms")

            fflush(stdout)
            Thread.sleep(forTimeInterval: intervalSec)
        }
    }
}

// MARK: - Helpers

private func readAllStdin() -> String {
    var result = ""
    while let line = readLine(strippingNewline: false) {
        result += line
    }
    return result.trimmingCharacters(in: .whitespacesAndNewlines)
}

private func hexToBytes(_ hex: String) -> [UInt8]? {
    let clean = hex.replacingOccurrences(of: " ", with: "")
        .replacingOccurrences(of: "\n", with: "")

    guard clean.count % 2 == 0 else { return nil }
    guard clean.allSatisfy({ $0.isHexDigit }) else { return nil }

    var bytes: [UInt8] = []
    var index = clean.startIndex
    while index < clean.endIndex {
        let nextIndex = clean.index(index, offsetBy: 2)
        guard let byte = UInt8(clean[index..<nextIndex], radix: 16) else { return nil }
        bytes.append(byte)
        index = nextIndex
    }
    return bytes
}

private extension Int32 {
    var boolValue: Bool { self != 0 }
}
