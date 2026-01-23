package upgrade

import (
	"github.com/morph-l2/go-ethereum/common"
)

// Hardcoded upgrade parameters
var (
	// UpgradeBlockHeight is the block height at which the sequencer upgrade activates
	// For testing, set this to a low value (e.g., 50)
	// TODO: add sequencer update logic
	UpgradeBlockHeight int64 = 10

	// SequencerAddress is the address of the centralized sequencer
	// Default: Hardhat test account #0
	// TODO: add sequencer update logic
	SequencerAddress = common.HexToAddress("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266")
)

// IsUpgraded returns true if the given height is at or after the upgrade height
func IsUpgraded(height int64) bool {
	return height >= UpgradeBlockHeight
}

// IsSequencer returns true if the given address is the sequencer address
func IsSequencer(addr common.Address) bool {
	return addr == SequencerAddress
}

// SetUpgradeBlockHeight sets the upgrade block height (for testing)
func SetUpgradeBlockHeight(height int64) {
	UpgradeBlockHeight = height
}

// SetSequencerAddress sets the sequencer address (for testing)
func SetSequencerAddress(addr common.Address) {
	SequencerAddress = addr
}
