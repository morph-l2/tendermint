package sequencer

import (
	"context"
	"testing"
	"time"

	"github.com/morph-l2/go-ethereum/common"
	"github.com/tendermint/tendermint/l2node"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/types"
)

// mockSignerImpl is a mock implementation of Signer for testing
type mockSignerImpl struct {
	address   common.Address
	signature []byte
	isActive  bool
}

func (m *mockSignerImpl) Sign(data []byte) ([]byte, error) {
	if m.signature != nil {
		return m.signature, nil
	}
	return make([]byte, 65), nil
}

func (m *mockSignerImpl) Address() common.Address {
	return m.address
}

func (m *mockSignerImpl) IsActiveSequencer(ctx context.Context) (bool, error) {
	return m.isActive, nil
}

// newTestMockL2Node creates a mock L2Node for testing
func newTestMockL2Node() l2node.L2Node {
	return l2node.NewMockL2Node(0, "")
}

func TestStateV2_NewStateV2(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()

	stateV2, err := NewStateV2(mockL2Node, time.Second, logger, nil, nil)
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

	stateV2, err := NewStateV2(mockL2Node, time.Second, logger, nil, nil)
	if err != nil {
		t.Fatalf("NewStateV2 failed: %v", err)
	}

	// Before start, latestBlock should be nil
	height := stateV2.LatestHeight()
	if height != 0 {
		t.Errorf("LatestHeight before start = %d, want 0", height)
	}
}

func TestStateV2_IsSequencerMode(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()

	// Without signer, sequencerMode should be false
	stateV2, err := NewStateV2(mockL2Node, time.Second, logger, nil, nil)
	if err != nil {
		t.Fatalf("NewStateV2 failed: %v", err)
	}

	if stateV2.IsSequencerMode() {
		t.Error("IsSequencerMode should be false when signer is nil")
	}

	// With mock signer, sequencerMode should be true
	mockSigner := &mockSignerImpl{}
	stateV2WithSigner, err := NewStateV2(mockL2Node, time.Second, logger, nil, mockSigner)
	if err != nil {
		t.Fatalf("NewStateV2 failed: %v", err)
	}

	if !stateV2WithSigner.IsSequencerMode() {
		t.Error("IsSequencerMode should be true when signer is provided")
	}
}

func TestStateV2_SignBlock(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()

	// Create mock signer
	mockSigner := &mockSignerImpl{
		signature: make([]byte, 65), // Mock 65-byte signature
	}

	stateV2, err := NewStateV2(mockL2Node, time.Second, logger, nil, mockSigner)
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

func TestStateV2_SignBlockWithoutSigner(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()

	// Create StateV2 without signer
	stateV2, err := NewStateV2(mockL2Node, time.Second, logger, nil, nil)
	if err != nil {
		t.Fatalf("NewStateV2 failed: %v", err)
	}

	block := &types.BlockV2{
		Number: 1,
		Hash:   [32]byte{1, 2, 3, 4},
	}

	// Sign should fail without signer
	err = stateV2.signBlock(block)
	if err == nil {
		t.Error("signBlock should fail without signer")
	}
}

// Helper to create a test StateV2
func createTestStateV2(t *testing.T, signer Signer) *StateV2 {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()

	stateV2, err := NewStateV2(mockL2Node, time.Second, logger, nil, signer)
	if err != nil {
		t.Fatalf("NewStateV2 failed: %v", err)
	}

	return stateV2
}
