package sequencer

import (
	"errors"
	"testing"

	"github.com/morph-l2/go-ethereum/common"
	"github.com/tendermint/tendermint/l2node"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/types"
)

// ============================================================================
// Mock implementations
// ============================================================================

// mockSignerImpl is a mock implementation of Signer for testing.
type mockSignerImpl struct {
	address   common.Address
	signature []byte
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

// mockSequencerVerifier is a mock implementation of SequencerVerifier for testing.
type mockSequencerVerifier struct {
	isSequencer bool
	err         error
}

func (m *mockSequencerVerifier) IsSequencerAt(addr common.Address, l2Height uint64) (bool, error) {
	if m.err != nil {
		return false, m.err
	}
	return m.isSequencer, nil
}

// mockSequencerHA is a mock implementation of SequencerHA for testing.
type mockSequencerHA struct {
	leader    bool
	commitErr error
	subCh     chan *BlockV2
}

func newMockSequencerHA(leader bool) *mockSequencerHA {
	return &mockSequencerHA{
		leader: leader,
		subCh:  make(chan *BlockV2, 10),
	}
}

func (m *mockSequencerHA) Start() error                              { return nil }
func (m *mockSequencerHA) Stop()                                     {}
func (m *mockSequencerHA) IsLeader() bool                            { return m.leader }
func (m *mockSequencerHA) Join() error                               { return nil }
func (m *mockSequencerHA) Commit(block *BlockV2) error               { return m.commitErr }
func (m *mockSequencerHA) Subscribe() <-chan *BlockV2                { return m.subCh }
func (m *mockSequencerHA) SetOnBlockApplied(fn func(*BlockV2) error) {}

// newTestMockL2Node creates a mock L2Node for testing.
func newTestMockL2Node() l2node.L2Node {
	return l2node.NewMockL2Node(0, "")
}

// ============================================================================
// Existing tests (adapted for new signature)
// ============================================================================

func TestStateV2_NewStateV2(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()

	stateV2, err := NewStateV2(mockL2Node, logger, &mockSequencerVerifier{}, nil, nil, nil, nil)
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

	stateV2, err := NewStateV2(mockL2Node, logger, &mockSequencerVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewStateV2 failed: %v", err)
	}

	height := stateV2.LatestHeight()
	if height != 0 {
		t.Errorf("LatestHeight before start = %d, want 0", height)
	}
}

func TestStateV2_HasSigner_MatchesIsSequencerMode(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()
	mockVerifier := &mockSequencerVerifier{}

	// Without signer
	s1, _ := NewStateV2(mockL2Node, logger, mockVerifier, nil, nil, nil, nil)
	if s1.HasSigner() {
		t.Error("should be false when signer is nil")
	}

	// With signer
	s2, _ := NewStateV2(mockL2Node, logger, mockVerifier, nil, &mockSignerImpl{}, nil, nil)
	if !s2.HasSigner() {
		t.Error("should be true when signer is provided")
	}
}

func TestStateV2_SignBlock(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()

	mockSigner := &mockSignerImpl{signature: make([]byte, 65)}
	mockVerifier := &mockSequencerVerifier{}

	stateV2, err := NewStateV2(mockL2Node, logger, mockVerifier, &mockL1Tracker{halt: false}, mockSigner, nil, nil)
	if err != nil {
		t.Fatalf("NewStateV2 failed: %v", err)
	}

	block := &types.BlockV2{
		Number: 1,
		Hash:   [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32},
	}

	if err := stateV2.signBlock(block); err != nil {
		t.Fatalf("signBlock failed: %v", err)
	}
	if len(block.Signature) != 65 {
		t.Errorf("Signature length = %d, want 65", len(block.Signature))
	}
}

func TestStateV2_SignBlockWithoutSigner(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()

	stateV2, err := NewStateV2(mockL2Node, logger, &mockSequencerVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewStateV2 failed: %v", err)
	}

	block := &types.BlockV2{Number: 1, Hash: [32]byte{1, 2, 3, 4}}
	if err := stateV2.signBlock(block); err == nil {
		t.Error("signBlock should fail without signer")
	}
}

// ============================================================================
// New tests: verifier requirement
// ============================================================================

func TestNewStateV2_VerifierRequired(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()

	// verifier==nil should fail
	_, err := NewStateV2(mockL2Node, logger, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error when verifier is nil")
	}

	// with signer, still fails without verifier
	mockSigner := &mockSignerImpl{}
	_, err = NewStateV2(mockL2Node, logger, nil, nil, mockSigner, nil, nil)
	if err == nil {
		t.Fatal("expected error when verifier is nil (with signer)")
	}
}

func TestNewStateV2_WithHA(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()
	mockSigner := &mockSignerImpl{}
	mockVerifier := &mockSequencerVerifier{}
	ha := newMockSequencerHA(true)

	stateV2, err := NewStateV2(mockL2Node, logger, mockVerifier, &mockL1Tracker{halt: false}, mockSigner, nil, ha)
	if err != nil {
		t.Fatalf("NewStateV2 failed: %v", err)
	}
	if !stateV2.IsHAMode() {
		t.Error("IsHAMode should be true when ha is provided")
	}
}

// ============================================================================
// New tests: node mode helpers
// ============================================================================

func TestStateV2_HasSigner(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()

	fullnode, _ := NewStateV2(mockL2Node, logger, &mockSequencerVerifier{}, nil, nil, nil, nil)
	if fullnode.HasSigner() {
		t.Error("Fullnode should not have signer")
	}

	mockSigner := &mockSignerImpl{}
	mockVerifier := &mockSequencerVerifier{}
	seqNode, _ := NewStateV2(mockL2Node, logger, mockVerifier, &mockL1Tracker{halt: false}, mockSigner, nil, nil)
	if !seqNode.HasSigner() {
		t.Error("Sequencer node should have signer")
	}
}

func TestStateV2_IsHAMode(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()
	mockSigner := &mockSignerImpl{}
	mockVerifier := &mockSequencerVerifier{}

	// Non-HA
	nonHA, _ := NewStateV2(mockL2Node, logger, mockVerifier, &mockL1Tracker{halt: false}, mockSigner, nil, nil)
	if nonHA.IsHAMode() {
		t.Error("IsHAMode should be false without ha")
	}

	// HA
	ha, _ := NewStateV2(mockL2Node, logger, mockVerifier, &mockL1Tracker{halt: false}, mockSigner, nil, newMockSequencerHA(false))
	if !ha.IsHAMode() {
		t.Error("IsHAMode should be true with ha")
	}
}

func TestStateV2_IsHALeader(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()
	mockSigner := &mockSignerImpl{}
	mockVerifier := &mockSequencerVerifier{}

	// Non-HA: never leader
	nonHA, _ := NewStateV2(mockL2Node, logger, mockVerifier, &mockL1Tracker{halt: false}, mockSigner, nil, nil)
	if nonHA.IsHALeader() {
		t.Error("non-HA node should not be HA leader")
	}

	// HA follower
	follower, _ := NewStateV2(mockL2Node, logger, mockVerifier, &mockL1Tracker{halt: false}, mockSigner, nil, newMockSequencerHA(false))
	if follower.IsHALeader() {
		t.Error("HA follower should not be leader")
	}

	// HA leader
	leader, _ := NewStateV2(mockL2Node, logger, mockVerifier, &mockL1Tracker{halt: false}, mockSigner, nil, newMockSequencerHA(true))
	if !leader.IsHALeader() {
		t.Error("HA leader should be leader")
	}
}

// ============================================================================
// New tests: isActiveSequencer
// ============================================================================

func TestStateV2_IsActiveSequencer_NonHA_Active(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()
	mockSigner := &mockSignerImpl{address: common.HexToAddress("0x1")}
	mockVerifier := &mockSequencerVerifier{isSequencer: true}

	s, _ := NewStateV2(mockL2Node, logger, mockVerifier, &mockL1Tracker{halt: false}, mockSigner, nil, nil)
	s.latestBlock = &BlockV2{Number: 0}

	if !s.isActiveSequencer() {
		t.Error("should be active sequencer when verifier returns true")
	}
}

func TestStateV2_IsActiveSequencer_NonHA_Inactive(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()
	mockSigner := &mockSignerImpl{address: common.HexToAddress("0x1")}
	mockVerifier := &mockSequencerVerifier{isSequencer: false}

	s, _ := NewStateV2(mockL2Node, logger, mockVerifier, &mockL1Tracker{halt: false}, mockSigner, nil, nil)
	s.latestBlock = &BlockV2{Number: 0}

	if s.isActiveSequencer() {
		t.Error("should not be active sequencer when verifier returns false")
	}
}

func TestStateV2_IsActiveSequencer_HA_Leader(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()
	mockSigner := &mockSignerImpl{address: common.HexToAddress("0x1")}
	mockVerifier := &mockSequencerVerifier{isSequencer: true}
	ha := newMockSequencerHA(true)

	s, _ := NewStateV2(mockL2Node, logger, mockVerifier, &mockL1Tracker{halt: false}, mockSigner, nil, ha)
	s.latestBlock = &BlockV2{Number: 0}

	if !s.isActiveSequencer() {
		t.Error("HA leader should be active sequencer")
	}
}

func TestStateV2_IsActiveSequencer_HA_Follower(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()
	mockSigner := &mockSignerImpl{address: common.HexToAddress("0x1")}
	mockVerifier := &mockSequencerVerifier{isSequencer: true} // L1 says active
	ha := newMockSequencerHA(false)                           // but not leader

	s, _ := NewStateV2(mockL2Node, logger, mockVerifier, &mockL1Tracker{halt: false}, mockSigner, nil, ha)
	s.latestBlock = &BlockV2{Number: 0}

	if s.isActiveSequencer() {
		t.Error("HA follower should not be active sequencer even if L1 says active")
	}
}

func TestStateV2_IsActiveSequencer_VerifierError(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()
	mockSigner := &mockSignerImpl{}
	mockVerifier := &mockSequencerVerifier{err: errors.New("rpc error")}

	s, _ := NewStateV2(mockL2Node, logger, mockVerifier, &mockL1Tracker{halt: false}, mockSigner, nil, nil)
	s.latestBlock = &BlockV2{Number: 0}

	if s.isActiveSequencer() {
		t.Error("should return false when verifier returns error")
	}
}

// ============================================================================
// New tests: ApplyBlock
// ============================================================================

func TestStateV2_ApplyBlock_Idempotent(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()

	s, _ := NewStateV2(mockL2Node, logger, &mockSequencerVerifier{}, nil, nil, nil, nil)
	block := &types.BlockV2{Number: 1, Signature: []byte{0x01, 0x02, 0x03}}

	// Apply twice should not error
	if err := s.ApplyBlock(block); err != nil {
		t.Fatalf("first apply failed: %v", err)
	}
	if err := s.ApplyBlock(block); err != nil {
		t.Fatalf("second apply (idempotent) failed: %v", err)
	}
	if s.LatestHeight() != 1 {
		t.Errorf("LatestHeight = %d, want 1", s.LatestHeight())
	}
}

func TestStateV2_ApplyBlock_OlderBlockSkipped(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()

	s, _ := NewStateV2(mockL2Node, logger, &mockSequencerVerifier{}, nil, nil, nil, nil)

	block2 := &types.BlockV2{Number: 2, Signature: []byte{0x01, 0x02, 0x03}}
	block1 := &types.BlockV2{Number: 1, Signature: []byte{0x01, 0x02, 0x03}}

	if err := s.ApplyBlock(block2); err != nil {
		t.Fatalf("apply block2 failed: %v", err)
	}
	// Apply older block should be skipped silently
	if err := s.ApplyBlock(block1); err != nil {
		t.Fatalf("apply older block should not error: %v", err)
	}
	// latestBlock should still be 2
	if s.LatestHeight() != 2 {
		t.Errorf("LatestHeight = %d, want 2", s.LatestHeight())
	}
}

func TestStateV2_ApplyBlock_Sequential(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()

	s, _ := NewStateV2(mockL2Node, logger, &mockSequencerVerifier{}, nil, nil, nil, nil)

	for i := uint64(1); i <= 5; i++ {
		block := &types.BlockV2{Number: i, Signature: []byte{0x01, 0x02, 0x03}}
		if err := s.ApplyBlock(block); err != nil {
			t.Fatalf("apply block %d failed: %v", i, err)
		}
	}
	if s.LatestHeight() != 5 {
		t.Errorf("LatestHeight = %d, want 5", s.LatestHeight())
	}
}

// mockL1Tracker is a controllable L1Tracker for tests.
type mockL1Tracker struct {
	halt bool
}

func (m *mockL1Tracker) IsHalt() bool { return m.halt }

func TestIsActiveSequencer_L1TrackerHaltsProduction(t *testing.T) {
	logger := log.NewNopLogger()
	verifier := &mockSequencerVerifier{isSequencer: true}
	signer := &mockSignerImpl{}

	// L1 healthy -> active (verifier says we are the sequencer).
	s, err := NewStateV2(newTestMockL2Node(), logger, verifier, &mockL1Tracker{halt: false}, signer, nil, nil)
	if err != nil {
		t.Fatalf("NewStateV2: %v", err)
	}
	s.latestBlock = &BlockV2{Number: 10}
	if !s.isActiveSequencer() {
		t.Fatal("expected active when L1 healthy and verifier says sequencer")
	}

	// L1 halted -> NOT active even though verifier says we are the sequencer.
	s2, err := NewStateV2(newTestMockL2Node(), logger, verifier, &mockL1Tracker{halt: true}, signer, nil, nil)
	if err != nil {
		t.Fatalf("NewStateV2: %v", err)
	}
	s2.latestBlock = &BlockV2{Number: 10}
	if s2.isActiveSequencer() {
		t.Fatal("expected NOT active when L1 tracker halts production")
	}
}
