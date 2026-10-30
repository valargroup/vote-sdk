package app

// V180UpgradeName coordinates the cutover to NU7 delegation authorizations.
const V180UpgradeName = "v1.8.0"

// registerV180Upgrade coordinates the NU7-only delegation validation rule.
// Existing stores are preserved; pending NU6.3 authorizations need new signatures.
func (app *SvoteApp) registerV180Upgrade() {
	app.registerNoopUpgrade(V180UpgradeName)
}
