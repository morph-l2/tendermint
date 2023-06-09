package consensus

import (
	sm "github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/types"

	ethcrypto "github.com/scroll-tech/go-ethereum/crypto"
)

// TODO Signatures[0] nil?
// currentHeight should be greater than InitialHeight
func GetBatchStartHeight(initialHeight int64, proposalBlock *types.Block, blockStore sm.BlockStore) int64 {
	if len(proposalBlock.LastCommit.Signatures[0].BLSSignature) > 0 {
		return proposalBlock.Height
	}
	for i := proposalBlock.Height - 1; ; i-- {
		if i == initialHeight {
			return initialHeight
		}
		block := blockStore.LoadBlock(i)
		if len(block.LastCommit.Signatures[0].BLSSignature) > 0 {
			return block.Height
		}
	}
}

func IsBatchPoint(currentHeight int64, batchStartHeight int64, batchInterval int64) bool {
	if currentHeight >= batchStartHeight+batchInterval-1 {
		return true
	}
	return false
}

func BatchContextHash(
	batchStartHeight int64,
	blockStore sm.BlockStore,
	proposalBlock *types.Block,
) []byte {
	var zkConfigContext []byte
	var txsContext []byte
	for i := batchStartHeight; i < proposalBlock.Height; i++ {
		block := blockStore.LoadBlock(batchStartHeight)
		zkConfigContext = append(zkConfigContext, block.Data.ZkConfig...)
		for _, tx := range block.Data.Txs {
			txsContext = append(txsContext, tx...)
		}
	}
	zkConfigContext = append(zkConfigContext, proposalBlock.Data.ZkConfig...)
	for _, tx := range proposalBlock.Data.Txs {
		txsContext = append(txsContext, tx...)
	}

	return ethcrypto.Keccak256(
		append(zkConfigContext, txsContext...),
	)
}
