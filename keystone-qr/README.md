# keystone-qr

CLI tool for displaying and scanning Keystone-style animated QR codes in the terminal.

Uses [URKit](https://github.com/BlockchainCommons/URKit) fountain codes to split large payloads across multiple QR frames that cycle in the terminal — the same protocol Keystone hardware wallets use.

## Quick Start

```
cd keystone-qr
swift run keystone-qr display --ur-type bytes \
  "48656c6c6f20576f726c642048656c6c6f20576f726c642048656c6c6f20576f726c642048656c6c6f20576f726c642048656c6c6f20576f726c642048656c6c6f20576f726c642048656c6c6f20576f726c642048656c6c6f20576f726c642048656c6c6f20576f726c6421"
```

Encodes hex as a UR and cycles animated fountain-coded QR frames in your terminal. Ctrl-C to stop.

## Build

```
swift build
```

## Usage

### Static QR

```
swift run keystone-qr display "any string here"
echo "piped input" | swift run keystone-qr display
```

### Animated (fountain-coded)

For payloads that need multi-frame encoding. Input is hex, `--ur-type` sets the UR type tag:

```
swift run keystone-qr display --ur-type bytes "deadbeefcafebabe..."
HEX=$(head -c 512 /dev/urandom | xxd -p | tr -d '\n')
swift run keystone-qr display --ur-type bytes --fragment-len 100 --interval 150 "$HEX"
```

- `--ur-type` — UR type identifier (triggers animated mode)
- `--fragment-len` — max bytes per fountain fragment (default: 200)
- `--interval` — milliseconds between frames (default: 200)

Ctrl-C to stop.

### Scan from camera

Reads QR codes from your webcam and reassembles fountain-coded UR data:

```
swift run keystone-qr scan --camera
```

### Scan from screen

Reads QR codes displayed on your screen (useful for cross-device testing or reading QR from another window):

```
swift run keystone-qr scan --screen
```

For single (non-animated) QR codes, add `--single`:

```
swift run keystone-qr scan --screen --single
swift run keystone-qr scan --camera --single
```

### E2E loop

Display in one terminal, scan from screen in another:

```
# Terminal 1: display animated QR
HEX=$(head -c 512 /dev/urandom | xxd -p | tr -d '\n')
swift run keystone-qr display --ur-type bytes "$HEX"

# Terminal 2: scan from screen
swift run keystone-qr scan --screen
```

**Permissions**: screen scanning requires Screen Recording permission for your terminal app (System Settings > Privacy > Screen Recording). Camera scanning requires Camera permission.

## Architecture

```
Sources/keystone-qr/
├── KeystoneQR.swift        # Entry point, subcommand routing
├── DisplayCommand.swift    # Static + animated QR display
├── ScanCommand.swift       # Scan orchestration, URDecoder progress, output
├── CameraScanner.swift     # AVFoundation webcam capture + Vision QR detection
├── ScreenScanner.swift     # ScreenCaptureKit capture + Vision QR detection
└── TerminalQR.swift        # CIFilter QR generation → Unicode half-block rendering
```

**TerminalQR** generates QR modules via `CIQRCodeGenerator`, reads the pixel bitmap, and renders using `▀` half-block characters with ANSI colors — two pixel rows per terminal line. Includes a 4-module quiet zone for scannability.

**DisplayCommand** handles two modes: static (single QR from any string) and animated (UR fountain encoder cycling frames). Animated mode clears the terminal and redraws each frame.

**CameraScanner** captures video frames via AVFoundation and detects QR codes using Vision's `VNDetectBarcodesRequest`.

**ScreenScanner** takes screenshots via ScreenCaptureKit and detects QR codes using Vision. Polls at ~10fps.

Both scanners feed detected QR strings into URKit's `URDecoder` which handles fountain code reassembly, showing a live progress bar until all fragments are received.

## Dependencies

- [URKit](https://github.com/BlockchainCommons/URKit) 15.x — UR encoding with fountain codes
- [swift-argument-parser](https://github.com/apple/swift-argument-parser) — CLI framework
- CoreImage (system) — QR code generation
- AVFoundation (system) — webcam capture
- Vision (system) — QR code detection
- ScreenCaptureKit (system) — screen capture
