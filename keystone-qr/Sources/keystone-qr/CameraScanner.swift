import AVFoundation
import Vision

/// Captures video frames from the webcam and detects QR codes using Vision.
final class CameraScanner: NSObject, AVCaptureVideoDataOutputSampleBufferDelegate {
    private let session = AVCaptureSession()
    private let queue = DispatchQueue(label: "camera-scan")
    private var onQRCode: ((String) -> Void)?

    func start(onQRCode: @escaping (String) -> Void) throws {
        self.onQRCode = onQRCode

        guard let device = AVCaptureDevice.default(for: .video) else {
            throw ScanError.noCameraAvailable
        }

        let input = try AVCaptureDeviceInput(device: device)
        session.addInput(input)

        let output = AVCaptureVideoDataOutput()
        output.setSampleBufferDelegate(self, queue: queue)
        output.alwaysDiscardsLateVideoFrames = true
        session.addOutput(output)

        session.startRunning()
    }

    func stop() {
        session.stopRunning()
    }

    func captureOutput(
        _ output: AVCaptureOutput,
        didOutput sampleBuffer: CMSampleBuffer,
        from connection: AVCaptureConnection
    ) {
        guard let pixelBuffer = CMSampleBufferGetImageBuffer(sampleBuffer) else { return }

        let request = VNDetectBarcodesRequest()
        request.symbologies = [.qr]

        let handler = VNImageRequestHandler(cvPixelBuffer: pixelBuffer)
        try? handler.perform([request])

        guard let results = request.results else { return }
        for observation in results {
            if let payload = observation.payloadStringValue {
                onQRCode?(payload)
            }
        }
    }
}
