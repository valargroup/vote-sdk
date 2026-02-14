import ScreenCaptureKit
import Vision

/// Captures the screen using ScreenCaptureKit and detects QR codes via Vision.
enum ScreenScanner {
    static func detectQRCodes() async throws -> [String] {
        let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: true)
        guard let display = content.displays.first else { return [] }

        let filter = SCContentFilter(display: display, excludingWindows: [])
        let config = SCStreamConfiguration()
        config.width = display.width * 2
        config.height = display.height * 2

        let image = try await SCScreenshotManager.captureImage(
            contentFilter: filter,
            configuration: config
        )

        let request = VNDetectBarcodesRequest()
        request.symbologies = [.qr]

        let handler = VNImageRequestHandler(cgImage: image)
        try handler.perform([request])

        guard let results = request.results else { return [] }
        return results.compactMap { $0.payloadStringValue }
    }
}
