package app

const (
	// V131UpgradeName coordinates the v1.3.1 patch cutover on both networks.
	V131UpgradeName = "v1.3.1"
)

// registerV131Upgrade registers the restart-safe helper patch cutover.
// No store migration is required.
func (app *SvoteApp) registerV131Upgrade() {
	app.registerNoopUpgrade(V131UpgradeName)
}
