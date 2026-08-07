package app

const (
	// V120RC1UpgradeName is the staging rehearsal plan for v1.2.0-rc.1.
	V120RC1UpgradeName = "v1.2.0-rc.1"

	// V120UpgradeName coordinates the final v1.2.0 cutover on both networks.
	V120UpgradeName = "v1.2.0"
)

// registerV120Upgrade registers the coordinated delegation-validation cutover.
// Mainnet also picks up the Ironwood verifier changes that staging already
// activated through ironwood-v1. No store migration is required.
func (app *SvoteApp) registerV120Upgrade() {
	app.registerNoopUpgrade(V120RC1UpgradeName)
	app.registerNoopUpgrade(V120UpgradeName)
}
