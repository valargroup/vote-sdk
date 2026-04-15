// Package ui serves the admin SPA as static files from a pre-built
// Vite dist directory. It provides a catch-all SPA fallback that serves
// index.html for any path not matched by other routes.
package ui

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"cosmossdk.io/log"
	"github.com/gorilla/mux"
)

// Config holds the UI server configuration, read from app.toml [ui].
type Config struct {
	Enable   bool   `mapstructure:"enable"`
	DistPath string `mapstructure:"dist_path"`
}

// RegisterRoutes registers static file serving and SPA fallback routes.
// Must be called AFTER all API routes are registered, since the catch-all
// PathPrefix("/") would otherwise shadow more specific routes. Gorilla mux
// matches most-specific first, so explicit routes always win.
func RegisterRoutes(router *mux.Router, distPath string, logger log.Logger) error {
	absPath, err := filepath.Abs(distPath)
	if err != nil {
		return fmt.Errorf("resolve dist path: %w", err)
	}

	indexPath := filepath.Join(absPath, "index.html")
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		return fmt.Errorf("index.html not found in %s", absPath)
	}

	fs := http.FileServer(http.Dir(absPath))

	// Serve /assets/* directly (Vite's hashed JS/CSS bundles).
	router.PathPrefix("/assets/").Handler(fs)

	// SPA fallback: serve index.html for all unmatched paths.
	router.PathPrefix("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If the file exists on disk, serve it (e.g. /vite.svg, /favicon.ico).
		filePath := filepath.Join(absPath, filepath.Clean(r.URL.Path))
		if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
			fs.ServeHTTP(w, r)
			return
		}
		// Otherwise serve index.html for SPA client-side routing.
		http.ServeFile(w, r, indexPath)
	})

	logger.Info("UI server enabled", "dist", absPath)
	return nil
}
