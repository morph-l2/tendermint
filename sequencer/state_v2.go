package sequencer

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/morph-l2/go-ethereum/common"
	"github.com/tendermint/tendermint/l2node"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/libs/service"
	"github.com/tendermint/tendermint/types"
)

// BlockInterval and FastBlockInterval are the effective block-production
// intervals read by NewStateV2. This package carries no default: the owning
// binary must call SetBlockIntervals before starting a node with a signer.
// roleCheckRoutine builds a time.Ticker from FastBlockInterval, which panics on
// a non-positive value, so leaving these unset (zero) is a programming error on
// a signer node. The morph node sets them from its --sequencerBlockInterval /
// --sequencerFastBlockInterval flags (which own the defaults).
var (
	BlockInterval     time.Duration
	FastBlockInterval time.Duration
)

// SetBlockIntervals sets the sequencer block-production intervals. It must be
// called before NewStateV2 on any node with a signer. Both values must be
// positive and the fast (txpool poll) interval must be strictly smaller than
// the empty-block fallback interval; otherwise the previous values are left
// untouched and an error is returned so a bad configuration fails fast.
func SetBlockIntervals(blockInterval, fastBlockInterval time.Duration) error {
	if blockInterval <= 0 || fastBlockInterval <= 0 {
		return fmt.Errorf("block intervals must be positive: blockInterval=%s fastBlockInterval=%s", blockInterval, fastBlockInterval)
	}
	if fastBlockInterval >= blockInterval {
		return fmt.Errorf("fastBlockInterval (%s) must be less than blockInterval (%s)", fastBlockInterval, blockInterval)
	}
	BlockInterval = blockInterval
	FastBlockInterval = fastBlockInterval
	return nil
}

const (
	// backfillMaxDepth bounds how many missing ancestors a single parent-not-found
	// will backfill into the execution layer. reth buffers only a couple of
	// unpersisted blocks by default, so this leaves ample margin while keeping a
	// genuinely broken EL from turning into a long silent catch-up.
	backfillMaxDepth = 16

	// backfillCacheCapacity is how many recently applied blocks are retained so they
	// can be re-pushed after the EL loses its unpersisted head. Kept above
	// backfillMaxDepth so the oldest ancestor a backfill may need is still present.
	backfillCacheCapacity = 64

	// parentNotFoundError is the error text geth returns when a parent hash is
	// absent from its chain (see go-ethereum/eth/catalyst/l2_api.go,
	// AssembleL2BlockV2 and NewL2BlockV2). This module cannot import
	// morph-l2/node/types without an import cycle, so the string is duplicated
	// here — keep it in sync with types.ParentNotFoundError there.
	parentNotFoundError = "parent block not found"
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
	l2Node    l2node.L2Node
	signer    Signer
	verifier  SequencerVerifier
	l1Tracker L1Tracker // required: gates block production on L1 freshness
	sigStore  *SignatureStore
	ha        SequencerHA // nil = single-node mode
	logger    log.Logger
	metrics   *Metrics

	// Block production
	blockInterval     time.Duration // empty-block fallback interval (default 2s)
	fastBlockInterval time.Duration // txpool polling interval (default 300ms)

	// Broadcast channel - non-HA self-produced blocks are sent here
	broadcastCh chan *BlockV2

	// backfillCache holds recently applied blocks so they can be re-pushed when the
	// execution layer comes back having lost its unpersisted head. It is fed from
	// ApplyBlock, the one funnel every role goes through, so unlike the reactor's
	// broadcast-fed cache it cannot develop holes when a channel overflows.
	//
	// The buffer carries its own lock, so backfillMissingBlocks reads it without
	// holding mtx.
	backfillCache *BlockRingBuffer
}

// NewStateV2 creates a new StateV2 instance.
// Node mode is determined by whether a signer and/or ha is provided.
// verifier is required when signer is configured (sequencer/HA nodes must verify blocks).
func NewStateV2(
	l2Node l2node.L2Node,
	logger log.Logger,
	verifier SequencerVerifier,
	l1Tracker L1Tracker,
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
		l1Tracker:         l1Tracker,
		sigStore:          sigStore,
		ha:                ha,
		blockInterval:     BlockInterval,
		fastBlockInterval: FastBlockInterval,
		logger:            logger.With("module", "stateV2"),
		metrics:           NopMetrics(),
		broadcastCh:       make(chan *BlockV2, 100),
		backfillCache:     NewBlockRingBuffer(backfillCacheCapacity),
	}

	s.BaseService = *service.NewBaseService(logger, "StateV2", s)

	return s, nil
}

// SetMetrics wires the sequencer metrics. Called once after construction
// (before OnStart). When unset, metrics default to a no-op implementation.
func (s *StateV2) SetMetrics(m *Metrics) {
	if m != nil {
		s.metrics = m
	}
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
	s.backfillCache.Clear()
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
//   - slowTimer (2s): forces a block (even empty) to maintain chain liveness.
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
			if s.produceBlock(true) {
				resetTimer(slowTimer, s.blockInterval)
			}
		case <-slowTimer.C:
			s.produceBlock(false)
			resetTimer(slowTimer, s.blockInterval)
		}
	}
}

func (s *StateV2) produceBlock(skipEmptyBlock bool) bool {
	if !s.isActiveSequencer() {
		return false
	}
	block, collectedL1Msgs, err := s.assembleBlock()
	if err != nil {
		_ = s.transferLeader()
		return false
	}
	if skipEmptyBlock && len(block.Transactions) == 0 && !collectedL1Msgs {
		return false // empty block, discard and wait for next tick
	}
	err = s.commitBlock(block, collectedL1Msgs)
	if err != nil {
		_ = s.transferLeader()
		return false
	}
	return true
}

func (s *StateV2) transferLeader() error {
	if s.ha == nil {
		return nil
	}
	err := s.ha.TransferLeader()
	if err != nil {
		s.logger.Error("Failed to transfer leader", "err", err)
		return err
	}
	s.logger.Info("raft: Transfer leader succeeded")
	return nil
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
func (s *StateV2) isActiveSequencer() (active bool) {
	defer func() {
		s.metrics.SetActiveSequencer(active)
	}()

	// HA mode: must be Raft leader
	if s.ha != nil && !s.ha.IsLeader() {
		return false
	}

	// L1 tracker: stop producing if L1 RPC is stale (we may be blind to
	// SequencerUpdated events on L1 and could produce as a revoked sequencer).
	if s.l1Tracker.IsHalt() {
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
// txpool and pending L1 messages. The candidate carries no commitment: it can be
// discarded if it turns out to hold no work.
//
// It is NOT side-effect free. When the execution layer has lost the head we build
// on, assembling repairs it — backfilling the missing blocks and then assembling
// again — so the caller gets a block in the same round instead of having to retry.
// Do not call this from a path that must leave the execution layer untouched.
func (s *StateV2) assembleBlock() (*BlockV2, bool, error) {
	s.mtx.RLock()
	parentHash := s.latestBlock.Hash
	s.mtx.RUnlock()

	tAssemble := time.Now()
	block, collectedL1Msgs, err := s.l2Node.RequestBlockDataV2(parentHash.Bytes())
	if err != nil {
		if strings.Contains(err.Error(), parentNotFoundError) {
			if rErr := s.backfillMissingBlocks(parentHash); rErr != nil {
				s.logger.Error("Backfill failed", "parentHash", parentHash.Hex(), "err", rErr)
				return nil, false, rErr
			}
			block, collectedL1Msgs, err = s.l2Node.RequestBlockDataV2(parentHash.Bytes())
		}
	}
	if err != nil {
		s.logger.Error("Failed to assemble block", "error", err)
		return nil, false, err
	}
	// Measured from the first attempt, so a round that had to backfill reports the
	// whole recovery (failed assemble + the re-pushed blocks + the retry) as its
	// assemble duration. That is the real time to get a block, but it does show up
	// as an outlier when reading this metric as "how slow is geth at assembling".
	assembleDur := time.Since(tAssemble)
	s.metrics.ObserveAssembleDuration(assembleDur)
	s.logger.Debug("[PERF] assembleBlock",
		"height", block.Number,
		"hash", block.Hash.Hex(),
		"txCount", len(block.Transactions),
		"collectedL1Msgs", collectedL1Msgs,
		"duration_ms", float64(assembleDur.Microseconds())/1000.0,
	)
	return block, collectedL1Msgs, nil
}

// commitBlock signs the assembled block and either commits via Raft (HA mode)
// or applies locally and sends to broadcastCh (single-node mode).
func (s *StateV2) commitBlock(block *BlockV2, collectedL1Msgs bool) error {
	t0 := time.Now()

	tSign := time.Now()
	if err := s.signBlock(block); err != nil {
		s.logger.Error("Failed to sign block", "error", err)
		return err
	}
	signDur := time.Since(tSign)
	s.metrics.ObserveSignDuration(signDur)

	if s.ha != nil {
		// HA mode: replicate via Raft. FSM callback handles ApplyBlock + SaveSignature
		// for both leader and follower. Broadcast via ha.Subscribe() -> broadcastRoutine.
		tCommit := time.Now()
		if err := s.ha.Commit(block); err != nil {
			s.logger.Error("Failed to commit block via HA", "number", block.Number, "err", err)
			return err
		}
		commitDur := time.Since(tCommit)
		totalDur := time.Since(t0)
		s.metrics.ObserveCommitDuration("ha", totalDur)

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
			return err
		}
		applyDur := time.Since(tApply)
		totalDur := time.Since(t0)
		s.metrics.ObserveCommitDuration("single", totalDur)

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
			s.metrics.IncBroadcastChannelDropped()
			s.logger.Error("Broadcast channel full, dropping block", "number", block.Number)
		}
	}
	s.metrics.IncBlocksProduced()
	return nil
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

// backfillMissingBlocks re-pushes the block identified by hash, plus any of its
// ancestors the execution layer is also missing, so the caller can retry the
// operation that hit parent-not-found.
//
// It takes no lock: backfillCache and the executor are each independently
// synchronised, and no StateV2 field is read or written here.
//
// The EL's head is read first so the gap is known exactly — only blocks above
// that head are pushed, and the walk must link to it by hash. That turns both
// failure modes into an explicit refusal instead of something discovered from a
// rejected apply:
//
//   - The cache no longer covers the gap: a cold cache, or a gap wider than
//     backfillMaxDepth. Nothing is applied and the caller keeps its own error.
//   - The cached chain does not descend from the EL's head, so the EL is on a
//     competing branch rather than merely behind. Pushing our blocks would be
//     *accepted* there (the competing block's parent is present, so
//     NewL2BlockV2 validates and WriteStateAndSetHead reorgs), silently moving
//     the EL onto our history. Reorg handling is out of scope for a gap filler,
//     so refuse and leave the EL as it is for an operator to inspect.
//
// Blocks are applied oldest-first because NewL2BlockV2 rejects a block whose
// parent it does not hold.
func (s *StateV2) backfillMissingBlocks(hash common.Hash) error {
	head, err := s.l2Node.GetLatestBlockV2()
	if err != nil {
		return fmt.Errorf("read execution layer head: %w", err)
	}

	var missing []*BlockV2
	for h := hash; ; {
		b := s.backfillCache.GetByHash(h)
		if b == nil {
			return fmt.Errorf("block %s not in cache, execution layer head is %d (%s)",
				h.Hex(), head.Number, head.Hash.Hex())
		}
		if b.Number <= head.Number {
			return fmt.Errorf("execution layer is on a competing branch: "+
				"cached block %d (%s) is not above head %d (%s) and does not link to it",
				b.Number, b.Hash.Hex(), head.Number, head.Hash.Hex())
		}
		missing = append(missing, b)
		if b.ParentHash == head.Hash {
			break
		}
		if len(missing) >= backfillMaxDepth {
			return fmt.Errorf("gap exceeds backfillMaxDepth: head %d up to block %d, limit %d",
				head.Number, missing[0].Number, backfillMaxDepth)
		}
		h = b.ParentHash
	}

	// Unreachable today: the loop's only break happens after an append, and every
	// other exit returns. Guarded anyway because the reporting below indexes
	// missing, and a panic here would take down block production.
	if len(missing) == 0 {
		return fmt.Errorf("nothing to backfill for %s", hash.Hex())
	}

	for i := len(missing) - 1; i >= 0; i-- {
		b := missing[i]
		if _, err := s.l2Node.ApplyBlockV2(b); err != nil {
			return fmt.Errorf("backfill block %d: %w", b.Number, err)
		}
	}
	s.logger.Info("Backfilled blocks into execution layer",
		"count", len(missing),
		"from", missing[len(missing)-1].Number,
		"to", missing[0].Number)
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
	s.metrics.ObserveApplyDuration("sig", sigDur)

	tGeth := time.Now()
	applied, err := s.l2Node.ApplyBlockV2(block)
	if err != nil && strings.Contains(err.Error(), parentNotFoundError) {
		// The EL restarted without persisting its head, so this block no longer
		// connects. Re-push the missing ancestors and retry once. Any failure
		// returns the original error, leaving behaviour identical to before.
		if rErr := s.backfillMissingBlocks(block.ParentHash); rErr != nil {
			s.logger.Error("Backfill failed", "number", block.Number, "err", rErr)
			return err
		}
		applied, err = s.l2Node.ApplyBlockV2(block)
	}
	if err != nil {
		return err
	}
	gethDur := time.Since(tGeth)
	s.metrics.ObserveApplyDuration("geth", gethDur)

	if applied {
		// Block attributes are recorded on every node that applies a block
		// (leader, HA follower, fullnode), so these are role-independent.
		// Interval uses block timestamps (not wall-clock) to stay accurate
		// even while catching up.
		if s.latestBlock != nil && block.Timestamp >= s.latestBlock.Timestamp {
			s.metrics.ObserveBlockIntervalSeconds(block.Timestamp - s.latestBlock.Timestamp)
		}
		s.metrics.ObserveBlockSizeBytes(types.BlockV2ToProto(block).Size())
		s.metrics.ObserveBlockTxs(len(block.Transactions))
		s.latestBlock = block
		s.backfillCache.Add(block)
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
