package admin

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"cosmossdk.io/log"
	"github.com/gorilla/mux"
)

// UIMode controls which features the admin UI exposes.
//
// "prod" hides developer-only controls such as the in-process PIR rebuild
// triggers. "dev" enables them. Production deployments must explicitly set
// SVOTE_UI_MODE=dev to opt in to the developer surface; the default is "prod"
// so a forgotten environment file can never accidentally expose them.
type UIMode string

const (
	UIModeProd UIMode = "prod"
	UIModeDev  UIMode = "dev"

	uiModeEnv             = "SVOTE_UI_MODE"
	precomputedBaseURLEnv = "SVOTE_PRECOMPUTED_BASE_URL"

	// DefaultPrecomputedBaseURL is the production DigitalOcean Spaces bucket
	// where the publish-snapshot workflow uploads pre-computed PIR snapshots.
	// Per-deployment overrides go through SVOTE_PRECOMPUTED_BASE_URL so a
	// staging svoted can point at a staging bucket.
	DefaultPrecomputedBaseURL = "https://vote.fra1.digitaloceanspaces.com"
)

// resolveUIMode reads SVOTE_UI_MODE and falls back to the safe-by-default prod
// mode for any unset, empty, or unrecognised value.
func resolveUIMode() UIMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(uiModeEnv))) {
	case "dev", "development":
		return UIModeDev
	default:
		return UIModeProd
	}
}

// resolvePrecomputedBaseURL reads SVOTE_PRECOMPUTED_BASE_URL, falling back to
// the production default. Trailing slashes are trimmed so consumers can
// concatenate well-known subpaths without worrying about doubled slashes.
func resolvePrecomputedBaseURL() string {
	raw := strings.TrimSpace(os.Getenv(precomputedBaseURLEnv))
	if raw == "" {
		raw = DefaultPrecomputedBaseURL
	}
	return strings.TrimRight(raw, "/")
}

// UIConfigResponse is the wire format returned by GET /api/ui-config.
//
// It carries everything the static admin UI bundle needs to know about the
// svoted instance it's served by. New fields should be additive — older UI
// builds must continue to work against newer servers.
type UIConfigResponse struct {
	// Mode is "dev" or "prod". The UI uses this to gate developer-only widgets.
	Mode UIMode `json:"mode"`
	// DevPIRControls is a denormalised flag for the most common gate so the UI
	// does not need to know the meaning of each mode value.
	DevPIRControls bool `json:"dev_pir_controls"`
	// PrecomputedBaseURL is the bucket origin that this svoted's PIR siblings
	// fetch their snapshots from. The UI composes the manifest URL as
	// "<base>/snapshots/<height>/manifest.json" using a shared subpath
	// constant. Resolved at startup from SVOTE_PRECOMPUTED_BASE_URL with a
	// production-bucket default. Has no trailing slash.
	PrecomputedBaseURL string `json:"precomputed_base_url"`
}

// RegisterUIConfigRoutes registers GET /api/ui-config on the given router.
//
// All values are resolved once at registration time from environment vars.
// Changing them requires a restart, which matches how the rest of the daemon
// reads its configuration.
func RegisterUIConfigRoutes(router *mux.Router, logger log.Logger) {
	mode := resolveUIMode()
	precomputedBase := resolvePrecomputedBaseURL()
	resp := UIConfigResponse{
		Mode:               mode,
		DevPIRControls:     mode == UIModeDev,
		PrecomputedBaseURL: precomputedBase,
	}
	logger.Info("ui config resolved",
		"mode", string(mode),
		"precomputed_base_url", precomputedBase,
		"mode_env", uiModeEnv,
		"precomputed_env", precomputedBaseURLEnv,
	)

	router.HandleFunc("/api/ui-config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			corsHeaders(w)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		corsHeaders(w)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(resp)
	}).Methods(http.MethodGet, http.MethodOptions)
}
