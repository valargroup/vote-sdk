package pir

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"context"

	"cosmossdk.io/log"
)

// ErrNotEmbedded is returned when svoted was built without the embed_pir tag.
var ErrNotEmbedded = fmt.Errorf("nf-server binary not embedded (build with -tags embed_pir)")

// ExtractTo writes the embedded nf-server binary to $homeDir/bin/nf-server,
// skipping the write if the file already exists with the correct SHA-256.
// Returns the path to the extracted binary.
func ExtractTo(homeDir string) (string, error) {
	if len(nfServerBinary) == 0 {
		return "", ErrNotEmbedded
	}

	binDir := filepath.Join(homeDir, "bin")
	binPath := filepath.Join(binDir, "nf-server")

	wantHash := sha256.Sum256(nfServerBinary)
	wantHex := hex.EncodeToString(wantHash[:])

	if existing, err := os.Open(binPath); err == nil {
		h := sha256.New()
		if _, err := io.Copy(h, existing); err == nil {
			existing.Close()
			if hex.EncodeToString(h.Sum(nil)) == wantHex {
				return binPath, nil
			}
		} else {
			existing.Close()
		}
	}

	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", fmt.Errorf("create bin dir: %w", err)
	}

	if err := os.WriteFile(binPath, nfServerBinary, 0o755); err != nil {
		return "", fmt.Errorf("write nf-server binary: %w", err)
	}

	return binPath, nil
}

// Run creates an exec.Cmd for the nf-server binary bound to the given context.
// Stdout and stderr are piped to the provided logger.
func Run(ctx context.Context, binPath string, logger log.Logger, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Stdout = &logWriter{logger: logger, level: "info"}
	cmd.Stderr = &logWriter{logger: logger, level: "error"}
	return cmd
}

// logWriter adapts a cosmossdk logger to an io.Writer for subprocess output.
type logWriter struct {
	logger log.Logger
	level  string
}

func (w *logWriter) Write(p []byte) (int, error) {
	msg := string(p)
	if len(msg) > 0 && msg[len(msg)-1] == '\n' {
		msg = msg[:len(msg)-1]
	}
	if msg == "" {
		return len(p), nil
	}
	switch w.level {
	case "error":
		w.logger.Error(msg)
	default:
		w.logger.Info(msg)
	}
	return len(p), nil
}
