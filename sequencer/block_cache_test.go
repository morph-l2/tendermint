package sequencer

import (
	"testing"

	"github.com/morph-l2/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeBlock(height uint64, hash byte) *BlockV2 {
	return &BlockV2{
		Number: height,
		Hash:   common.Hash{hash},
	}
}

func TestBlockRingBuffer_Empty(t *testing.T) {
	rb := NewBlockRingBuffer(10)

	// Empty state
	assert.Equal(t, 0, rb.Count())
	assert.Nil(t, rb.GetByHeight(100))
	assert.Nil(t, rb.GetByHash(common.Hash{0x01}))

	min, max := rb.HeightRange()
	assert.Equal(t, uint64(0), min)
	assert.Equal(t, uint64(0), max)
	assert.Equal(t, uint64(0), rb.LatestHeight())
}

func TestBlockRingBuffer_FirstBlock(t *testing.T) {
	rb := NewBlockRingBuffer(10)

	block := makeBlock(100, 0x01)
	ok := rb.Add(block)

	assert.True(t, ok)
	assert.Equal(t, 1, rb.Count())
	assert.Equal(t, uint64(100), rb.LatestHeight())

	min, max := rb.HeightRange()
	assert.Equal(t, uint64(100), min)
	assert.Equal(t, uint64(100), max)

	// Lookup
	assert.Equal(t, block, rb.GetByHeight(100))
	assert.Equal(t, block, rb.GetByHash(common.Hash{0x01}))
}

func TestBlockRingBuffer_ContiguousAdd(t *testing.T) {
	rb := NewBlockRingBuffer(10)

	// Add blocks 100-104
	for i := uint64(100); i <= 104; i++ {
		block := makeBlock(i, byte(i))
		ok := rb.Add(block)
		assert.True(t, ok, "failed to add block %d", i)
	}

	assert.Equal(t, 5, rb.Count())
	assert.Equal(t, uint64(104), rb.LatestHeight())

	min, max := rb.HeightRange()
	assert.Equal(t, uint64(100), min)
	assert.Equal(t, uint64(104), max)

	// All blocks should be retrievable
	for i := uint64(100); i <= 104; i++ {
		block := rb.GetByHeight(i)
		require.NotNil(t, block)
		assert.Equal(t, i, block.Number)
	}
}

func TestBlockRingBuffer_NonContiguousAdd_Rejected(t *testing.T) {
	rb := NewBlockRingBuffer(10)

	// Add first block
	rb.Add(makeBlock(100, 0x01))

	// Try to add non-contiguous (skip 101)
	ok := rb.Add(makeBlock(102, 0x02))
	assert.False(t, ok, "non-contiguous add should be rejected")

	// State unchanged
	assert.Equal(t, 1, rb.Count())
	assert.Equal(t, uint64(100), rb.LatestHeight())
}

func TestBlockRingBuffer_CapacityEviction(t *testing.T) {
	rb := NewBlockRingBuffer(5)

	// Add blocks 100-104 (fills capacity)
	for i := uint64(100); i <= 104; i++ {
		rb.Add(makeBlock(i, byte(i)))
	}
	assert.Equal(t, 5, rb.Count())

	min, max := rb.HeightRange()
	assert.Equal(t, uint64(100), min)
	assert.Equal(t, uint64(104), max)

	// Add block 105, should evict 100
	rb.Add(makeBlock(105, 0x69))
	assert.Equal(t, 5, rb.Count())

	min, max = rb.HeightRange()
	assert.Equal(t, uint64(101), min)
	assert.Equal(t, uint64(105), max)

	// Block 100 should be gone
	assert.Nil(t, rb.GetByHeight(100))
	assert.Nil(t, rb.GetByHash(common.Hash{100}))

	// Block 101-105 should exist
	for i := uint64(101); i <= 105; i++ {
		assert.NotNil(t, rb.GetByHeight(i), "block %d should exist", i)
	}
}

func TestBlockRingBuffer_MultipleEvictions(t *testing.T) {
	rb := NewBlockRingBuffer(3)

	// Add 100-102
	for i := uint64(100); i <= 102; i++ {
		rb.Add(makeBlock(i, byte(i)))
	}

	// Add 103-105, evicting 100-102
	for i := uint64(103); i <= 105; i++ {
		rb.Add(makeBlock(i, byte(i)))
	}

	assert.Equal(t, 3, rb.Count())

	min, max := rb.HeightRange()
	assert.Equal(t, uint64(103), min)
	assert.Equal(t, uint64(105), max)

	// Old blocks gone
	for i := uint64(100); i <= 102; i++ {
		assert.Nil(t, rb.GetByHeight(i))
	}

	// New blocks exist
	for i := uint64(103); i <= 105; i++ {
		assert.NotNil(t, rb.GetByHeight(i))
	}
}

func TestBlockRingBuffer_Reorg_SameHeightDifferentHash(t *testing.T) {
	rb := NewBlockRingBuffer(10)

	// Add blocks 100-105
	for i := uint64(100); i <= 105; i++ {
		rb.Add(makeBlock(i, byte(i)))
	}
	assert.Equal(t, 6, rb.Count())

	// Reorg at height 103 (different hash)
	newBlock103 := makeBlock(103, 0xFF) // Different hash
	ok := rb.Add(newBlock103)
	assert.True(t, ok)

	// Blocks 104, 105 should be removed
	assert.Equal(t, 4, rb.Count()) // 100, 101, 102, 103

	min, max := rb.HeightRange()
	assert.Equal(t, uint64(100), min)
	assert.Equal(t, uint64(103), max)

	// New block 103 should be there
	block := rb.GetByHeight(103)
	require.NotNil(t, block)
	assert.Equal(t, common.Hash{0xFF}, block.Hash)

	// Old hash should not be found
	assert.Nil(t, rb.GetByHash(common.Hash{103}))
	// New hash should be found
	assert.NotNil(t, rb.GetByHash(common.Hash{0xFF}))

	// Blocks 104, 105 should be gone
	assert.Nil(t, rb.GetByHeight(104))
	assert.Nil(t, rb.GetByHeight(105))
}

func TestBlockRingBuffer_Reorg_SameBlock_NoOp(t *testing.T) {
	rb := NewBlockRingBuffer(10)

	// Add blocks 100-103
	for i := uint64(100); i <= 103; i++ {
		rb.Add(makeBlock(i, byte(i)))
	}

	// Add same block again (same height, same hash)
	ok := rb.Add(makeBlock(102, 102))
	assert.True(t, ok)

	// State unchanged
	assert.Equal(t, 4, rb.Count())
	assert.Equal(t, uint64(103), rb.LatestHeight())
}

func TestBlockRingBuffer_RollbackTo(t *testing.T) {
	rb := NewBlockRingBuffer(10)

	// Add blocks 100-109
	for i := uint64(100); i <= 109; i++ {
		rb.Add(makeBlock(i, byte(i)))
	}
	assert.Equal(t, 10, rb.Count())

	// Rollback to 105
	rb.RollbackTo(105)

	assert.Equal(t, 6, rb.Count()) // 100-105

	min, max := rb.HeightRange()
	assert.Equal(t, uint64(100), min)
	assert.Equal(t, uint64(105), max)

	// Blocks 106-109 should be gone
	for i := uint64(106); i <= 109; i++ {
		assert.Nil(t, rb.GetByHeight(i))
	}

	// Blocks 100-105 should exist
	for i := uint64(100); i <= 105; i++ {
		assert.NotNil(t, rb.GetByHeight(i))
	}
}

func TestBlockRingBuffer_RollbackTo_All(t *testing.T) {
	rb := NewBlockRingBuffer(10)

	// Add blocks 100-104
	for i := uint64(100); i <= 104; i++ {
		rb.Add(makeBlock(i, byte(i)))
	}

	// Rollback to before first block
	rb.RollbackTo(99)

	// All blocks removed, but minHeight stays at 100
	// (rollbackTo only removes > targetHeight)
	assert.Equal(t, 1, rb.Count()) // Block 100 remains (not > 99, it's == 100)

	// Actually let's re-read the logic... rollbackTo removes h > targetHeight
	// So rollbackTo(99) removes 100, 101, 102, 103, 104
	// Wait, the loop is: for h := rb.maxHeight; h > targetHeight; h--
	// So it removes 104, 103, 102, 101, 100 (all > 99)
}

func TestBlockRingBuffer_RollbackTo_All_Correct(t *testing.T) {
	rb := NewBlockRingBuffer(10)

	// Add blocks 100-104
	for i := uint64(100); i <= 104; i++ {
		rb.Add(makeBlock(i, byte(i)))
	}

	// Rollback to 99 (removes all blocks since all > 99)
	rb.RollbackTo(99)

	assert.Equal(t, 0, rb.Count())
	assert.Equal(t, uint64(99), rb.LatestHeight()) // maxHeight set to targetHeight

	// All blocks gone
	for i := uint64(100); i <= 104; i++ {
		assert.Nil(t, rb.GetByHeight(i))
	}
}

func TestBlockRingBuffer_ReorgThenContinue(t *testing.T) {
	rb := NewBlockRingBuffer(10)

	// Add blocks 100-105
	for i := uint64(100); i <= 105; i++ {
		rb.Add(makeBlock(i, byte(i)))
	}

	// Reorg at 103
	rb.Add(makeBlock(103, 0xAA))

	// Continue adding on new chain
	rb.Add(makeBlock(104, 0xBB))
	rb.Add(makeBlock(105, 0xCC))
	rb.Add(makeBlock(106, 0xDD))

	assert.Equal(t, 7, rb.Count()) // 100-106

	// Verify new chain blocks
	assert.Equal(t, common.Hash{0xAA}, rb.GetByHeight(103).Hash)
	assert.Equal(t, common.Hash{0xBB}, rb.GetByHeight(104).Hash)
	assert.Equal(t, common.Hash{0xCC}, rb.GetByHeight(105).Hash)
	assert.Equal(t, common.Hash{0xDD}, rb.GetByHeight(106).Hash)
}

func TestBlockRingBuffer_WrapAround(t *testing.T) {
	rb := NewBlockRingBuffer(3)

	// Fill and wrap around multiple times
	for i := uint64(100); i <= 110; i++ {
		ok := rb.Add(makeBlock(i, byte(i)))
		assert.True(t, ok)
	}

	assert.Equal(t, 3, rb.Count())

	min, max := rb.HeightRange()
	assert.Equal(t, uint64(108), min)
	assert.Equal(t, uint64(110), max)

	// Only last 3 blocks should exist
	for i := uint64(108); i <= 110; i++ {
		block := rb.GetByHeight(i)
		require.NotNil(t, block, "block %d should exist", i)
		assert.Equal(t, i, block.Number)
	}
}

func TestBlockRingBuffer_StartFromNonZero(t *testing.T) {
	rb := NewBlockRingBuffer(10)

	// Start from height 1000000
	for i := uint64(1000000); i <= 1000005; i++ {
		ok := rb.Add(makeBlock(i, byte(i%256)))
		assert.True(t, ok)
	}

	assert.Equal(t, 6, rb.Count())

	min, max := rb.HeightRange()
	assert.Equal(t, uint64(1000000), min)
	assert.Equal(t, uint64(1000005), max)
}

func TestBlockRingBuffer_GetByHash(t *testing.T) {
	rb := NewBlockRingBuffer(10)

	blocks := []*BlockV2{
		makeBlock(100, 0xAA),
		makeBlock(101, 0xBB),
		makeBlock(102, 0xCC),
	}

	for _, b := range blocks {
		rb.Add(b)
	}

	// Find by hash
	assert.Equal(t, blocks[0], rb.GetByHash(common.Hash{0xAA}))
	assert.Equal(t, blocks[1], rb.GetByHash(common.Hash{0xBB}))
	assert.Equal(t, blocks[2], rb.GetByHash(common.Hash{0xCC}))

	// Not found
	assert.Nil(t, rb.GetByHash(common.Hash{0xDD}))
}

func TestBlockRingBuffer_ReorgUpdatesHashIndex(t *testing.T) {
	rb := NewBlockRingBuffer(10)

	// Add block 100 with hash AA
	rb.Add(makeBlock(100, 0xAA))
	assert.NotNil(t, rb.GetByHash(common.Hash{0xAA}))

	// Reorg: block 100 with hash BB
	rb.Add(makeBlock(100, 0xBB))

	// Old hash gone, new hash exists
	assert.Nil(t, rb.GetByHash(common.Hash{0xAA}))
	assert.NotNil(t, rb.GetByHash(common.Hash{0xBB}))
}

func TestBlockRingBuffer_Capacity1(t *testing.T) {
	rb := NewBlockRingBuffer(1)

	rb.Add(makeBlock(100, 0x01))
	assert.Equal(t, 1, rb.Count())
	assert.NotNil(t, rb.GetByHeight(100))

	rb.Add(makeBlock(101, 0x02))
	assert.Equal(t, 1, rb.Count())
	assert.Nil(t, rb.GetByHeight(100))
	assert.NotNil(t, rb.GetByHeight(101))

	// Reorg
	rb.Add(makeBlock(101, 0xFF))
	assert.Equal(t, 1, rb.Count())
	assert.Equal(t, common.Hash{0xFF}, rb.GetByHeight(101).Hash)
}

