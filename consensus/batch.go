package consensus

import (
	"time"

	"github.com/tendermint/tendermint/l2node"
	sm "github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/types"
)

// currentHeight should be greater than InitialHeight
func (cs *State) getBatchStart(proposalBlock *types.Block) (int64, time.Time) {
	prHeight := cs.Height - 1
	if CheckBLS(proposalBlock.LastCommit.Signatures) {
		return prHeight, cs.blockStore.LoadBlock(prHeight).Time
	}
	for i := prHeight; ; i-- {
		if i == cs.state.InitialHeight {
			return i, cs.blockStore.LoadBlock(i).Time
		}
		block := cs.blockStore.LoadBlock(i)
		if CheckBLS(block.LastCommit.Signatures) {
			preBlock := cs.blockStore.LoadBlock(i - 1)
			return preBlock.Height, preBlock.Time
		}
	}
}

func (cs *State) isBatchPoint(batchStartHeight int64, batchSize int, batchStartTime time.Time, blockTime time.Time) bool {
	// batch_blocks_interval, batch_max_bytes and batch_timeout can't be all 0
	// block_interval || max_bytes || timeout
	return (cs.state.ConsensusParams.Batch.BlocksInterval > 0 && cs.Height-batchStartHeight >= cs.state.ConsensusParams.Batch.BlocksInterval) ||
		(cs.state.ConsensusParams.Batch.MaxBytes > 0 && batchSize >= int(cs.state.ConsensusParams.Batch.MaxBytes)) ||
		(cs.state.ConsensusParams.Batch.Timeout > 0 && blockTime.Sub(batchStartTime) >= cs.state.ConsensusParams.Batch.Timeout)
}

func (cs *State) batchData(batchStartHeight int64) (zkConfigContext []byte, rawBatchTxs [][]byte, root []byte) {
	for i := batchStartHeight; i < cs.Height; i++ {
		block := cs.blockStore.LoadBlock(i)
		zkConfigContext = append(zkConfigContext, block.Data.ZkConfig...)
		for _, tx := range block.Data.Txs {
			rawBatchTxs = append(rawBatchTxs, tx)
		}
	}
	root = cs.blockStore.LoadBlock(cs.Height - 1).Data.Root
	return
}

func (cs *State) batchContext(zkConfigContext []byte, encodedTxs []byte, root []byte) []byte {
	return append(append(zkConfigContext, encodedTxs...), root...)
}

func (cs *State) proposalBlockRawTxs(proposalBlock *types.Block) (rawTxs [][]byte) {
	for _, tx := range proposalBlock.Data.Txs {
		rawTxs = append(rawTxs, tx)
	}
	return
}

func CheckBLS(signatures []types.CommitSig) bool {
	for _, sig := range signatures {
		if len(sig.BLSSignature) > 0 {
			return true
		}
	}
	return false
}

func txsSize(batchTxs [][]byte) int {
	sum := 0
	for _, tx := range batchTxs {
		sum += len(tx)
	}
	return sum
}

func GetBatchContext(
	l2Node l2node.L2Node,
	blockStore sm.BlockStore,
	initialHeight int64,
	endHeight int64,
) []byte {
	var zkConfigContext []byte
	var rawBatchTxs [][]byte
	var root []byte
	var startHeight int64
	if endHeight < endHeight {
		panic("invalid block height")
	}
	for i := endHeight; ; i-- {
		if i == initialHeight {
			startHeight = i
			break
		}
		block := blockStore.LoadBlock(i)
		if CheckBLS(block.LastCommit.Signatures) {
			preBlock := blockStore.LoadBlock(i - 1)
			startHeight = preBlock.Height
		}
	}
	for i := startHeight; i <= endHeight; i++ {
		block := blockStore.LoadBlock(i)
		zkConfigContext = append(zkConfigContext, block.Data.ZkConfig...)
		for _, tx := range block.Data.Txs {
			rawBatchTxs = append(rawBatchTxs, tx)
		}
	}
	root = blockStore.LoadBlock(endHeight).Data.Root
	encodedTxs, err := l2Node.EncodeTxs(rawBatchTxs)
	if err != nil {
		panic(err)
	}
	return append(append(zkConfigContext, encodedTxs...), root...)
}
