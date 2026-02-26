package sequencer

import (
	"context"
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
type StateV2 struct {
	service.BaseService

	mtx sync.RWMutex

	// Core state
	latestBlock   *BlockV2
	sequencerMode bool // Whether the node is started in sequencer mode (has signer configured)

	// Dependencies
	l2Node   l2node.L2Node
	signer   Signer
	verifier SequencerVerifier
	logger   log.Logger

	// Block production
	blockTicker   *time.Ticker
	blockInterval time.Duration

	// Broadcast channel - blocks produced by this sequencer are sent here
	broadcastCh chan *BlockV2

	// Quit channel
	quitCh chan struct{}
}

// NewStateV2 creates a new StateV2 instance.
// sequencerMode is determined by whether a signer is provided.
func NewStateV2(
	l2Node l2node.L2Node,
	blockInterval time.Duration,
	logger log.Logger,
	verifier SequencerVerifier,
	signer Signer,
) (*StateV2, error) {
	if blockInterval <= 0 {
		blockInterval = DefaultBlockInterval
	}

	s := &StateV2{
		l2Node:        l2Node,
		signer:        signer,
		verifier:      verifier,
		sequencerMode: signer != nil,
		blockInterval: blockInterval,
		logger:        logger.With("module", "stateV2"),
		broadcastCh:   make(chan *BlockV2, 100),
		quitCh:        make(chan struct{}),
	}

	s.BaseService = *service.NewBaseService(logger, "StateV2", s)

	return s, nil
}

// OnStart implements service.Service.
// It initializes state from geth and starts block production if this node is the active sequencer.
func (s *StateV2) OnStart() error {
	// Initialize latest block from geth
	latestBlock, err := s.l2Node.GetLatestBlockV2()
	if err != nil {
		return fmt.Errorf("failed to get latest block: %w", err)
	}

	s.mtx.Lock()
	s.latestBlock = latestBlock
	s.mtx.Unlock()

	var seqAddr string
	var isActiveSequencer bool
	if s.signer != nil {
		seqAddr = s.signer.Address().Hex()
		// Check if this node is the active sequencer via L1 contract
		isActiveSequencer, err = s.signer.IsActiveSequencer(context.Background())
		if err != nil {
			s.logger.Error("Failed to check sequencer status", "error", err)
			isActiveSequencer = false
		}
	}

	s.logger.Info("StateV2 initialized",
		"latestHeight", s.latestBlock.Number,
		"latestHash", s.latestBlock.Hash.Hex(),
		"sequencerMode", s.sequencerMode,
		"isActiveSequencer", isActiveSequencer,
		"seqAddr", seqAddr)

	// Start block production if sequencer mode is enabled and this node is the active sequencer
	if s.sequencerMode && isActiveSequencer {
		go s.produceBlockRoutine()
	}

	return nil
}

// OnStop implements service.Service.
func (s *StateV2) OnStop() {
	s.logger.Info("Stopping StateV2")
	close(s.quitCh)
	if s.blockTicker != nil {
		s.blockTicker.Stop()
	}
}

// produceBlockRoutine is the main loop for block production.
func (s *StateV2) produceBlockRoutine() {
	s.blockTicker = time.NewTicker(s.blockInterval)
	defer s.blockTicker.Stop()

	s.logger.Info("Starting block production routine", "interval", s.blockInterval)

	for {
		select {
		case <-s.quitCh:
			s.logger.Info("Block production routine stopped")
			return
		case <-s.blockTicker.C:
			s.produceBlock()
		}
	}
}

// produceBlock produces a new block and broadcasts it.
func (s *StateV2) produceBlock() {
	s.mtx.Lock()
	parentHash := s.latestBlock.Hash
	s.mtx.Unlock()

	s.logger.Debug("Producing block", "parentHash", parentHash.Hex())

	// Request block data from geth (pass hash as bytes)
	block, collectedL1Msgs, err := s.l2Node.RequestBlockDataV2(parentHash.Bytes())
	if err != nil {
		s.logger.Error("Failed to request block data", "error", err)
		return
	}
	_ = collectedL1Msgs // TODO: log or use this info

	// Sign the block
	if err := s.signBlock(block); err != nil {
		s.logger.Error("Failed to sign block", "error", err)
		return
	}

	// ********************* RAFT HA *********************
	// TODO: add raft HA
	// ****************************************************

	// Apply the block to geth and update local state
	if err := s.ApplyBlock(block); err != nil {
		s.logger.Error("Failed to apply block", "error", err)
		return
	}

	// Send to broadcast channel
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

// signBlock signs the block hash with the signer.
func (s *StateV2) signBlock(block *BlockV2) error {
	if s.signer == nil {
		return fmt.Errorf("signer not set")
	}

	// Sign the block hash
	signature, err := s.signer.Sign(block.Hash.Bytes())
	if err != nil {
		return fmt.Errorf("failed to sign block: %w", err)
	}

	block.Signature = signature

	s.logger.Debug("Block signed", "number", block.Number, "hash", block.Hash.Hex(), "signer", s.signer.Address().Hex())
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

// BroadcastCh returns the channel for blocks to be broadcast.
// No lock needed - channel itself is thread-safe.
func (s *StateV2) BroadcastCh() <-chan *BlockV2 {
	return s.broadcastCh
}

// ApplyBlock applies a block to L2 and updates local state.
// This is the unified entry point for block application.
func (s *StateV2) ApplyBlock(block *BlockV2) error {
	// Apply to L2 execution layer
	if err := s.l2Node.ApplyBlockV2(block); err != nil {
		return err
	}

	// Update local state
	s.mtx.Lock()
	s.latestBlock = block
	s.mtx.Unlock()

	return nil
}

// GetBlockByNumber gets a block from l2node by number.
// Uses geth's eth_getBlockByNumber RPC internally.
func (s *StateV2) GetBlockByNumber(number uint64) (*BlockV2, error) {
	return s.l2Node.GetBlockByNumber(number)
}

// IsSequencerMode returns whether this node is started in sequencer mode.
// This means the node has a signer configured and can potentially produce blocks.
func (s *StateV2) IsSequencerMode() bool {
	return s.sequencerMode
}
