package upgrade

// Hardcoded upgrade parameters
var (
	// UpgradeBlockHeight is the block height at which the sequencer upgrade activates
	// For testing, set this to a low value (e.g., 50)
	// TODO: add sequencer update logic
	UpgradeBlockHeight int64 = 10
)

// IsUpgraded returns true if the given height is at or after the upgrade height
func IsUpgraded(height int64) bool {
	return height >= UpgradeBlockHeight
}
