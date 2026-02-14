import ArgumentParser
import Foundation
import URKit

enum ScanError: Error, CustomStringConvertible {
    case noCameraAvailable
    case noInput
    case decodingFailed(String)

    var description: String {
        switch self {
        case .noCameraAvailable: return "No camera found"
        case .noInput: return "Specify --camera or --screen"
        case .decodingFailed(let msg): return "Decoding failed: \(msg)"
        }
    }
}

struct Scan: ParsableCommand {
    static let configuration = CommandConfiguration(
        abstract: "Scan QR code(s) from camera or screen"
    )

    @Flag(name: .long, help: "Capture from webcam")
    var camera = false

    @Flag(name: .long, help: "Read QR codes from screen (useful for cross-device testing)")
    var screen = false

    @Flag(name: .long, help: "Expect a single QR code (not animated UR)")
    var single = false

    func run() throws {
        guard camera || screen else {
            throw ScanError.noInput
        }

        // Hide cursor
        print("\u{001B}[?25l", terminator: "")
        fflush(stdout)

        signal(SIGINT) { _ in
            print("\u{001B}[?25h\u{001B}[0m") // restore cursor + reset colors
            Darwin.exit(0)
        }

        if camera {
            try runCamera()
        } else {
            runScreenLoop()
        }
    }

    // MARK: - Camera

    private func runCamera() throws {
        let decoder = URDecoder()
        let scanner = CameraScanner()
        var lastPayload = ""
        var partsReceived = 0

        try scanner.start { payload in
            guard payload != lastPayload else { return }
            lastPayload = payload

            if single {
                scanner.stop()
                DispatchQueue.main.async {
                    printResult(payload)
                    cleanup()
                }
                return
            }

            decoder.receivePart(payload)
            partsReceived += 1

            if let result = decoder.result {
                scanner.stop()
                DispatchQueue.main.async {
                    handleURResult(result, decoder: decoder, partsReceived: partsReceived)
                    cleanup()
                }
            } else {
                DispatchQueue.main.async {
                    printProgress(decoder: decoder, partsReceived: partsReceived)
                }
            }
        }

        // Run the event loop for AVFoundation callbacks
        printStatus("Camera active — point at QR code...")
        RunLoop.main.run()
    }

    // MARK: - Screen

    private func runScreenLoop() {
        let state = ScanState()

        printStatus("Screen capture active — show QR code on screen...")

        // Poll screen at ~10fps
        let single = self.single
        let timer = Timer.scheduledTimer(withTimeInterval: 0.1, repeats: true) { timer in
            Task {
                guard let codes = try? await ScreenScanner.detectQRCodes(), !codes.isEmpty else { return }

                for payload in codes {
                    guard payload != state.lastPayload else { continue }
                    state.lastPayload = payload

                    if single {
                        timer.invalidate()
                        printResult(payload)
                        cleanup()
                        return
                    }

                    state.decoder.receivePart(payload)
                    state.partsReceived += 1

                    if let result = state.decoder.result {
                        timer.invalidate()
                        handleURResult(result, decoder: state.decoder, partsReceived: state.partsReceived)
                        cleanup()
                        return
                    } else {
                        printProgress(decoder: state.decoder, partsReceived: state.partsReceived)
                    }
                }
            }
        }

        RunLoop.main.add(timer, forMode: .common)
        RunLoop.main.run()
    }
}

/// Mutable state container to avoid captured-var concurrency warnings in the Timer+Task closure.
private final class ScanState: @unchecked Sendable {
    let decoder = URDecoder()
    var lastPayload = ""
    var partsReceived = 0
}

// MARK: - Output

private func handleURResult(_ result: Result<UR, Error>, decoder: URDecoder, partsReceived: Int) {
    // Clear progress line
    print("\u{001B}[2K\r", terminator: "")

    switch result {
    case .success(let ur):
        if let data = try? Data(cbor: ur.cbor) {
            let hex = data.map { String(format: "%02x", $0) }.joined()
            print("Type:     ur:\(ur.type)")
            print("Size:     \(data.count) bytes")
            print("Parts:    \(partsReceived) processed")
            print("Data:     \(hex)")
        } else {
            // CBOR isn't raw bytes — dump the CBOR diagnostic
            print("Type:     ur:\(ur.type)")
            print("CBOR:     \(ur.cbor)")
        }
    case .failure(let error):
        fputs("Error: \(error.localizedDescription)\n", stderr)
    }
}

private func printResult(_ payload: String) {
    print("\u{001B}[2K\r", terminator: "")
    print(payload)
}

private func printProgress(decoder: URDecoder, partsReceived: Int) {
    let pct = Int(decoder.estimatedPercentComplete * 100)
    let received = decoder.receivedFragmentIndexes.count
    let expected = decoder.expectedFragmentCount ?? 0
    let type = decoder.expectedType ?? "?"
    let bar = progressBar(pct)

    print("\u{001B}[2K\r  \(bar) \(pct)%  [\(received)/\(expected) fragments]  ur:\(type)  (\(partsReceived) frames)", terminator: "")
    fflush(stdout)
}

private func printStatus(_ msg: String) {
    print("  \(msg)")
}

private func progressBar(_ percent: Int) -> String {
    let filled = percent / 5  // 20 chars wide
    let empty = 20 - filled
    return "[" + String(repeating: "=", count: filled) + String(repeating: " ", count: empty) + "]"
}

private func cleanup() {
    print("\u{001B}[?25h", terminator: "") // restore cursor
    fflush(stdout)
    Darwin.exit(0)
}
