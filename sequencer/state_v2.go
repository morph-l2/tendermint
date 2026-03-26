package sequencer

import (
	"fmt"
	"sync"
	"time"

	"github.com/tendermint/tendermint/l2node"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/libs/service"
)

const (
	// DefaultBlockInterval is the default interval between blocks
	// TODO: make this configurable
	DefaultBlockInterval = 3000 * time.Millisecond
)

// StateV2 manages the state for centralized sequencer mode.
// It replaces the PBFT consensus state after the upgrade.
//
// Node roles:
//   - Fullnode (signer==nil): only applyRoutine runs (in reactor), no block production
//   - ActiveSequencer (signer!=nil, ha==nil): roleCheckRoutine + broadcastRoutine
//   - HA-Leader (signer!=nil, ha!=nil, ha.IsLeader()==true): roleCheckRoutine + broadcastRoutine
//   - HA-Follower (signer!=nil, ha!=nil, ha.IsLeader()==false): roleCheckRoutine (idle)
type StateV2 struct {
	service.BaseService

	mtx sync.RWMutex

	// Core state
	latestBlock *BlockV2

	// Dependencies
	l2Node   l2node.L2Node
	signer   Signer
	verifier SequencerVerifier
	sigStore *SignatureStore
	ha       SequencerHA // nil = single-node mode
	logger   log.Logger

	// Block production
	blockInterval time.Duration

	// Broadcast channel - non-HA self-produced blocks are sent here
	broadcastCh chan *BlockV2

	// Lifecycle
	quitCh chan struct{}
}

// NewStateV2 creates a new StateV2 instance.
// Node mode is determined by whether a signer and/or ha is provided.
// verifier is required when signer is configured (sequencer/HA nodes must verify blocks).
func NewStateV2(
	l2Node l2node.L2Node,
	blockInterval time.Duration,
	logger log.Logger,
	verifier SequencerVerifier,
	signer Signer,
	sigStore *SignatureStore,
	ha SequencerHA,
) (*StateV2, error) {
	if verifier == nil {
		return nil, fmt.Errorf("sequencer verifier is required for V2 mode")
	}
	if blockInterval <= 0 {
		blockInterval = DefaultBlockInterval
	}

	s := &StateV2{
		l2Node:        l2Node,
		signer:        signer,
		verifier:      verifier,
		sigStore:      sigStore,
		ha:            ha,
		blockInterval: blockInterval,
		logger:        logger.With("module", "stateV2"),
		broadcastCh:   make(chan *BlockV2, 100),
		quitCh:        make(chan struct{}),
	}

	s.BaseService = *service.NewBaseService(logger, "StateV2", s)

	return s, nil
}

// OnStart implements service.Service.
// Initializes state from geth. Nodes with a signer start roleCheckRoutine.
func (s *StateV2) OnStart() error {
	latestBlock, err := s.l2Node.GetLatestBlockV2()
	if err != nil {
		return fmt.Errorf("failed to get latest block: %w", err)
	}

	s.mtx.Lock()
	s.latestBlock = latestBlock
	s.mtx.Unlock()

	// Use local variable to avoid accessing s.latestBlock without lock in log statement.
	s.logger.Info("StateV2 initialized",
		"latestHeight", latestBlock.Number,
		"latestHash", latestBlock.Hash.Hex(),
		"hasSigner", s.signer != nil,
		"isHAMode", s.ha != nil)

	// Fullnode (no signer) does not produce blocks; applyRoutine is managed by the reactor.
	// Nodes with signer start roleCheckRoutine, which handles dynamic role detection.
	if s.signer != nil {
		go s.roleCheckRoutine()
	}

	return nil
}

// OnStop implements service.Service.
func (s *StateV2) OnStop() {
	s.logger.Info("Stopping StateV2")
	close(s.quitCh)
}

// roleCheckRoutine is the unified loop for role detection and block production.
// It runs for all nodes with a signer (ActiveSequencer, HA-Leader, HA-Follower).
// On each tick it checks isActiveSequencer(): if true, produces a block; otherwise idles.
// This enables bidirectional role transitions without restarting the service.
func (s *StateV2) roleCheckRoutine() {
	ticker := time.NewTicker(s.blockInterval)
	defer ticker.Stop()

	s.logger.Info("Starting role check routine", "interval", s.blockInterval)

	for {
		select {
		case <-s.quitCh:
			s.logger.Info("Role check routine stopped")
			return
		case <-ticker.C:
			if !s.isActiveSequencer() {
				continue
			}
			s.produceBlock()
		}
	}
}

// isActiveSequencer returns true if this node should produce the next block.
// For HA mode: must be Raft leader AND L1-designated sequencer.
// For single-node mode: must be L1-designated sequencer.
func (s *StateV2) isActiveSequencer() bool {
	// HA mode: must be Raft leader
	if s.ha != nil && !s.ha.IsLeader() {
		return false
	}

	s.mtx.RLock()
	lb := s.latestBlock
	s.mtx.RUnlock()
	if lb == nil {
		return false
	}
	nextHeight := lb.Number + 1

	ok, err := s.verifier.IsSequencerAt(s.signer.Address(), nextHeight)
	if err != nil {
		s.logger.Error("Failed to check sequencer status", "height", nextHeight, "err", err)
		return false
	}
	return ok
}

// produceBlock produces a new block, signs it, and either commits via Raft (HA)
// or applies locally and sends to broadcastCh (single-node).
func (s *StateV2) produceBlock() {
	s.mtx.RLock()
	parentHash := s.latestBlock.Hash
	s.mtx.RUnlock()

	s.logger.Debug("Producing block", "parentHash", parentHash.Hex())

	block, collectedL1Msgs, err := s.l2Node.RequestBlockDataV2(parentHash.Bytes())
	if err != nil {
		s.logger.Error("Failed to request block data", "error", err)
		return
	}
	_ = collectedL1Msgs

	if err := s.signBlock(block); err != nil {
		s.logger.Error("Failed to sign block", "error", err)
		return
	}

	if err := s.sigStore.SaveSignature(block.Hash, block.Signature); err != nil {
		panic(fmt.Sprintf("failed to save signature at height %d: %v", block.Number, err))
	}

	// HA mode: replicate via Raft consensus (data replication + majority ACK).
	// ha.Commit() only ensures majority of cluster nodes have received the block data.
	// Leader applies locally after Commit. Followers apply via Raft FSM internally.
	// Broadcast to P2P happens via ha.Subscribe() -> broadcastRoutine on all HA nodes.
	if s.ha != nil {
		if err := s.ha.Commit(block); err != nil {
			s.logger.Error("Failed to commit block via HA", "number", block.Number, "err", err)
			return
		}
		s.logger.Debug("Block committed via HA", "number", block.Number, "hash", block.Hash.Hex())
	}

	// Apply locally (HA leader + non-HA both apply here)
	if err := s.ApplyBlock(block); err != nil {
		s.logger.Error("Failed to apply block", "error", err)
		return
	}

	// Non-HA: broadcast via broadcastCh -> broadcastRoutine
	// HA: broadcast via ha.Subscribe() -> broadcastRoutine (don't write broadcastCh)
	if s.ha == nil {
		select {
		case s.broadcastCh <- block:
			s.logger.Debug("Block produced and queued for broadcast",
				"number", block.Number,
				"hash", block.Hash.Hex(),
				"txCount", len(block.Transactions),
				"collectedL1Msgs", collectedL1Msgs)
		default:
			s.logger.Error("Broadcast channel full, dropping block", "number", block.Number)
		}
	}
}

// signBlock signs the block hash with the signer.
func (s *StateV2) signBlock(block *BlockV2) error {
	if s.signer == nil {
		return fmt.Errorf("signer not set")
	}
	signature, err := s.signer.Sign(block.Hash.Bytes())
	if err != nil {
		return fmt.Errorf("failed to sign block: %w", err)
	}
	block.Signature = signature
	s.logger.Debug("Block signed", "number", block.Number, "hash", block.Hash.Hex(), "signer", s.signer.Address().Hex())
	return nil
}

// ApplyBlock applies a block to L2 and updates local state.
// Serialized by mutex to prevent concurrent application.
// Idempotent: silently skips blocks already applied.
func (s *StateV2) ApplyBlock(block *BlockV2) error {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	if s.latestBlock != nil && block.Number <= s.latestBlock.Number {
		return nil // idempotent: already applied or older block
	}

	if err := s.l2Node.ApplyBlockV2(block); err != nil {
		return err
	}
	s.latestBlock = block
	return nil
}

// LatestHeight returns the latest block height.
func (s *StateV2) LatestHeight() int64 {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	if s.latestBlock == nil {
		return 0
	}
	return int64(s.latestBlock.Number)
}

// LatestBlock returns the latest block.
func (s *StateV2) LatestBlock() *BlockV2 {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	return s.latestBlock
}

// BroadcastCh returns the channel for self-produced blocks (non-HA mode only).
func (s *StateV2) BroadcastCh() <-chan *BlockV2 {
	return s.broadcastCh
}

// GetBlockByNumber gets a block from l2node by number.
func (s *StateV2) GetBlockByNumber(number uint64) (*BlockV2, error) {
	return s.l2Node.GetBlockByNumber(number)
}

// HasSigner returns whether this node has a signer configured.
// Fullnode returns false; ActiveSequencer and HA nodes return true.
func (s *StateV2) HasSigner() bool {
	return s.signer != nil
}

// IsHAMode returns whether this node is in HA mode (ha != nil).
func (s *StateV2) IsHAMode() bool {
	return s.ha != nil
}

// IsHALeader returns whether this node is the current Raft leader.
// Returns false if not in HA mode.
func (s *StateV2) IsHALeader() bool {
	return s.ha != nil && s.ha.IsLeader()
}

// HASubscribe returns the HA block delivery channel.
// Panics if not in HA mode.
func (s *StateV2) HASubscribe() <-chan *BlockV2 {
	if s.ha == nil {
		panic("HASubscribe called but not in HA mode")
	}
	return s.ha.Subscribe()
}

// IsSequencerMode returns whether this node has a signer configured.
// Deprecated: use HasSigner() instead. TODO: remove after all callers are updated.
func (s *StateV2) IsSequencerMode() bool {
	return s.signer != nil
}
