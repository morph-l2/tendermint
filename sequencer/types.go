package sequencer

import (
	"github.com/tendermint/tendermint/types"
)

// BlockV2 is an alias for types.BlockV2
type BlockV2 = types.BlockV2

// SyncableBlock is an alias for types.SyncableBlock
type SyncableBlock = types.SyncableBlock

// ExecutableL2Data is the internal representation matching go-ethereum's ExecutableL2Data.
// Used for type conversion with geth.
type ExecutableL2Data = types.BlockV2
