package sequencer

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/go-kit/kit/metrics"
	"github.com/morph-l2/go-ethereum/common"
	"github.com/tendermint/tendermint/l2node"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/types"
)

// ============================================================================
// Mock implementations
// ============================================================================

// mockSignerImpl is a mock implementation of Signer for testing.
// Single-key by default (address); set addrs for a multi-key signer.
type mockSignerImpl struct {
	address   common.Address
	addrs     []common.Address
	signature []byte
	signedBy  []common.Address // addresses SignWith was called with, in order
}

func (m *mockSignerImpl) Addresses() []common.Address {
	if m.addrs != nil {
		return m.addrs
	}
	return []common.Address{m.address}
}

func (m *mockSignerImpl) HasAddress(addr common.Address) bool {
	for _, a := range m.Addresses() {
		if a == addr {
			return true
		}
	}
	return false
}

func (m *mockSignerImpl) SignWith(addr common.Address, data []byte) ([]byte, error) {
	if !m.HasAddress(addr) {
		return nil, fmt.Errorf("unknown signer address %s", addr.Hex())
	}
	m.signedBy = append(m.signedBy, addr)
	if m.signature != nil {
		return m.signature, nil
	}
	return make([]byte, 65), nil
}

// isActive is the bool half of isActiveSequencer, for terse assertions.
func isActive(s *StateV2) bool {
	_, ok := s.isActiveSequencer()
	return ok
}

// mockSequencerVerifier is a mock implementation of SequencerVerifier for testing.
//
// If designated is set, it decides per height (height -> sequencer address).
// Otherwise it gives a blanket answer driven by isSequencer:
//   - IsSequencerAt (block verification) returns isSequencer for any address;
//   - SequencerAtHeight (block production) returns sequencer when isSequencer
//     is true — set it to the signer's address — and an address nobody holds
//     when it is false.
type mockSequencerVerifier struct {
	isSequencer bool
	sequencer   common.Address
	err         error
	designated  func(height uint64) common.Address
}

// mockOtherSequencer is the designated address when the blanket answer is
// "not the sequencer": some other party no test signer holds a key for.
var mockOtherSequencer = common.HexToAddress("0x00000000000000000000000000000000000000ff")

func (m *mockSequencerVerifier) SequencerAtHeight(l2Height uint64) (common.Address, error) {
	if m.err != nil {
		return common.Address{}, m.err
	}
	if m.designated != nil {
		return m.designated(l2Height), nil
	}
	if !m.isSequencer {
		return mockOtherSequencer, nil
	}
	return m.sequencer, nil
}

func (m *mockSequencerVerifier) IsSequencerAt(addr common.Address, l2Height uint64) (bool, error) {
	if m.err != nil {
		return false, m.err
	}
	if m.designated != nil {
		return m.designated(l2Height) == addr, nil
	}
	return m.isSequencer, nil
}

type recordingGauge struct {
	value float64
}

func (g *recordingGauge) With(labelValues ...string) metrics.Gauge { return g }
func (g *recordingGauge) Set(value float64)                        { g.value = value }
func (g *recordingGauge) Add(delta float64)                        { g.value += delta }

// mockSequencerHA is a mock implementation of SequencerHA for testing.
type mockSequencerHA struct {
	leader    bool
	commitErr error
	subCh     chan *BlockV2
	handOffs  []common.Address // TransferLeaderToEligible calls, in order
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
func (m *mockSequencerHA) TransferLeader() error                     { return nil }
func (m *mockSequencerHA) TransferLeaderToEligible(addr common.Address) error {
	m.handOffs = append(m.handOffs, addr)
	return nil
}

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

	if err := stateV2.signBlock(block, mockSigner.address); err != nil {
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
	if err := stateV2.signBlock(block, common.Address{}); err == nil {
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
	mockVerifier := &mockSequencerVerifier{isSequencer: true, sequencer: mockSigner.address}

	s, _ := NewStateV2(mockL2Node, logger, mockVerifier, &mockL1Tracker{halt: false}, mockSigner, nil, nil)
	s.latestBlock = &BlockV2{Number: 0}

	if !isActive(s) {
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

	if isActive(s) {
		t.Error("should not be active sequencer when verifier returns false")
	}
}

func TestStateV2_IsActiveSequencer_HA_Leader(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()
	mockSigner := &mockSignerImpl{address: common.HexToAddress("0x1")}
	mockVerifier := &mockSequencerVerifier{isSequencer: true, sequencer: mockSigner.address}
	ha := newMockSequencerHA(true)

	s, _ := NewStateV2(mockL2Node, logger, mockVerifier, &mockL1Tracker{halt: false}, mockSigner, nil, ha)
	s.latestBlock = &BlockV2{Number: 0}

	if !isActive(s) {
		t.Error("HA leader should be active sequencer")
	}
}

func TestStateV2_IsActiveSequencer_HA_Follower(t *testing.T) {
	mockL2Node := newTestMockL2Node()
	logger := log.NewNopLogger()
	mockSigner := &mockSignerImpl{address: common.HexToAddress("0x1")}
	mockVerifier := &mockSequencerVerifier{isSequencer: true, sequencer: mockSigner.address} // L1 says active
	ha := newMockSequencerHA(false)                                                          // but not leader

	s, _ := NewStateV2(mockL2Node, logger, mockVerifier, &mockL1Tracker{halt: false}, mockSigner, nil, ha)
	s.latestBlock = &BlockV2{Number: 0}

	if isActive(s) {
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

	if isActive(s) {
		t.Error("should return false when verifier returns error")
	}
}

func TestStateV2_IsActiveSequencer_ReportsInactiveAfterBecomingInactive(t *testing.T) {
	logger := log.NewNopLogger()
	signer := &mockSignerImpl{address: common.HexToAddress("0x1")}
	verifier := &mockSequencerVerifier{isSequencer: true, sequencer: signer.address}
	l1Tracker := &mockL1Tracker{halt: false}
	ha := newMockSequencerHA(true)
	gauge := &recordingGauge{}

	s, err := NewStateV2(newTestMockL2Node(), logger, verifier, l1Tracker, signer, nil, ha)
	if err != nil {
		t.Fatalf("NewStateV2: %v", err)
	}
	metrics := NopMetrics()
	metrics.IsActiveSequencer = gauge
	s.SetMetrics(metrics)
	s.latestBlock = &BlockV2{Number: 10}

	if !isActive(s) {
		t.Fatal("expected active when HA leader, L1 healthy, and verifier says sequencer")
	}
	if gauge.value != 1 {
		t.Fatalf("active metric = %v, want 1", gauge.value)
	}

	l1Tracker.halt = true
	if isActive(s) {
		t.Fatal("expected inactive when L1 tracker halts production")
	}
	if gauge.value != 0 {
		t.Fatalf("active metric after L1 halt = %v, want 0", gauge.value)
	}

	l1Tracker.halt = false
	if !isActive(s) {
		t.Fatal("expected active again after L1 recovers")
	}
	if gauge.value != 1 {
		t.Fatalf("active metric after L1 recovery = %v, want 1", gauge.value)
	}

	ha.leader = false
	if isActive(s) {
		t.Fatal("expected inactive when HA node is no longer leader")
	}
	if gauge.value != 0 {
		t.Fatalf("active metric after losing HA leadership = %v, want 0", gauge.value)
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
	if !isActive(s) {
		t.Fatal("expected active when L1 healthy and verifier says sequencer")
	}

	// L1 halted -> NOT active even though verifier says we are the sequencer.
	s2, err := NewStateV2(newTestMockL2Node(), logger, verifier, &mockL1Tracker{halt: true}, signer, nil, nil)
	if err != nil {
		t.Fatalf("NewStateV2: %v", err)
	}
	s2.latestBlock = &BlockV2{Number: 10}
	if isActive(s2) {
		t.Fatal("expected NOT active when L1 tracker halts production")
	}
}

// ============================================================================
// Backfill cache lifecycle
// ============================================================================

// gatedL2Node lets a test park an ApplyBlockV2 call inside StateV2.ApplyBlock,
// so a stop/reset can be driven while an apply is still in flight. Calls made
// while no gate is armed pass straight through to the wrapped mock.
type gatedL2Node struct {
	l2node.L2Node

	mtx     sync.Mutex
	entered chan struct{}
	release chan struct{}
}

func newGatedL2Node() *gatedL2Node {
	return &gatedL2Node{L2Node: newTestMockL2Node()}
}

// gate arms the node: the next ApplyBlockV2 signals entered and then blocks
// until release is closed.
func (g *gatedL2Node) gate(entered, release chan struct{}) {
	g.mtx.Lock()
	defer g.mtx.Unlock()
	g.entered, g.release = entered, release
}

func (g *gatedL2Node) ApplyBlockV2(block *BlockV2) (bool, error) {
	g.mtx.Lock()
	entered, release := g.entered, g.release
	g.entered, g.release = nil, nil
	g.mtx.Unlock()

	if release != nil {
		close(entered)
		<-release
	}
	return g.L2Node.ApplyBlockV2(block)
}

func cachedTestBlock(number uint64, parent common.Hash) *BlockV2 {
	return &types.BlockV2{
		Number:     number,
		Hash:       common.Hash{byte(number)},
		ParentHash: parent,
		Signature:  []byte{0x01},
	}
}

// An apply that outlives a stop/reset must not leave anything behind for the
// next lifecycle: OnStart re-seeds latestBlock from the execution layer, which
// may come back on a different head, so a block cached before the transition
// could be off-chain and backfilling it would push a dead branch into the EL.
//
// The stop/reset happens under a derivation reorg (StopReactorsBeforeReorg ->
// deriveForce -> StartReactorsAfterReorg), which does not wait for an in-flight
// apply, so the late Add is reachable in production.
func TestStateV2_OnStart_DropsBlocksCachedAcrossRestart(t *testing.T) {
	l2 := newGatedL2Node()
	s, err := NewStateV2(l2, log.NewNopLogger(), &mockSequencerVerifier{}, &mockL1Tracker{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewStateV2: %v", err)
	}
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	b1 := cachedTestBlock(1, common.Hash{})
	b2 := cachedTestBlock(2, b1.Hash)
	for _, b := range []*BlockV2{b1, b2} {
		if err := s.ApplyBlock(b); err != nil {
			t.Fatalf("ApplyBlock(%d): %v", b.Number, err)
		}
	}
	if got := s.backfillCache.Count(); got != 2 {
		t.Fatalf("cached blocks before restart = %d, want 2", got)
	}

	// Park the apply of b3 inside the execution-layer call.
	entered, release := make(chan struct{}), make(chan struct{})
	l2.gate(entered, release)
	b3 := cachedTestBlock(3, b2.Hash)
	applyErr := make(chan error, 1)
	go func() { applyErr <- s.ApplyBlock(b3) }()
	<-entered

	// Stop and reset while that apply is still stuck in the EL.
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := s.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	close(release)
	if err := <-applyErr; err != nil {
		t.Fatalf("in-flight ApplyBlock: %v", err)
	}

	if err := s.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if got := s.backfillCache.Count(); got != 0 {
		t.Errorf("cached blocks after restart = %d, want 0", got)
	}
	if s.backfillCache.GetByHash(b3.Hash) != nil {
		t.Error("block cached by an apply that outlived the stop/reset is still reachable after restart")
	}
}

// ============================================================================
// Backfill depth
// ============================================================================

// headL2Node pins the execution-layer head. The shared mock hardcodes head 0,
// which cannot express "the EL lost its unpersisted blocks and came back behind".
type headL2Node struct {
	l2node.L2Node
	head *BlockV2
}

func (h *headL2Node) GetLatestBlockV2() (*BlockV2, error) { return h.head, nil }

// cacheGap fills the backfill cache with depth contiguous blocks directly above
// head and returns the hash of the newest one — the block an apply would have
// been looking for when its parent was reported missing.
func cacheGap(t *testing.T, s *StateV2, head *BlockV2, depth int) common.Hash {
	t.Helper()
	parent := head.Hash
	for i := 1; i <= depth; i++ {
		b := cachedTestBlock(head.Number+uint64(i), parent)
		if !s.backfillCache.Add(b) {
			t.Fatalf("cache add block %d", b.Number)
		}
		parent = b.Hash
	}
	return parent
}

// backfillMaxDepth has to cover how far the EL's canonical head can regress
// after a crash. That ceiling is upstream reth configuration rather than a
// fixed quantity — persistence_backpressure_threshold + memory_block_buffer_target
// is 16 on reth v2.4.0 and 21 on reth v2.5.2 — so the boundary is pinned here
// instead of being left to the constant's comment. A depth short of it refuses
// the gap outright, which is indistinguishable from a node that simply stopped
// catching up.
func TestStateV2_Backfill_DepthBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		depth   int
		wantErr string
	}{
		{"reth v2.5.2 default ceiling", 21, ""},
		{"exactly backfillMaxDepth", backfillMaxDepth, ""},
		{"one past backfillMaxDepth", backfillMaxDepth + 1, "gap exceeds backfillMaxDepth"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			head := cachedTestBlock(100, common.Hash{})
			l2 := &headL2Node{L2Node: newTestMockL2Node(), head: head}
			var logs bytes.Buffer
			s, err := NewStateV2(l2, log.NewTMLogger(&logs), &mockSequencerVerifier{}, &mockL1Tracker{}, nil, nil, nil)
			if err != nil {
				t.Fatalf("NewStateV2: %v", err)
			}

			tip := cacheGap(t, s, head, tc.depth)

			switch err = s.backfillMissingBlocks(tip); {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("depth %d: backfill refused, want success: %v", tc.depth, err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("depth %d: backfill succeeded, want refusal", tc.depth)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("depth %d: err = %v, want it to mention %q", tc.depth, err, tc.wantErr)
			}

			if tc.wantErr != "" {
				for _, want := range []string{
					fmt.Sprintf("oldestMissing=%d", head.Number+1),
					fmt.Sprintf("newestMissing=%d", head.Number+uint64(tc.depth)),
					fmt.Sprintf("gap=%d", tc.depth),
				} {
					if !strings.Contains(logs.String(), want) {
						t.Errorf("refusal log = %q, want field %q", logs.String(), want)
					}
				}
			}
		})
	}
}

// ============================================================================
// Multi-key signer: the node holds several keys and, per height, signs with
// the one the L1 contract designates — no leader change needed.
// ============================================================================

var (
	oldKey = common.HexToAddress("0x00000000000000000000000000000000000000a1")
	newKey = common.HexToAddress("0x00000000000000000000000000000000000000b2")
)

// rotateAt designates oldKey below height h and newKey from h on.
func rotateAt(h uint64) func(uint64) common.Address {
	return func(height uint64) common.Address {
		if height < h {
			return oldKey
		}
		return newKey
	}
}

func newMultiKeyState(t *testing.T, signer *mockSignerImpl, verifier *mockSequencerVerifier, ha SequencerHA) *StateV2 {
	t.Helper()
	s, err := NewStateV2(newTestMockL2Node(), log.NewNopLogger(), verifier, &mockL1Tracker{}, signer, nil, ha)
	if err != nil {
		t.Fatalf("NewStateV2: %v", err)
	}
	return s
}

func TestSigningAddrFor_SwitchesKeyAtRotationHeight(t *testing.T) {
	signer := &mockSignerImpl{addrs: []common.Address{oldKey, newKey}}
	s := newMultiKeyState(t, signer, &mockSequencerVerifier{designated: rotateAt(100)}, nil)

	for _, tc := range []struct {
		next uint64
		want common.Address
	}{{98, oldKey}, {99, oldKey}, {100, newKey}, {101, newKey}} {
		got, ok := s.signingAddrFor(tc.next)
		if !ok || got != tc.want {
			t.Fatalf("signingAddrFor(%d) = %s,%v; want %s,true", tc.next, got.Hex(), ok, tc.want.Hex())
		}
	}

	// Key order in the signer does not matter — the contract decides.
	signer.addrs = []common.Address{newKey, oldKey}
	if got, ok := s.signingAddrFor(99); !ok || got != oldKey {
		t.Fatalf("reordered keys: got %s,%v; want old key", got.Hex(), ok)
	}
}

func TestSigningAddrFor_NoKeyDesignated(t *testing.T) {
	// Node only holds the old key; from height 100 it has no valid key.
	signer := &mockSignerImpl{addrs: []common.Address{oldKey}}
	s := newMultiKeyState(t, signer, &mockSequencerVerifier{designated: rotateAt(100)}, nil)
	if _, ok := s.signingAddrFor(99); !ok {
		t.Fatal("old key should be valid before the rotation height")
	}
	if addr, ok := s.signingAddrFor(100); ok {
		t.Fatalf("no key should be valid at the rotation height, got %s", addr.Hex())
	}
}

func TestSigningAddrFor_VerifierErrorIsNotActive(t *testing.T) {
	// The designated sequencer cannot be looked up; even though the node
	// holds every key, an undecidable check must not produce a block.
	signer := &mockSignerImpl{addrs: []common.Address{oldKey, newKey}}
	verifier := &mockSequencerVerifier{
		designated: rotateAt(0), // newKey everywhere
		err:        errors.New("l1 rpc down"),
	}
	s := newMultiKeyState(t, signer, verifier, nil)
	if addr, ok := s.signingAddrFor(5); ok {
		t.Fatalf("expected inactive on verifier error, got %s", addr.Hex())
	}
}

func TestIsActiveSequencer_ReturnsSigningAddr(t *testing.T) {
	signer := &mockSignerImpl{addrs: []common.Address{oldKey, newKey}}
	s := newMultiKeyState(t, signer, &mockSequencerVerifier{designated: rotateAt(11)}, newMockSequencerHA(true))

	s.latestBlock = &BlockV2{Number: 9}
	if addr, ok := s.isActiveSequencer(); !ok || addr != oldKey {
		t.Fatalf("next=10: got %s,%v; want old key", addr.Hex(), ok)
	}
	s.latestBlock = &BlockV2{Number: 10}
	if addr, ok := s.isActiveSequencer(); !ok || addr != newKey {
		t.Fatalf("next=11: got %s,%v; want new key", addr.Hex(), ok)
	}

	// Followers never produce, whatever keys they hold.
	s.ha = newMockSequencerHA(false)
	if _, ok := s.isActiveSequencer(); ok {
		t.Fatal("HA follower must not be active")
	}
}

// End-to-end through produceBlock (HA leader): the same leader keeps
// producing across the rotation height and the block signature switches
// from the old key to the new one exactly there.
func TestProduceBlock_LeaderSwitchesKeyWithoutLeaderChange(t *testing.T) {
	signer := &mockSignerImpl{addrs: []common.Address{oldKey, newKey}}
	ha := newMockSequencerHA(true)
	s := newMultiKeyState(t, signer, &mockSequencerVerifier{designated: rotateAt(11)}, ha)

	for _, latest := range []uint64{8, 9, 10, 11} {
		s.latestBlock = &BlockV2{Number: latest}
		if !s.produceBlock(false) {
			t.Fatalf("produceBlock after height %d failed", latest)
		}
	}
	want := []common.Address{oldKey, oldKey, newKey, newKey} // heights 9,10,11,12
	if len(signer.signedBy) != len(want) {
		t.Fatalf("signed %d blocks, want %d", len(signer.signedBy), len(want))
	}
	for i, a := range want {
		if signer.signedBy[i] != a {
			t.Fatalf("block %d signed by %s, want %s", 9+i, signer.signedBy[i].Hex(), a.Hex())
		}
	}
}

// A leader that only holds the old key cannot sign from the rotation height
// on: it hands leadership to the holder of the designated key, retrying on
// every tick while it is still leader.
func TestIsActiveSequencer_LeaderWithoutDesignatedKeyHandsOff(t *testing.T) {
	signer := &mockSignerImpl{addrs: []common.Address{oldKey}}
	ha := newMockSequencerHA(true)
	s := newMultiKeyState(t, signer, &mockSequencerVerifier{designated: rotateAt(11)}, ha)

	// Before the rotation height the old key is designated: no hand-off.
	s.latestBlock = &BlockV2{Number: 9}
	if !isActive(s) || len(ha.handOffs) != 0 {
		t.Fatalf("next=10: active with no hand-off expected, hand-offs = %v", ha.handOffs)
	}

	// The next block is the rotation height: hand off to the new key's holder.
	s.latestBlock = &BlockV2{Number: 10}
	if isActive(s) {
		t.Fatal("next=11: must not be active without the designated key")
	}
	if len(ha.handOffs) != 1 || ha.handOffs[0] != newKey {
		t.Fatalf("hand-offs = %v, want one to the new key", ha.handOffs)
	}

	// Still leader on the following tick: try again.
	isActive(s)
	if len(ha.handOffs) != 2 {
		t.Fatalf("hand-offs = %d, want a retry on the next tick", len(ha.handOffs))
	}

	// Neither a follower nor a leader that cannot tell who is designated hands off.
	ha.leader = false
	isActive(s)
	ha.leader = true
	s.verifier = &mockSequencerVerifier{err: errors.New("l1 rpc down")}
	isActive(s)
	if len(ha.handOffs) != 2 {
		t.Fatalf("hand-offs = %d, want none from a follower or on a verifier error", len(ha.handOffs))
	}
}

func TestSignBlock_UnknownAddressFails(t *testing.T) {
	signer := &mockSignerImpl{addrs: []common.Address{oldKey}}
	s := newMultiKeyState(t, signer, &mockSequencerVerifier{}, nil)
	if err := s.signBlock(&BlockV2{Number: 1}, newKey); err == nil {
		t.Fatal("signing with an address the signer doesn't hold must fail")
	}
}
