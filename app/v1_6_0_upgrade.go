package app

const (
	// V160UpgradeName coordinates activation of atomic delegation plus casting.
	V160UpgradeName = "v1.6.0"
)

// registerV160Upgrade registers the additive wire-protocol binary cutover.
// No store migration is required.
func (app *SvoteApp) registerV160Upgrade() {
	app.registerNoopUpgrade(V160UpgradeName)
}
