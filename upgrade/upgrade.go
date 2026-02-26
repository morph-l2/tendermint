package upgrade

// Hardcoded upgrade parameters
var (
	// UpgradeBlockHeight is the block height at which the consensus switches to sequencer mode.
	// Default is -1 (upgrade disabled). Set via --consensus.switchHeight flag.
	// When < 0, the upgrade never activates.
	UpgradeBlockHeight int64 = -1
)

// IsUpgraded returns true if the given height is at or after the upgrade height.
// Returns false when UpgradeBlockHeight < 0 (upgrade disabled).
func IsUpgraded(height int64) bool {
	if UpgradeBlockHeight < 0 {
		return false
	}
	return height >= UpgradeBlockHeight
}

// SetUpgradeBlockHeight sets the upgrade block height.
func SetUpgradeBlockHeight(height int64) {
	UpgradeBlockHeight = height
}
