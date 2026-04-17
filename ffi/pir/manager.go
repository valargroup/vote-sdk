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
//
// Stdout and stderr are wired to os.Stderr via a prefixing writer so the
// child's output interleaves with svoted's own stderr. Earlier iterations
// of this code routed both streams through the Cosmos logger, but that
// path occasionally resulted in child output never surfacing when svoted
// was under load (the root cause was never fully pinned down, but direct
// writes to os.Stderr are both simpler and guaranteed to flush because
// stderr is unbuffered by the Go runtime). The `logger` argument is kept
// for future use but only consumed when the caller chooses logWriter.
func Run(ctx context.Context, binPath string, _ log.Logger, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Stdout = &prefixWriter{w: os.Stderr, prefix: []byte("[nf-server] ")}
	cmd.Stderr = &prefixWriter{w: os.Stderr, prefix: []byte("[nf-server] ")}
	return cmd
}

// prefixWriter writes each \n-terminated line from the subprocess to w,
// prefixed with the configured tag so child output is easy to grep in the
// combined svoted/nf-server log stream.
type prefixWriter struct {
	w      io.Writer
	prefix []byte
	buf    []byte
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		i := indexByte(p.buf, '\n')
		if i < 0 {
			break
		}
		line := p.buf[:i+1]
		if _, err := p.w.Write(p.prefix); err != nil {
			return 0, err
		}
		if _, err := p.w.Write(line); err != nil {
			return 0, err
		}
		p.buf = p.buf[i+1:]
	}
	return len(b), nil
}

// indexByte is a tiny reimplementation of bytes.IndexByte to avoid pulling
// the bytes package into this file's already-heavy import set.
func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}
