package consensus

import (
	"fmt"
	"time"

	ethcrypto "github.com/scroll-tech/go-ethereum/crypto"
	"github.com/tendermint/tendermint/types"
)

// currentHeight should be greater than InitialHeight
func (cs *State) getBatchStartHeight() (int64, time.Time) {
	if checkBLS(cs.ProposalBlock.LastCommit.Signatures) {
		return cs.ProposalBlock.Height, cs.ProposalBlock.Time
	}
	for i := cs.ProposalBlock.Height - 1; ; i-- {
		if i == cs.state.InitialHeight {
			return i, cs.blockStore.LoadBlock(i).Time
		}
		block := cs.blockStore.LoadBlock(i)
		if checkBLS(block.LastCommit.Signatures) {
			fmt.Println(block.Time)
			return block.Height, block.Time
		}
	}
}

func (cs *State) isBatchPoint(batchStartHeight int64, batchContext []byte, batchStartTime time.Time) bool {
	// batch_blocks_interval, batch_max_bytes and batch_timeout can't be all 0
	// block_interval || max_bytes || timeout
	return (cs.config.BatchBlocksInterval > 0 && cs.ProposalBlock.Height >= batchStartHeight+cs.config.BatchBlocksInterval-1) ||
		(cs.config.BatchMaxBytes > 0 && len(batchContext) >= int(cs.config.BatchMaxBytes)) ||
		(cs.config.BatchTimeout > 0 && cs.ProposalBlock.Time.Sub(batchStartTime) >= cs.config.BatchTimeout)
}

func (cs *State) batchContext(batchStartHeight int64) []byte {
	var zkConfigContext []byte
	var txsContext []byte
	for i := batchStartHeight; i < cs.ProposalBlock.Height; i++ {
		block := cs.blockStore.LoadBlock(batchStartHeight)
		zkConfigContext = append(zkConfigContext, block.Data.ZkConfig...)
		for _, tx := range block.Data.Txs {
			txsContext = append(txsContext, tx...)
		}
	}
	zkConfigContext = append(zkConfigContext, cs.ProposalBlock.Data.ZkConfig...)
	for _, tx := range cs.ProposalBlock.Data.Txs {
		txsContext = append(txsContext, tx...)
	}

	return append(zkConfigContext, txsContext...)
}

func (cs *State) batchContextHash(batchContext []byte) []byte {
	return ethcrypto.Keccak256(batchContext)
}

func checkBLS(signatures []types.CommitSig) bool {
	for _, sig := range signatures {
		if len(sig.BLSSignature) > 0 {
			return true
		}
	}
	return false
}
