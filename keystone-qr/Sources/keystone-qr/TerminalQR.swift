import Foundation
import CoreImage

/// Renders QR codes as Unicode half-block characters for terminal display.
/// Uses CIQRCodeGenerator for QR encoding and ▀ half-blocks with ANSI colors
/// to pack two pixel rows per terminal line.
enum TerminalQR {
    static func render(_ content: String) -> String {
        let modules = generateModules(from: content)
        return modulesToTerminal(modules)
    }

    // MARK: - QR Generation

    private static func generateModules(from content: String) -> [[Bool]] {
        guard let data = content.data(using: .ascii) else { return [] }
        guard let filter = CIFilter(name: "CIQRCodeGenerator") else { return [] }
        filter.setValue(data, forKey: "inputMessage")
        // Low error correction = smaller QR = fits terminal better
        filter.setValue("L", forKey: "inputCorrectionLevel")

        guard let ciImage = filter.outputImage else { return [] }

        // CIQRCodeGenerator outputs 1 pixel per module - don't scale
        let context = CIContext()
        guard let cgImage = context.createCGImage(ciImage, from: ciImage.extent) else { return [] }

        let width = cgImage.width
        let height = cgImage.height

        // Read pixel data as grayscale
        let colorSpace = CGColorSpaceCreateDeviceGray()
        var pixels = [UInt8](repeating: 0, count: width * height)
        guard let bitmapContext = CGContext(
            data: &pixels,
            width: width,
            height: height,
            bitsPerComponent: 8,
            bytesPerRow: width,
            space: colorSpace,
            bitmapInfo: CGImageAlphaInfo.none.rawValue
        ) else { return [] }

        bitmapContext.draw(cgImage, in: CGRect(x: 0, y: 0, width: width, height: height))

        var modules: [[Bool]] = []
        for y in 0..<height {
            var row: [Bool] = []
            for x in 0..<width {
                row.append(pixels[y * width + x] < 128) // true = dark module
            }
            modules.append(row)
        }

        return modules
    }

    // MARK: - Terminal Rendering

    private static func modulesToTerminal(_ modules: [[Bool]]) -> String {
        guard !modules.isEmpty else { return "" }

        // Quiet zone: 4 modules on each side (QR spec minimum)
        let quiet = 4
        let width = modules[0].count + quiet * 2
        var height = modules.count + quiet * 2

        // Build padded matrix with quiet zone
        var padded: [[Bool]] = Array(repeating: Array(repeating: false, count: width), count: height)
        for y in 0..<modules.count {
            for x in 0..<modules[y].count {
                padded[y + quiet][x + quiet] = modules[y][x]
            }
        }

        // Ensure even height for half-block pairing
        if height % 2 != 0 {
            padded.append(Array(repeating: false, count: width))
            height += 1
        }

        var output = ""

        // Process two rows at a time using ▀ (upper half block)
        // Foreground color = top pixel, background color = bottom pixel
        for row in stride(from: 0, to: height, by: 2) {
            for col in 0..<width {
                let top = padded[row][col]      // true = dark
                let bottom = padded[row + 1][col]

                switch (top, bottom) {
                case (false, false): // both white
                    output += "\u{001B}[97;107m▀"
                case (true, true):   // both dark
                    output += "\u{001B}[30;40m▀"
                case (true, false):  // top dark, bottom white
                    output += "\u{001B}[30;107m▀"
                case (false, true):  // top white, bottom dark
                    output += "\u{001B}[97;40m▀"
                }
            }
            output += "\u{001B}[0m\n"
        }

        return output
    }
}
