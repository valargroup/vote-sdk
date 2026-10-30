package app

// V180UpgradeName coordinates acceptance of NU7 delegation signing profiles.
const V180UpgradeName = "v1.8.0"

// registerV180Upgrade coordinates the expanded delegation validation rule.
// Existing NU6.3 authorizations and all stores remain valid.
func (app *SvoteApp) registerV180Upgrade() {
	app.registerNoopUpgrade(V180UpgradeName)
}
