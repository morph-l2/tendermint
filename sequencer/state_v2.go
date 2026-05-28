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
	// BlockInterval is the fallback interval for empty blocks (no txs).
	BlockInterval = 3000 * time.Millisecond
	// FastBlockInterval is the txpool polling interval.
	// When pending txs are found, a block is produced immediately.
	FastBlockInterval = 300 * time.Millisecond
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
	blockInterval     time.Duration // empty-block fallback interval (default 3s)
	fastBlockInterval time.Duration // txpool polling interval (default 300ms)

	// Broadcast channel - non-HA self-produced blocks are sent here
	broadcastCh chan *BlockV2
}

// NewStateV2 creates a new StateV2 instance.
// Node mode is determined by whether a signer and/or ha is provided.
// verifier is required when signer is configured (sequencer/HA nodes must verify blocks).
func NewStateV2(
	l2Node l2node.L2Node,
	logger log.Logger,
	verifier SequencerVerifier,
	signer Signer,
	sigStore *SignatureStore,
	ha SequencerHA,
) (*StateV2, error) {
	if verifier == nil {
		return nil, fmt.Errorf("sequencer verifier is required for V2 mode")
	}

	s := &StateV2{
		l2Node:            l2Node,
		signer:            signer,
		verifier:          verifier,
		sigStore:          sigStore,
		ha:                ha,
		blockInterval:     BlockInterval,
		fastBlockInterval: FastBlockInterval,
		logger:            logger.With("module", "stateV2"),
		broadcastCh:       make(chan *BlockV2, 100),
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

	// Start HA service at upgrade height. This initializes Raft and begins
	// leader election (bootstrap) or cluster join (follower).
	if s.ha != nil {
		if err := s.ha.Start(); err != nil {
			return fmt.Errorf("failed to start HA service: %w", err)
		}
	}

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
	if s.ha != nil {
		s.ha.Stop()
	}
}

func (s *StateV2) OnReset() error {
	return nil
}

// roleCheckRoutine is the unified loop for role detection and block production.
// It runs for all nodes with a signer (ActiveSequencer, HA-Leader, HA-Follower).
//
// Two timers drive block production:
//   - fastTicker (300ms): polls txpool via assembleBlock; produces immediately when txs found.
//   - slowTimer (3s): forces a block (even empty) to maintain chain liveness.
//
// When fastTicker produces a block, slowTimer is reset to avoid redundant empty blocks.
func (s *StateV2) roleCheckRoutine() {
	fastTicker := time.NewTicker(s.fastBlockInterval)
	slowTimer := time.NewTimer(s.blockInterval)
	defer fastTicker.Stop()
	defer slowTimer.Stop()

	s.logger.Info("Starting role check routine",
		"pollInterval", s.fastBlockInterval,
		"emptyBlockInterval", s.blockInterval)

	for {
		select {
		case <-s.Quit():
			s.logger.Info("Role check routine stopped")
			return

		case <-fastTicker.C:
			if !s.isActiveSequencer() {
				continue
			}
			block, collectedL1Msgs, err := s.assembleBlock()
			if err != nil {
				continue
			}
			if len(block.Transactions) == 0 && !collectedL1Msgs {
				continue // empty block, discard and wait for next tick
			}
			s.commitBlock(block, collectedL1Msgs)
			resetTimer(slowTimer, s.blockInterval)

		case <-slowTimer.C:
			if s.isActiveSequencer() {
				block, collectedL1Msgs, err := s.assembleBlock()
				if err == nil {
					s.commitBlock(block, collectedL1Msgs)
				}
			}
			slowTimer.Reset(s.blockInterval)
		}
	}
}

// resetTimer safely stops, drains, and resets a timer.
// t.Stop() returns false when the timer has already fired but t.C has not been
// consumed; the non-blocking drain prevents a stale fire from triggering the
// next select iteration.
func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
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

// assembleBlock calls geth Engine API to build a candidate block from the current
// txpool and pending L1 messages. This is a read-only operation with no side
// effects — the result can be safely discarded if the block has no work.
func (s *StateV2) assembleBlock() (*BlockV2, bool, error) {
	s.mtx.RLock()
	parentHash := s.latestBlock.Hash
	s.mtx.RUnlock()

	block, collectedL1Msgs, err := s.l2Node.RequestBlockDataV2(parentHash.Bytes())
	if err != nil {
		s.logger.Error("Failed to assemble block", "error", err)
		return nil, false, err
	}
	return block, collectedL1Msgs, nil
}

// commitBlock signs the assembled block and either commits via Raft (HA mode)
// or applies locally and sends to broadcastCh (single-node mode).
func (s *StateV2) commitBlock(block *BlockV2, collectedL1Msgs bool) {
	t0 := time.Now()

	tSign := time.Now()
	if err := s.signBlock(block); err != nil {
		s.logger.Error("Failed to sign block", "error", err)
		return
	}
	signDur := time.Since(tSign)

	if s.ha != nil {
		// HA mode: replicate via Raft. FSM callback handles ApplyBlock + SaveSignature
		// for both leader and follower. Broadcast via ha.Subscribe() -> broadcastRoutine.
		tCommit := time.Now()
		if err := s.ha.Commit(block); err != nil {
			s.logger.Error("Failed to commit block via HA", "number", block.Number, "err", err)
			return
		}
		commitDur := time.Since(tCommit)
		totalDur := time.Since(t0)

		s.logger.Debug("[PERF] commitBlock",
			"mode", "HA",
			"height", block.Number,
			"txCount", len(block.Transactions),
			"gasUsed", block.GasUsed,
			"sign_ms", float64(signDur.Microseconds())/1000.0,
			"raft_commit_ms", float64(commitDur.Microseconds())/1000.0,
			"total_ms", float64(totalDur.Microseconds())/1000.0,
		)
	} else {
		// Non-HA mode: apply locally (includes SaveSignature), broadcast via broadcastCh.
		tApply := time.Now()
		if err := s.ApplyBlock(block); err != nil {
			s.logger.Error("Failed to apply block", "error", err)
			return
		}
		applyDur := time.Since(tApply)
		totalDur := time.Since(t0)

		s.logger.Debug("[PERF] commitBlock",
			"mode", "single",
			"height", block.Number,
			"txCount", len(block.Transactions),
			"gasUsed", block.GasUsed,
			"sign_ms", float64(signDur.Microseconds())/1000.0,
			"apply_ms", float64(applyDur.Microseconds())/1000.0,
			"total_ms", float64(totalDur.Microseconds())/1000.0,
		)

		select {
		case s.broadcastCh <- block:
			s.logger.Debug("Block queued for broadcast",
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

// ApplyBlock saves the block signature and delegates to l2Node.ApplyBlockV2.
// Reorg detection and idempotent checks are handled in the Executor layer.
func (s *StateV2) ApplyBlock(block *BlockV2) error {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	if len(block.Signature) == 0 {
		return fmt.Errorf("ApplyBlock: block %d missing signature", block.Number)
	}

	// Save signature BEFORE applying to geth. If crash happens after Apply
	// but before SaveSignature, the block is on-chain but its signature is
	// lost — which can cause P2P peers to reject/disconnect when they cannot
	// verify the block. Saving first at worst leaves an orphan signature if
	// Apply fails, which is harmless.
	tSig := time.Now()
	if err := s.sigStore.SaveSignature(block.Hash, block.Signature); err != nil {
		return err
	}
	sigDur := time.Since(tSig)

	tGeth := time.Now()
	applied, err := s.l2Node.ApplyBlockV2(block)
	if err != nil {
		return err
	}
	gethDur := time.Since(tGeth)

	if applied {
		s.latestBlock = block
	}

	s.logger.Debug("[PERF] ApplyBlock",
		"height", block.Number,
		"txCount", len(block.Transactions),
		"gasUsed", block.GasUsed,
		"sigSave_ms", float64(sigDur.Microseconds())/1000.0,
		"geth_ms", float64(gethDur.Microseconds())/1000.0,
		"total_ms", float64((gethDur+sigDur).Microseconds())/1000.0,
	)

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
