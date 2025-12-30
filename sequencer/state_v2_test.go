package sequencer

import (
	"crypto/ecdsa"
	"testing"
	"time"

	"github.com/morph-l2/go-ethereum/crypto"
	"github.com/tendermint/tendermint/l2node"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/types"
)

// newTestMockL2Node creates a mock L2Node for testing
func newTestMockL2Node() l2node.L2Node {
	return l2node.NewMockL2Node(0, "")
}

func TestStateV2_NewStateV2(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()

	stateV2, err := NewStateV2(mockL2Node, nil, time.Second, logger)
	if err != nil {
		t.Fatalf("NewStateV2 failed: %v", err)
	}

	if stateV2 == nil {
		t.Fatal("StateV2 should not be nil")
	}
}

func TestStateV2_LatestHeight(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()

	stateV2, err := NewStateV2(mockL2Node, nil, time.Second, logger)
	if err != nil {
		t.Fatalf("NewStateV2 failed: %v", err)
	}

	// Before start, latestBlock should be nil
	height := stateV2.LatestHeight()
	if height != 0 {
		t.Errorf("LatestHeight before start = %d, want 0", height)
	}
}

func TestStateV2_IsSequencerNode(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()

	// Generate a test private key
	privKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}

	stateV2, err := NewStateV2(mockL2Node, privKey, time.Second, logger)
	if err != nil {
		t.Fatalf("NewStateV2 failed: %v", err)
	}

	// Before start, isSequencer is not set
	if stateV2.IsSequencerNode() {
		t.Error("IsSequencerNode should be false before start")
	}
}

func TestStateV2_SignBlock(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()

	// Generate a test private key
	privKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}

	stateV2, err := NewStateV2(mockL2Node, privKey, time.Second, logger)
	if err != nil {
		t.Fatalf("NewStateV2 failed: %v", err)
	}

	// Create a test block
	block := &types.BlockV2{
		Number: 1,
		Hash:   [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32},
	}

	// Sign the block
	err = stateV2.signBlock(block)
	if err != nil {
		t.Fatalf("signBlock failed: %v", err)
	}

	if len(block.Signature) == 0 {
		t.Error("Block signature should not be empty")
	}

	// Verify signature length (Ethereum signatures are 65 bytes)
	if len(block.Signature) != 65 {
		t.Errorf("Signature length = %d, want 65", len(block.Signature))
	}
}

func TestStateV2_SignBlockWithoutKey(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()

	// Create StateV2 without private key
	stateV2, err := NewStateV2(mockL2Node, nil, time.Second, logger)
	if err != nil {
		t.Fatalf("NewStateV2 failed: %v", err)
	}

	block := &types.BlockV2{
		Number: 1,
		Hash:   [32]byte{1, 2, 3, 4},
	}

	// Sign should fail without private key
	err = stateV2.signBlock(block)
	if err == nil {
		t.Error("signBlock should fail without private key")
	}
}

// TestStateV2_UpdateLatestBlock and TestStateV2_UpdateLatestBlock_NonContinuous
// were removed because UpdateLatestBlock was merged into ApplyBlock

// Helper to create a test StateV2 with a running state
func createTestStateV2(t *testing.T, privKey *ecdsa.PrivateKey) *StateV2 {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()

	stateV2, err := NewStateV2(mockL2Node, privKey, time.Second, logger)
	if err != nil {
		t.Fatalf("NewStateV2 failed: %v", err)
	}

	return stateV2
}
