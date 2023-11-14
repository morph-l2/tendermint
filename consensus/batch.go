package consensus

import (
	"fmt"
	"github.com/tendermint/tendermint/crypto/tmhash"
	"time"

	"github.com/tendermint/tendermint/types"
)

type batchData struct {
	batchHash   []byte
	batchHeader []byte
}

type BatchCache struct {
	BatchStartHeight          int64
	BatchStartTime            time.Time
	ParentBatchHeader         []byte
	BlocksSinceLastBatchPoint []*types.Block // Notice: order by desc

	BatchHashes map[[tmhash.Size]byte]batchData // blockHash -> batchData
}

func NewBatchCache() *BatchCache {
	return &BatchCache{
		ParentBatchHeader:         make([]byte, 0),
		BlocksSinceLastBatchPoint: make([]*types.Block, 0),
		BatchHashes:               make(map[[tmhash.Size]byte]batchData),
	}
}

func (bc *BatchCache) UpdateStartPoint(block *types.Block) {
	bc.BatchStartHeight = block.Height
	bc.BatchStartTime = block.Time
	bc.ParentBatchHeader = block.L2BatchHeader
	bc.BlocksSinceLastBatchPoint = []*types.Block{block}
}

func (bc *BatchCache) AppendBlock(block *types.Block) {
	bc.BlocksSinceLastBatchPoint = append(bc.BlocksSinceLastBatchPoint, block)
}

func (bc *BatchCache) StoreBatchData(blockHash []byte, batchHash []byte, batchHeader []byte) {
	var blockHashKey [tmhash.Size]byte
	copy(blockHashKey[:], blockHash)

	bc.BatchHashes[blockHashKey] = batchData{
		batchHash:   batchHash,
		batchHeader: batchHeader,
	}
}

func (bc *BatchCache) ClearBatchData() {
	bc.BatchHashes = make(map[[tmhash.Size]byte]batchData)
}

func (bc *BatchCache) batchData(blockHash []byte) (batchData batchData, found bool) {
	var blockHashKey [tmhash.Size]byte
	copy(blockHashKey[:], blockHash)
	batchData, found = bc.BatchHashes[blockHashKey]
	return
}

// currentHeight should be greater than InitialHeight
func (cs *State) getBatchStart() (int64, time.Time) {
	if cs.batchCache != nil && cs.batchCache.BatchStartHeight != 0 {
		return cs.batchCache.BatchStartHeight, cs.batchCache.BatchStartTime
	}
	if cs.batchCache == nil {
		cs.batchCache = NewBatchCache()
	}

	if cs.Height == cs.state.InitialHeight { // use genesis block as the first batch point
		return 0, cs.StartTime
	}

	var blocksByDesc []*types.Block // stores the blocks from last batch point block(which is not included) to now
	for i := cs.Height - 1; i >= cs.state.InitialHeight; i-- {
		block := cs.blockStore.LoadBlock(i)
		if block.IsBatchPoint() {
			cs.batchCache.UpdateStartPoint(block)
			break
		}
		if i == cs.state.InitialHeight {
			cs.batchCache.UpdateStartPoint(block)
			break
		}
		blocksByDesc = append(blocksByDesc, block)
	}

	// reverse `blocksByDesc`, and append them to batch cache
	for i := len(blocksByDesc) - 1; i >= 0; i-- {
		cs.batchCache.AppendBlock(blocksByDesc[i])
	}

	return cs.batchCache.BatchStartHeight, cs.batchCache.BatchStartTime
}

// currentHeight should be greater than InitialHeight
func (cs *State) getBatchStart2(proposalBlock *types.Block) (int64, time.Time) {
	if cs.batchCache != nil && cs.batchCache.BatchStartHeight != 0 {
		return cs.batchCache.BatchStartHeight, cs.batchCache.BatchStartTime
	}

	if cs.batchCache == nil {
		cs.batchCache = NewBatchCache()
	}
	prHeight := cs.Height - 1
	if CheckBLS(proposalBlock.LastCommit.Signatures) {
		lastBatchPointBlock := cs.blockStore.LoadBlock(prHeight)
		fmt.Printf("===========>start to UpdateStartPoint, step 1: %x \n", lastBatchPointBlock.BatchHash)
		cs.batchCache.UpdateStartPoint(lastBatchPointBlock)
		return lastBatchPointBlock.Height, lastBatchPointBlock.Time
	}
	for i := prHeight; ; i-- {
		if i == cs.state.InitialHeight {
			lastBatchPointBlock := cs.blockStore.LoadBlock(i)
			fmt.Printf("===========>start to UpdateStartPoint, step 2. batchPoint height: %d \n", lastBatchPointBlock.Height)
			cs.batchCache.UpdateStartPoint(lastBatchPointBlock)
			return lastBatchPointBlock.Height, lastBatchPointBlock.Time
		}

		block := cs.blockStore.LoadBlock(i)
		cs.batchCache.AppendBlock(block)
		if CheckBLS(block.LastCommit.Signatures) {
			preBlock := cs.blockStore.LoadBlock(i - 1)
			fmt.Printf("===========>start to UpdateStartPoint, step 3. batchPoint height: %d \n", preBlock.Height)
			cs.batchCache.UpdateStartPoint(preBlock)
			cs.batchCache.AppendBlock(preBlock)
			return preBlock.Height, preBlock.Time
		}
	}
}

func CheckBLS(signatures []types.CommitSig) bool {
	for _, sig := range signatures {
		if len(sig.BLSSignature) > 0 {
			return true
		}
	}
	return false
}
