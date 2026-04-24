package l2node

import (
	"github.com/tendermint/tendermint/types"
)

// BlockV2 is an alias to types.BlockV2 for convenience.
type BlockV2 = types.BlockV2

type L2Node interface {
	RequestHeight(
		tmHeight int64,
	) (
		height int64,
		err error,
	)

	EncodeTxs(
		batchTxs [][]byte,
	) (
		encodedTxs []byte,
		err error,
	)

	RequestBlockData(
		height int64,
	) (
		txs [][]byte,
		blockMeta []byte,
		collectedL1Msgs bool,
		err error,
	)

	CheckBlockData(
		txs [][]byte,
		blockMeta []byte,
	) (
		valid bool,
		err error,
	)

	DeliverBlock(
		txs [][]byte,
		blockMeta []byte,
		consensusData ConsensusData,
	) (
		nextValidatorSet [][]byte,
		err error,
	)

	// ==================== V2 Methods for Sequencer Mode ====================
	// These methods are used after the upgrade to centralized sequencer mode.
	// They operate on BlockV2 (based on ExecutableL2Data) instead of raw txs/blockMeta.

	// RequestBlockDataV2 requests block data based on parent hash (for fork chain support).
	// Uses Engine API to assemble a new block.
	RequestBlockDataV2(parentHash []byte) (block *BlockV2, collectedL1Msgs bool, err error)

	// ApplyBlockV2 applies a BlockV2 to the L2 execution layer.
	// Uses Engine API (NewL2Block) internally.
	ApplyBlockV2(block *BlockV2) error

	// GetBlockByNumber retrieves a BlockV2 by its number.
	// Can be implemented using geth's eth_getBlockByNumber JSON-RPC.
	GetBlockByNumber(height uint64) (*BlockV2, error)

	// GetLatestBlockV2 returns the latest block.
	// Can be implemented using geth's eth_blockNumber + eth_getBlockByNumber JSON-RPC.
	GetLatestBlockV2() (*BlockV2, error)
}

type ConsensusData struct {
	ValidatorSet [][]byte
}

func ConvertBytesToTxs(txs [][]byte) []types.Tx {
	s := make([]types.Tx, len(txs))
	for i, v := range txs {
		s[i] = v
	}
	return s
}

func ConvertTxsToBytes(txs []types.Tx) [][]byte {
	s := make([][]byte, len(txs))
	for i, v := range txs {
		s[i] = v
	}
	return s
}
