package sequencer

import (
	"sync"

	"github.com/morph-l2/go-ethereum/common"
)

// BlockRingBuffer stores recent applied blocks for serving peer requests.
// Maintains a contiguous range of blocks [minHeight, maxHeight].
// Supports reorg: when replacing a block, all higher blocks are removed.
type BlockRingBuffer struct {
	blocks   []*BlockV2
	capacity int

	// Height tracking
	minHeight uint64
	maxHeight uint64

	// Fast lookup (O(1))
	byHeight map[uint64]*BlockV2
	byHash   map[common.Hash]*BlockV2
	position map[uint64]int // height -> index in blocks array

	mtx sync.RWMutex
}

// NewBlockRingBuffer creates a new ring buffer.
func NewBlockRingBuffer(capacity int) *BlockRingBuffer {
	return &BlockRingBuffer{
		blocks:   make([]*BlockV2, capacity),
		capacity: capacity,
		byHeight: make(map[uint64]*BlockV2),
		byHash:   make(map[common.Hash]*BlockV2),
		position: make(map[uint64]int),
	}
}

// Add adds a block to the cache.
// - If first block: initializes cache
// - If height == maxHeight+1: appends (normal case)
// - If height exists with different hash: reorg, rollback higher blocks and replace
// - If height exists with same hash: no-op
// Returns false if block cannot be added (non-contiguous).
func (rb *BlockRingBuffer) Add(block *BlockV2) bool {
	rb.mtx.Lock()
	defer rb.mtx.Unlock()

	height := block.Number

	// Case 1: First block
	if len(rb.byHeight) == 0 {
		return rb.addAt(block, 0)
	}

	// Case 2: Normal append
	if height == rb.maxHeight+1 {
		return rb.append(block)
	}

	// Case 3: Same height exists
	if old, exists := rb.byHeight[height]; exists {
		if old.Hash == block.Hash {
			return true // Same block, no-op
		}
		// Different hash = reorg, rollback and replace
		rb.rollbackTo(height - 1)
		return rb.append(block)
	}

	// Case 4: Height within range but somehow missing (shouldn't happen)
	if height >= rb.minHeight && height <= rb.maxHeight {
		return false
	}

	// Case 5: Non-contiguous, reject
	return false
}

// addAt adds block at specific position (internal).
func (rb *BlockRingBuffer) addAt(block *BlockV2, pos int) bool {
	rb.blocks[pos] = block
	rb.byHeight[block.Number] = block
	rb.byHash[block.Hash] = block
	rb.position[block.Number] = pos
	rb.minHeight = block.Number
	rb.maxHeight = block.Number
	return true
}

// append adds block at maxHeight+1 (internal).
func (rb *BlockRingBuffer) append(block *BlockV2) bool {
	count := len(rb.byHeight)

	// Calculate position
	var pos int
	if count == 0 {
		pos = 0
	} else {
		pos = (rb.position[rb.maxHeight] + 1) % rb.capacity
	}

	// Evict oldest if full
	if count >= rb.capacity {
		rb.evictOldest()
	}

	// Add
	rb.blocks[pos] = block
	rb.byHeight[block.Number] = block
	rb.byHash[block.Hash] = block
	rb.position[block.Number] = pos
	rb.maxHeight = block.Number

	if count == 0 {
		rb.minHeight = block.Number
	}

	return true
}

// evictOldest removes the oldest block (internal).
func (rb *BlockRingBuffer) evictOldest() {
	if len(rb.byHeight) == 0 {
		return
	}
	old := rb.byHeight[rb.minHeight]
	if old != nil {
		pos := rb.position[rb.minHeight]
		rb.blocks[pos] = nil // Help GC
		delete(rb.byHeight, rb.minHeight)
		delete(rb.byHash, old.Hash)
		delete(rb.position, rb.minHeight)
		rb.minHeight++
	}
}

// rollbackTo removes all blocks with height > targetHeight.
// Used during reorg.
func (rb *BlockRingBuffer) rollbackTo(targetHeight uint64) {
	for h := rb.maxHeight; h > targetHeight; h-- {
		block := rb.byHeight[h]
		if block != nil {
			pos := rb.position[h]
			rb.blocks[pos] = nil // Help GC
			delete(rb.byHeight, h)
			delete(rb.byHash, block.Hash)
			delete(rb.position, h)
		}
	}
	rb.maxHeight = targetHeight
}

// GetByHeight returns a block by height.
func (rb *BlockRingBuffer) GetByHeight(height uint64) *BlockV2 {
	rb.mtx.RLock()
	defer rb.mtx.RUnlock()
	return rb.byHeight[height]
}

// GetByHash returns a block by hash.
func (rb *BlockRingBuffer) GetByHash(hash common.Hash) *BlockV2 {
	rb.mtx.RLock()
	defer rb.mtx.RUnlock()
	return rb.byHash[hash]
}

// LatestHeight returns the highest block height.
func (rb *BlockRingBuffer) LatestHeight() uint64 {
	rb.mtx.RLock()
	defer rb.mtx.RUnlock()
	return rb.maxHeight
}

// HeightRange returns the min and max height in buffer.
func (rb *BlockRingBuffer) HeightRange() (min, max uint64) {
	rb.mtx.RLock()
	defer rb.mtx.RUnlock()
	return rb.minHeight, rb.maxHeight
}

// Count returns the number of blocks.
func (rb *BlockRingBuffer) Count() int {
	rb.mtx.RLock()
	defer rb.mtx.RUnlock()
	return len(rb.byHeight)
}

// RollbackTo removes all blocks with height > targetHeight.
// Exposed for external use during explicit reorg.
func (rb *BlockRingBuffer) RollbackTo(targetHeight uint64) {
	rb.mtx.Lock()
	defer rb.mtx.Unlock()
	rb.rollbackTo(targetHeight)
}
