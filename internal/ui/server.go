// Package ui serves the admin SPA as static files from a pre-built
// Vite dist directory. It provides a catch-all SPA fallback that serves
// index.html for any path not matched by other routes.
package ui

import (
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"cosmossdk.io/log"
	"github.com/gorilla/mux"
)

// Config holds the UI server configuration, read from app.toml [ui].
type Config struct {
	Enable   bool   `mapstructure:"enable"`
	DistPath string `mapstructure:"dist_path"`
}

// RegisterRoutes registers static file serving and SPA fallback routes.
// The getDistPath function is called at request time so routes can be
// mounted before PostSetup sets the dist path (same pattern as helper).
// Must be called AFTER all API routes are registered, since the catch-all
// PathPrefix("/") would otherwise shadow more specific routes.
func RegisterRoutes(router *mux.Router, getDistPath func() string, logger log.Logger) {
	h := &uiHandler{getDistPath: getDistPath, logger: logger}

	router.PathPrefix("/assets/").HandlerFunc(h.serveStatic)
	router.PathPrefix("/").HandlerFunc(h.serveSPAFallback)
}

type uiHandler struct {
	getDistPath func() string
	logger      log.Logger

	// Lazy-init: resolved on first request with a valid dist path.
	once    sync.Once
	fs      http.Handler
	absPath string
}

func (h *uiHandler) resolve() (http.Handler, string) {
	distPath := h.getDistPath()
	if distPath == "" {
		return nil, ""
	}

	h.once.Do(func() {
		abs, err := filepath.Abs(distPath)
		if err != nil {
			h.logger.Error("resolve dist path", "error", err)
			return
		}
		indexPath := filepath.Join(abs, "index.html")
		if _, err := os.Stat(indexPath); os.IsNotExist(err) {
			h.logger.Error("index.html not found", "dist", abs)
			return
		}
		h.absPath = abs
		h.fs = http.FileServer(http.Dir(abs))
		h.logger.Info("UI server enabled", "dist", abs)
	})

	return h.fs, h.absPath
}

func (h *uiHandler) serveStatic(w http.ResponseWriter, r *http.Request) {
	fs, _ := h.resolve()
	if fs == nil {
		http.NotFound(w, r)
		return
	}
	fs.ServeHTTP(w, r)
}

func (h *uiHandler) serveSPAFallback(w http.ResponseWriter, r *http.Request) {
	fs, absPath := h.resolve()
	if fs == nil {
		http.NotFound(w, r)
		return
	}

	// If the file exists on disk, serve it directly (e.g. /vite.svg).
	filePath := filepath.Join(absPath, filepath.Clean(r.URL.Path))
	if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
		fs.ServeHTTP(w, r)
		return
	}

	// SPA fallback: serve index.html for client-side routing.
	http.ServeFile(w, r, filepath.Join(absPath, "index.html"))
}
