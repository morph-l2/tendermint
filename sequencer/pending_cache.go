package sequencer

import (
	"sync"

	"github.com/morph-l2/go-ethereum/common"
)

const (
	MaxPendingBlocks      = 500 // Max total blocks in cache
	MaxPendingHeightAhead  = 100 // Max height ahead of local
	MaxPendingHeightBehind = 20  // Max height behind local (for reorg)
)

// PendingBlockCache stores blocks that cannot be applied immediately.
// Includes: future blocks, blocks with unverified signatures (sequencer rotation),
// and recent past blocks (for potential reorg).
type PendingBlockCache struct {
	blocks   map[common.Hash]*BlockV2   // hash -> block
	byParent map[common.Hash][]*BlockV2 // parentHash -> children

	mtx sync.RWMutex
}

// NewPendingBlockCache creates a new cache.
func NewPendingBlockCache() *PendingBlockCache {
	return &PendingBlockCache{
		blocks:   make(map[common.Hash]*BlockV2),
		byParent: make(map[common.Hash][]*BlockV2),
	}
}

// Add adds a block. Returns false if rejected.
func (c *PendingBlockCache) Add(block *BlockV2, localHeight uint64) bool {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	// Reject out of range (allow some behind for reorg)
	minHeight := localHeight - MaxPendingHeightBehind
	if localHeight < MaxPendingHeightBehind {
		minHeight = 0
	}
	maxHeight := localHeight + MaxPendingHeightAhead
	if block.Number < minHeight || block.Number > maxHeight {
		return false
	}

	// Reject if full
	if len(c.blocks) >= MaxPendingBlocks {
		return false
	}

	// Check duplicate
	if _, exists := c.blocks[block.Hash]; exists {
		return false
	}

	// Add
	c.blocks[block.Hash] = block
	c.byParent[block.ParentHash] = append(c.byParent[block.ParentHash], block)
	return true
}

// GetChildren returns direct children of given parent.
func (c *PendingBlockCache) GetChildren(parentHash common.Hash) []*BlockV2 {
	c.mtx.RLock()
	defer c.mtx.RUnlock()
	result := make([]*BlockV2, len(c.byParent[parentHash]))
	copy(result, c.byParent[parentHash])
	return result
}

// GetLongestChain builds and returns the longest chain starting from parentHash.
// Returns blocks in order (first is direct child of parent).
func (c *PendingBlockCache) GetLongestChain(parentHash common.Hash) []*BlockV2 {
	c.mtx.RLock()
	defer c.mtx.RUnlock()
	return c.buildLongestChain(parentHash)
}

// buildLongestChain recursively builds the longest chain (internal, no lock).
func (c *PendingBlockCache) buildLongestChain(parentHash common.Hash) []*BlockV2 {
	children := c.byParent[parentHash]
	if len(children) == 0 {
		return nil
	}

	var longestChain []*BlockV2
	for _, child := range children {
		chain := append([]*BlockV2{child}, c.buildLongestChain(child.Hash)...)
		if len(chain) > len(longestChain) {
			longestChain = chain
		}
	}
	return longestChain
}

// Get returns a block by hash.
func (c *PendingBlockCache) Get(hash common.Hash) *BlockV2 {
	c.mtx.RLock()
	defer c.mtx.RUnlock()
	return c.blocks[hash]
}

// PruneBelow removes blocks at or below the given height.
func (c *PendingBlockCache) PruneBelow(height uint64) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	for hash, block := range c.blocks {
		if block.Number <= height {
			// Remove from parent index
			c.removeFromParent(block)
			delete(c.blocks, hash)
		}
	}
}

// removeFromParent removes block from parent index (internal, no lock).
func (c *PendingBlockCache) removeFromParent(block *BlockV2) {
	children := c.byParent[block.ParentHash]
	for i, child := range children {
		if child.Hash == block.Hash {
			c.byParent[block.ParentHash] = append(children[:i], children[i+1:]...)
			break
		}
	}
	if len(c.byParent[block.ParentHash]) == 0 {
		delete(c.byParent, block.ParentHash)
	}
}

// Size returns total block count.
func (c *PendingBlockCache) Size() int {
	c.mtx.RLock()
	defer c.mtx.RUnlock()
	return len(c.blocks)
}
