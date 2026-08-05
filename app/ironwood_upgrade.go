package app

const (
	// IronwoodUpgradeName matches the production release for the verifier cutover.
	IronwoodUpgradeName = "v1.1.0"

	// StagingIronwoodUpgradeName is retained for the plan already applied on svote-1.
	StagingIronwoodUpgradeName = "ironwood-v1"
)

// registerIronwoodUpgrade registers the coordinated Ironwood binary cutover.
// The verifier changes without a store migration, so the handler is a no-op.
func (app *SvoteApp) registerIronwoodUpgrade() {
	app.registerNoopUpgrade(IronwoodUpgradeName)
	app.registerNoopUpgrade(StagingIronwoodUpgradeName)
}
