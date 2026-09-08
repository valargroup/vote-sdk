package app

const (
	// V150UpgradeName coordinates atomic vote batch activation on both networks.
	V150UpgradeName = "v1.5.0"
)

// registerV150Upgrade registers the atomic vote batch binary cutover.
// The accepted transaction set changes without a store migration.
func (app *SvoteApp) registerV150Upgrade() {
	app.registerNoopUpgrade(V150UpgradeName)
}
