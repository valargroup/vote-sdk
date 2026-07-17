package app

const (
	// V1UpgradeName is the canonical x/upgrade plan name for the first
	// mainnet major upgrade.
	V1UpgradeName = "v1"
)

// registerV1Upgrade registers a chain-agnostic no-op upgrade scaffold that can
// be safely scheduled on svote-1, zvote-1, and test chains.
func (app *SvoteApp) registerV1Upgrade() {
	app.registerNoopUpgrade(V1UpgradeName)
}
