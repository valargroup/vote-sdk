package app

// V170UpgradeName coordinates activation of DKG constant-term proofs.
const V170UpgradeName = "v1.7.0"

// registerV170Upgrade coordinates the new contribution validation rule.
// Existing rounds and stores are preserved.
func (app *SvoteApp) registerV170Upgrade() {
	app.registerNoopUpgrade(V170UpgradeName)
}
