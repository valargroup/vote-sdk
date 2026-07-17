package app

const (
	// IronwoodUpgradeName is the x/upgrade plan name for the Ironwood verifier cutover.
	// It is distinct from the already-applied v1 plan.
	IronwoodUpgradeName = "ironwood-v1"
)

// registerIronwoodUpgrade registers the coordinated Ironwood binary cutover.
// The verifier changes without a store migration, so the handler is a no-op.
func (app *SvoteApp) registerIronwoodUpgrade() {
	app.registerNoopUpgrade(IronwoodUpgradeName)
}
