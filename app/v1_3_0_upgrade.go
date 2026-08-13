package app

const (
	// V130RC1UpgradeName is the staging rehearsal plan for v1.3.0-rc.1.
	V130RC1UpgradeName = "v1.3.0-rc.1"

	// V130UpgradeName coordinates the final v1.3.0 cutover.
	V130UpgradeName = "v1.3.0"
)

// registerV130Upgrade registers the coordinated voting-verifier cutover.
// No store migration is required.
func (app *SvoteApp) registerV130Upgrade() {
	app.registerNoopUpgrade(V130RC1UpgradeName)
	app.registerNoopUpgrade(V130UpgradeName)
}
