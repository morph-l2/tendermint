package consensus

import (
	"time"

	"github.com/tendermint/tendermint/crypto/tmhash"

	"github.com/tendermint/tendermint/types"
)

// batchData is used to store the cached batchHash/batchHeader mapping to blockHash, preventing duplicated calculation
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
